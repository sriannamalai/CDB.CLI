package command

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/config"
	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// envSession returns a non-interactive session and the config the deps point
// at, with env supplying the environment.
func envSession(t *testing.T, env map[string]string) (*session.Session, *bytes.Buffer) {
	t.Helper()
	withDeps(t, config.Defaults(), env)
	var out bytes.Buffer
	s := session.New(strings.NewReader(""), &out, &out)
	t.Cleanup(func() { _ = s.Detach() })
	return s, &out
}

// A user name in the URL is part of the target the operator named, so it wins
// over CDB_USER -- which a shell may have exported for an entirely different
// server. Logging in as the environment's user against the URL's server is the
// same silent redirection #36 fixed for CDB_URL.
func TestURLUserNameWinsOverCDBUser(t *testing.T) {
	srv := couchtest.New(t)
	s, out := envSession(t, map[string]string{"CDB_USER": "bob", "CDB_PASSWORD": "hunter2"})

	if _, err := connectWith(t, s, withUser(srv.URL(), "alice")); err != nil {
		t.Fatalf("connect: %v (output: %s)", err, out.String())
	}
	name, password := loginCredentials(t, srv)
	if name != "alice" || password != "hunter2" {
		t.Errorf("login sent %q/%q, want the URL's user with the environment's password", name, password)
	}
}

// The other order: a URL that names nobody is what CDB_USER is for.
func TestCDBUserAppliesWhenTheURLNamesNoUser(t *testing.T) {
	srv := couchtest.New(t)
	s, out := envSession(t, map[string]string{"CDB_USER": "bob", "CDB_PASSWORD": "hunter2"})

	if _, err := connectWith(t, s, srv.URL()); err != nil {
		t.Fatalf("connect: %v (output: %s)", err, out.String())
	}
	name, password := loginCredentials(t, srv)
	if name != "bob" || password != "hunter2" {
		t.Errorf("login sent %q/%q, want the environment's user and password", name, password)
	}
}

// CDB_URL carries its user name the same way --url does: the environment's
// URL names the target, and CDB_USER only fills one in that names nobody.
func TestCDBURLUserNameWinsOverCDBUser(t *testing.T) {
	srv := couchtest.New(t)
	s, out := envSession(t, map[string]string{
		"CDB_URL":      withUser(srv.URL(), "alice"),
		"CDB_USER":     "bob",
		"CDB_PASSWORD": "hunter2",
	})

	if _, err := connectWith(t, s); err != nil {
		t.Fatalf("connect: %v (output: %s)", err, out.String())
	}
	name, password := loginCredentials(t, srv)
	if name != "alice" || password != "hunter2" {
		t.Errorf("login sent %q/%q, want CDB_URL's user with the environment's password", name, password)
	}
}
