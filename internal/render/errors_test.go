package render

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/command"
	"github.com/sriannamalai/CDB.CLI/internal/couch"
	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
)

func TestErrorMessage(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "401",
			err:  couch.NewError(401, "unauthorized", "Name or password is incorrect.", "read", `user "admin" at localhost:5984`),
			want: `Login failed for admin at localhost:5984. Check the password with "profiles" or "connect".`,
		},
		{
			// --anonymous, or a profile with auth = "none", sends nothing at
			// all. "Check the password" is advice about a password that does
			// not exist; what the operator needs is to supply one.
			name: "401 with no credentials",
			err: func() error {
				e := couch.NewError(401, "unauthorized", "You are not authorized to access this db.", "read", `document "doc1" in "mydb"`)
				e.Auth = couch.AuthNone
				return e
			}(),
			want: `The server requires credentials for read document "doc1" in "mydb". Connect with a username and password, or set CDB_USER and CDB_PASSWORD.`,
		},
		{
			name: "403",
			err:  couch.NewError(403, "forbidden", "You are not a db or server admin.", "delete", `database "mydb"`),
			want: `You do not have permission to delete database "mydb".`,
		},
		{
			name: "404 database",
			err:  couch.NewError(404, "not_found", "Database does not exist.", "read", `database "mydb"`),
			want: `Database "mydb" does not exist. "ls /" lists databases.`,
		},
		{
			name: "404 document",
			err:  couch.NewError(404, "not_found", "missing", "read", `document "doc1" in "mydb"`),
			want: `Document "doc1" was not found in "mydb".`,
		},
		{
			// A path below a design document that is not a view is one of the
			// design document's attachments — design documents carry
			// attachments like any other document. The sentence has to say so,
			// or "Attachment \"y\" was not found on \"mydb\"" leaves the
			// operator wondering which document was searched.
			name: "404 attachment of a design document",
			err:  couch.NewError(404, "not_found", "missing", "read", `attachment "logo.png" of design document "app" in "mydb"`),
			want: `Attachment "logo.png" was not found on design document "app".`,
		},
		{
			name: "404 attachment of an ordinary document",
			err:  couch.NewError(404, "not_found", "missing", "read", `attachment "photo.jpg" of "doc1" in "mydb"`),
			want: `Attachment "photo.jpg" was not found on document "doc1".`,
		},
		{
			name: "409",
			err:  couch.NewError(409, "conflict", "Document update conflict.", "write", `document "doc1" in "mydb"`),
			want: `Document "doc1" was changed by someone else. "cat doc1" shows the latest version.`,
		},
		{
			name: "connection refused",
			err:  couch.NewError(couch.StatusUnreachable, "connection_refused", "connection refused", "read", "server localhost:5984"),
			want: `Could not reach localhost:5984. Is CouchDB running?`,
		},
		{
			name: "412 database exists",
			err:  couch.NewError(412, "file_exists", "The database could not be created, the file already exists.", "create", `database "mydb"`),
			want: `Database "mydb" already exists.`,
		},
		{
			// "ls /nosuchdb" reads through AllDocs, which targets "documents in
			// %q" rather than "database %q" on a 404 — see couch/database.go.
			name: "404 documents in a missing database",
			err:  couch.NewError(404, "not_found", "Database does not exist.", "list", `documents in "nosuchdb"`),
			want: `Database "nosuchdb" does not exist. "ls /" lists databases.`,
		},
		{
			// A document target's quoted string is a document id, not a user
			// name. Before userAndHost required the "user ... at ..." shape,
			// this target rendered "Login failed for doc1 at ...". Reachable
			// today only if some future caller mis-targets a 401; doDecode,
			// GetRev and sessionTransport.login all now build the proper
			// user-and-host target instead.
			name: "401 with a document-shaped target never uses the document id as the user",
			err:  couch.NewError(401, "unauthorized", "Name or password is incorrect.", "read", `document "doc1" in "mydb"`),
			want: `Login failed for the configured user at "mydb". Check the password with "profiles" or "connect".`,
		},
		{
			// A bare server target has no quoted string at all, so the old
			// code already fell back to "the configured user" here; this pins
			// that the strict parse keeps doing so.
			name: "401 with a server-shaped target falls back to the generic user",
			err:  couch.NewError(401, "unauthorized", "Name or password is incorrect.", "read", "server localhost:5984"),
			want: `Login failed for the configured user at localhost:5984. Check the password with "profiles" or "connect".`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ErrorMessage(tc.err, false); got != tc.want {
				t.Errorf("ErrorMessage = %q,\n           want %q", got, tc.want)
			}
		})
	}
}

func TestErrorMessageVerboseAddsTheRawDetail(t *testing.T) {
	err := couch.NewError(404, "not_found", "missing", "read", `document "doc1" in "mydb"`)
	got := ErrorMessage(err, true)
	if !strings.Contains(got, "404") || !strings.Contains(got, "not_found") || !strings.Contains(got, "missing") {
		t.Errorf("verbose message %q is missing the raw status, name or reason", got)
	}
}

func TestErrorMessagePassesThroughOtherErrors(t *testing.T) {
	if got := ErrorMessage(errors.New("something broke"), false); got != "something broke" {
		t.Errorf("ErrorMessage = %q", got)
	}
	if got := ErrorMessage(nil, false); got != "" {
		t.Errorf("ErrorMessage(nil) = %q, want empty", got)
	}
}

// TestErrorMessageNeverLeaksACredential guards spec section 11's "no secret in
// an error message" rule. couch.Client.Host strips userinfo before an error
// target is ever built (Client.base keeps it for authentication, Client.host
// and Client.safe never do), so a target built the way the couch package
// really builds one — "server "+c.Host() — must never carry the password.
func TestErrorMessageNeverLeaksACredential(t *testing.T) {
	c, err := couch.New(couch.Config{URL: "http://admin:s3cr3t-password@localhost:5984", Auth: couch.AuthNone})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	ce := couch.NewError(couch.StatusUnreachable, "connection_refused", "connection refused", "read", "server "+c.Host())
	got := ErrorMessage(ce, true)
	if strings.Contains(got, "s3cr3t-password") {
		t.Errorf("ErrorMessage leaked the credential: %q", got)
	}
	if !strings.Contains(got, "localhost:5984") {
		t.Errorf("ErrorMessage = %q, want it to name the host", got)
	}
}

// TestErrorMessageForAnUnauthorizedGetRevNamesTheRealUser is the end-to-end
// regression for the GetRev finding: put, rm, cp and attach all call GetRev
// for optimistic concurrency, and under jwt/none auth there is no session
// transport to retry a 401 first, so a HEAD can answer 401 directly. Before
// GetRev built the same user-and-host target doDecode does, this rendered
// "Login failed for doc1 at ..." — the document id, not the user.
func TestErrorMessageForAnUnauthorizedGetRevNamesTheRealUser(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("HEAD", "/mydb/doc1", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	c, err := couch.New(couch.Config{URL: srv.URL(), Auth: couch.AuthJWT, Username: "admin", Secret: "sometoken"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	_, err = c.GetRev(context.Background(), "mydb", "doc1")
	if err == nil {
		t.Fatal("GetRev returned no error for a 401")
	}
	// Since 1.2 a 401 under a bearer token reads as a rejected token rather
	// than a password failure, so the user and host live in the error's target
	// rather than in the sentence; the finding is pinned there instead.
	got := ErrorMessage(err, false)
	want := "The server rejected the token."
	if got != want {
		t.Errorf("ErrorMessage = %q,\n           want %q", got, want)
	}
	ce, ok := couch.AsError(err)
	if !ok {
		t.Fatalf("GetRev returned %T, want a *couch.Error", err)
	}
	if wantTarget := fmt.Sprintf("user %q at %s", "admin", c.Host()); ce.Target != wantTarget {
		t.Errorf("target = %q, want %q", ce.Target, wantTarget)
	}
	if strings.Contains(ce.Target, "doc1") {
		t.Errorf("target = %q, the document id must not appear as the user", ce.Target)
	}
}

// TestRejectedTokenOnCouchDB30 is the whole sentence an operator sees when a
// bearer token goes to a server too old to have a JWT handler. Hint is the
// literal string command.jwtVersionHint builds; TestJWTVersionHint above is
// what pins that it builds exactly this.
func TestRejectedTokenOnCouchDB30(t *testing.T) {
	e := couch.NewError(http.StatusUnauthorized, "unauthorized",
		"The server rejected the token.", "authenticate", "server localhost:15985")
	e.Auth = couch.AuthJWT
	e.Hint = "JWT authentication needs CouchDB 3.1 or later; this server is 3.0.0."

	got := ErrorMessage(e, false)
	want := "The server rejected the token. JWT authentication needs CouchDB 3.1 or later; this server is 3.0.0."
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if strings.Contains(got, "password") {
		t.Errorf("a rejected token must not be reported as a password problem: %q", got)
	}
}

// TestRejectedTokenOnASupportedServer keeps the bare sentence bare: the
// version clause is only true below 3.1.
func TestRejectedTokenOnASupportedServer(t *testing.T) {
	e := couch.NewError(http.StatusUnauthorized, "unauthorized",
		"The server rejected the token.", "authenticate", "server localhost:15984")
	e.Auth = couch.AuthJWT
	e.Hint = ""

	got := ErrorMessage(e, false)
	want := "The server rejected the token."
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestVerboseSyntheticJWT30DoesNotRepeatTheSentence pins the fix for the
// pre-3.1 synthetic 401: it carries no real server reason (it is
// command.dial's own literal), so --verbose must not print it a second time
// in brackets.
func TestVerboseSyntheticJWT30DoesNotRepeatTheSentence(t *testing.T) {
	e := couch.NewError(http.StatusUnauthorized, "unauthorized",
		"The server rejected the token.", "authenticate", "server localhost:15985")
	e.Auth = couch.AuthJWT
	e.Hint = "JWT authentication needs CouchDB 3.1 or later; this server is 3.0.0."

	got := ErrorMessage(e, true)
	if strings.Count(got, "The server rejected the token.") != 1 {
		t.Errorf("verbose message repeats the sentence: %q", got)
	}
}

// TestVerboseRealJWT401KeepsTheServerReason pins that a genuine 3.1+ 401
// (a real server reason, distinct from the synthetic sentence) still gets
// the bracketed detail --verbose exists to show.
func TestVerboseRealJWT401KeepsTheServerReason(t *testing.T) {
	e := couch.NewError(http.StatusUnauthorized, "unauthorized",
		"exp not in future", "authenticate", "server localhost:15984")
	e.Auth = couch.AuthJWT

	got := ErrorMessage(e, true)
	want := "The server rejected the token. [status 401 unauthorized: exp not in future]"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestProxyRejectionSentence(t *testing.T) {
	e := couch.NewError(401, "unauthorized", "The server did not act on the proxy credentials.",
		"authenticate", couch.UnauthorizedTarget("ops", "couch.example.com:5984"))
	e.Auth = couch.AuthProxy
	want := `The server did not accept the proxy credentials for ops at couch.example.com:5984. Check the shared secret and that proxy authentication is enabled on the server.`
	if got := ErrorMessage(e, false); got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

// --verbose may add the status bracket but must never add the token, the
// secret, or anything derived from either.
func TestProxyRejectionSentenceVerboseCarriesNoSecret(t *testing.T) {
	e := couch.NewError(401, "unauthorized", "The server did not act on the proxy credentials.",
		"authenticate", couch.UnauthorizedTarget("ops", "couch.example.com:5984"))
	e.Auth = couch.AuthProxy
	got := ErrorMessage(e, true)
	for _, forbidden := range []string{"proxysecret", "9fd98e6f"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("the verbose message leaked %q: %s", forbidden, got)
		}
	}
	if !strings.Contains(got, "[status 401 unauthorized:") {
		t.Errorf("the verbose bracket is missing: %s", got)
	}
}

func TestIAMExchangeFailureSentence(t *testing.T) {
	e := couch.NewError(401, couch.IAMExchangeFailed, "Provided API key could not be found.",
		"authenticate", "server example.cloudantnosqldb.appdomain.cloud")
	e.Auth = couch.AuthIAM
	want := "IBM IAM did not issue a token for the API key. Check the key."
	if got := ErrorMessage(e, false); got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	// IBM's own errorMessage is the one extra thing --verbose is for.
	if got := ErrorMessage(e, true); !strings.Contains(got, "Provided API key could not be found.") {
		t.Errorf("the verbose form drops IBM's reason: %s", got)
	}
}

func TestIAMTokenRejectedSentence(t *testing.T) {
	e := couch.NewError(401, "unauthorized", "credentials expired", "read",
		couch.UnauthorizedTarget("", "example.cloudantnosqldb.appdomain.cloud"))
	e.Auth = couch.AuthIAM
	want := "The server rejected the IAM token at example.cloudantnosqldb.appdomain.cloud."
	if got := ErrorMessage(e, false); got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

// A sentence cdb composed itself still gets the --verbose bracket, built from
// the status and reason the error carries rather than from a reachable
// *couch.Error.
func TestErrorMessageAppendsTheStatusToASentenceError(t *testing.T) {
	err := &command.SentenceError{
		Text:   "This server has no search service running; Clouseau must be installed and started for _search indexes.",
		Status: 503,
		Name:   "service unavailable",
		Reason: "Search is not available",
	}
	if got := ErrorMessage(err, false); got != err.Text {
		t.Errorf("plain = %q, want the sentence alone", got)
	}
	want := err.Text + " [status 503 service unavailable: Search is not available]"
	if got := ErrorMessage(err, true); got != want {
		t.Errorf("verbose = %q\nwant       %q", got, want)
	}
}

// A sentence with no server behind it — nothing sets one today, but the type
// allows it — must not grow an empty bracket.
func TestErrorMessageOmitsTheBracketWhenThereIsNoStatus(t *testing.T) {
	err := &command.SentenceError{Text: "Something cdb decided on its own."}
	if got := ErrorMessage(err, true); got != err.Text {
		t.Errorf("verbose = %q, want the sentence alone", got)
	}
}

func TestForbiddenAdminEndpointNamesTheAction(t *testing.T) {
	e := couch.NewError(403, "unauthorized", "You are not a server admin.",
		couch.AdminOp, "changing configuration")
	if got := ErrorMessage(e, false); got != "Server administrator rights are required for changing configuration." {
		t.Errorf("message = %q", got)
	}
	want := "Server administrator rights are required for changing configuration. [status 403 unauthorized: You are not a server admin.]"
	if got := ErrorMessage(e, true); got != want {
		t.Errorf("verbose message = %q", got)
	}
}

func TestForbiddenElsewhereKeepsTheOldSentence(t *testing.T) {
	e := couch.NewError(403, "forbidden", "no", "read", `document "d" in "mydb"`)
	if got := ErrorMessage(e, false); got != `You do not have permission to read document "d" in "mydb".` {
		t.Errorf("message = %q", got)
	}
}
