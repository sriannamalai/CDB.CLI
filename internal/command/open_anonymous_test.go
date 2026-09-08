package command

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/config"
	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// "connect" is not the common door into a connection: every subcommand and the
// shell's own startup reach a server through Open, so "cdb --url http://host
// ls /" hit the trap I3 named — connect anonymously, then fail every command
// with "Login failed for the configured user. Check the password" for a
// password nobody ever supplied. The credential resolution therefore belongs in
// openProfile, not in the connect command.

func TestOpenPromptsForCredentialsOnABareURL(t *testing.T) {
	srv := couchtest.New(t)
	withDeps(t, config.Defaults(), nil)
	var out bytes.Buffer
	s := session.New(strings.NewReader(""), &out, &out)
	s.Prefs.Interactive = true
	s.SetStdin(strings.NewReader("admin\ns3cret\n"))

	if err := Open(context.Background(), s, srv.URL()); err != nil {
		t.Fatalf("open: %v (output: %s)", err, out.String())
	}
	name, password := loginCredentials(t, srv)
	if name != "admin" || password != "s3cret" {
		t.Errorf("login sent %q/%q, want the answers typed at the prompt", name, password)
	}
	if strings.Contains(out.String(), "s3cret") {
		t.Errorf("the password was echoed:\n%s", out.String())
	}
}

func TestOpenSaysSoWhenItConnectsAnonymously(t *testing.T) {
	srv := couchtest.New(t)
	withDeps(t, config.Defaults(), nil)
	var stdout, stderr bytes.Buffer
	s := session.New(strings.NewReader(""), &stdout, &stderr)

	if err := Open(context.Background(), s, srv.URL()); err != nil {
		t.Fatalf("open: %v", err)
	}
	if n := strings.Count(stderr.String(), anonymousNotice); n != 1 {
		t.Errorf("stderr = %q, want the anonymous notice exactly once", stderr.String())
	}
	if strings.Contains(stdout.String(), anonymousNotice) {
		t.Errorf("the notice went to stdout, where it would join a pipeline:\n%s", stdout.String())
	}
}

func TestOpenIsSilentWhenAnonymousWasAskedFor(t *testing.T) {
	srv := couchtest.New(t)
	withDeps(t, config.Defaults(), nil)
	var stdout, stderr bytes.Buffer
	s := session.New(strings.NewReader(""), &stdout, &stderr)
	s.Prefs.Anonymous = true
	s.Prefs.Interactive = true
	s.SetStdin(strings.NewReader("admin\ns3cret\n"))

	if err := Open(context.Background(), s, srv.URL()); err != nil {
		t.Fatalf("open: %v", err)
	}
	if strings.Contains(stderr.String(), anonymousNotice) {
		t.Errorf("--anonymous still printed the notice: %q", stderr.String())
	}
	if strings.Contains(stdout.String(), "Username") {
		t.Errorf("--anonymous still prompted:\n%s", stdout.String())
	}
	if srv.Last("POST", "/_session") != nil {
		t.Error("--anonymous attempted a login")
	}
}

// A saved profile, a URL with its own userinfo, and credentials in the
// environment are all "credentials supplied": none of them may prompt or warn.
func TestOpenIsQuietWhenCredentialsExist(t *testing.T) {
	t.Run("saved profile", func(t *testing.T) {
		srv := couchtest.New(t)
		cfg := config.Defaults()
		cfg.SetProfile(config.Profile{Name: "local", URL: srv.URL(), Auth: "none"})
		withDeps(t, cfg, nil)
		var stdout, stderr bytes.Buffer
		s := session.New(strings.NewReader(""), &stdout, &stderr)
		s.Prefs.Interactive = true
		s.SetStdin(strings.NewReader("someone\nelse\n"))

		if err := Open(context.Background(), s, "local"); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(stderr.String(), anonymousNotice) || strings.Contains(stdout.String(), "Username") {
			t.Errorf("a saved profile prompted or warned: %q %q", stdout.String(), stderr.String())
		}
	})

	t.Run("URL with userinfo", func(t *testing.T) {
		srv := couchtest.New(t)
		withDeps(t, config.Defaults(), nil)
		var stdout, stderr bytes.Buffer
		s := session.New(strings.NewReader(""), &stdout, &stderr)
		s.Prefs.Interactive = true
		s.SetStdin(strings.NewReader("someone\nelse\n"))

		withCreds := strings.Replace(srv.URL(), "http://", "http://admin:hunter2@", 1)
		if err := Open(context.Background(), s, withCreds); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(stderr.String(), anonymousNotice) || strings.Contains(stdout.String(), "Username") {
			t.Errorf("a URL with userinfo prompted or warned: %q %q", stdout.String(), stderr.String())
		}
	})

	t.Run("environment", func(t *testing.T) {
		srv := couchtest.New(t)
		withDeps(t, config.Defaults(), map[string]string{"CDB_USER": "admin", "CDB_PASSWORD": "hunter2"})
		var stdout, stderr bytes.Buffer
		s := session.New(strings.NewReader(""), &stdout, &stderr)

		if err := Open(context.Background(), s, srv.URL()); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(stderr.String(), anonymousNotice) {
			t.Errorf("the environment's credentials still drew the notice: %q", stderr.String())
		}
	})
}
