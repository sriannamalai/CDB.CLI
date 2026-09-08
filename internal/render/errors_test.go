package render

import (
	"errors"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/couch"
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
