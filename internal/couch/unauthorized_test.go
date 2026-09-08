package couch

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
)

// A 401 for a client that sends no credentials at all is not a wrong password:
// there is no password. The error has to carry that, so the message can tell
// the operator to connect with credentials rather than to check the ones they
// do not have. The target stays the operation's own, because there is no user
// name to name.
func TestUnauthorizedAnonymousKeepsTheOperationTarget(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb/doc1", 401, `{"error":"unauthorized","reason":"You are not authorized to access this db."}`)
	c, err := New(Config{URL: srv.URL(), Auth: AuthNone})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = c.GetDocument(context.Background(), "mydb", "doc1", GetOptions{})
	e, ok := AsError(err)
	if !ok {
		t.Fatalf("err = %#v, want a couch.Error", err)
	}
	if e.Auth != AuthNone {
		t.Errorf("Auth = %q, want %q", e.Auth, AuthNone)
	}
	if want := `document "doc1" in "mydb"`; e.Target != want {
		t.Errorf("Target = %s, want %s", e.Target, want)
	}
}

// Credentials in the URL's userinfo are credentials: net/http turns them into a
// Basic header, so a 401 there really is a wrong password and must keep the
// user-and-host target.
func TestUnauthorizedWithURLCredentialsNamesTheUser(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb/doc1", 401, `{"error":"unauthorized","reason":"Name or password is incorrect."}`)
	u, err := url.Parse(srv.URL())
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword("admin", "wrong")
	c, err := New(Config{URL: u.String(), Auth: AuthNone, Username: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = c.GetDocument(context.Background(), "mydb", "doc1", GetOptions{})
	e, ok := AsError(err)
	if !ok {
		t.Fatalf("err = %#v, want a couch.Error", err)
	}
	if e.Auth == AuthNone {
		t.Errorf("Auth = %q, want it not to claim the client is anonymous", e.Auth)
	}
	if !strings.HasPrefix(e.Target, `user "admin" at `) {
		t.Errorf("Target = %s, want the user-and-host form", e.Target)
	}
}
