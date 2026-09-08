package render

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

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
	got := ErrorMessage(err, false)
	want := fmt.Sprintf(`Login failed for admin at %s. Check the password with "profiles" or "connect".`, c.Host())
	if got != want {
		t.Errorf("ErrorMessage = %q,\n           want %q", got, want)
	}
	if strings.Contains(got, "doc1") {
		t.Errorf("ErrorMessage = %q, the document id must not appear as the user", got)
	}
}
