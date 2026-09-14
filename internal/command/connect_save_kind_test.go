package command

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/config"
	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// withUser returns srv's URL with user as its userinfo, which is how an
// operator names a server and a user in one argument.
func withUser(url, user string) string {
	return strings.Replace(url, "http://", "http://"+user+"@", 1)
}

// "connect --save --url http://alice@host" with no password anywhere connects
// anonymously, and what is saved has to be what it connected with: auth =
// "none" and no user name. A profile that says "none" and names alice reads
// like a login nobody ever made, and "profiles list" shows a user the next
// connection will not send.
func TestSavedAnonymousURLKeepsNoUserName(t *testing.T) {
	srv := couchtest.New(t)
	path := withDeps(t, config.Defaults(), nil)
	var out bytes.Buffer
	s := session.New(strings.NewReader(""), &out, &out)
	t.Cleanup(func() { _ = s.Detach() })

	if _, err := connectWith(t, s, withUser(srv.URL(), "alice"), "--save", "--as", "anon"); err != nil {
		t.Fatalf("connect --save: %v (output: %s)", err, out.String())
	}
	back, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := back.Profile("anon")
	if !ok {
		t.Fatalf("profiles = %v, want one named anon", back.Profiles)
	}
	if p.Auth != "none" {
		t.Errorf("auth = %q, want %q for a connection that sent no credentials", p.Auth, "none")
	}
	if p.Username != "" {
		t.Errorf("username = %q, want none alongside auth = \"none\"", p.Username)
	}
}

// The other half of the same rule: a password the prompt collected makes it a
// session login, and that profile does name the user it logged in as.
func TestSavedPromptedURLKeepsTheUserName(t *testing.T) {
	srv := couchtest.New(t)
	path := withDeps(t, config.Defaults(), nil)
	s, out := guidedSession("s3cret\n")
	t.Cleanup(func() { _ = s.Detach() })

	if _, err := connectWith(t, s, withUser(srv.URL(), "alice"), "--save", "--as", "alice"); err != nil {
		t.Fatalf("connect --save: %v (output: %s)", err, out.String())
	}
	back, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := back.Profile("alice")
	if !ok {
		t.Fatalf("profiles = %v, want one named alice", back.Profiles)
	}
	if p.Auth != "session" || p.Username != "alice" {
		t.Errorf("profile = %+v, want a session login as alice", p)
	}
	if got, err := CurrentDeps().Secrets.Get("alice"); err != nil || got != "s3cret" {
		t.Errorf("keyring secret = %q, %v; want the typed password", got, err)
	}
}
