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

// An operator who names a server or a profile on the command line must reach
// that server. env.Apply ran after the flag had been turned into a profile, so
// CDB_URL — which a shell may export for convenience — silently redirected a
// command aimed somewhere else. Reading the wrong server is bad enough; a put,
// rmdir or restore aimed at one server and delivered to another is worse.
//
// The order is: explicit flag, then environment, then config file.
func TestExplicitTargetBeatsTheEnvironment(t *testing.T) {
	t.Run("--url beats CDB_URL", func(t *testing.T) {
		wanted, other := couchtest.New(t), couchtest.New(t)
		withDeps(t, config.Defaults(), map[string]string{"CDB_URL": other.URL()})
		s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})

		if err := Open(context.Background(), s, wanted.URL()); err != nil {
			t.Fatalf("open: %v", err)
		}
		if got := s.Client.URL(); got != wanted.URL() {
			t.Errorf("connected to %s, want the URL named on the command line (%s)", got, wanted.URL())
		}
		if other.Last("GET", "/") != nil {
			t.Error("CDB_URL's server was contacted although --url named another")
		}
	})

	t.Run("--profile beats CDB_URL", func(t *testing.T) {
		wanted, other := couchtest.New(t), couchtest.New(t)
		cfg := config.Defaults()
		cfg.SetProfile(config.Profile{Name: "local", URL: wanted.URL(), Auth: "none"})
		withDeps(t, cfg, map[string]string{"CDB_URL": other.URL()})
		s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})

		if err := Open(context.Background(), s, "local"); err != nil {
			t.Fatalf("open: %v", err)
		}
		if got := s.Client.URL(); got != wanted.URL() {
			t.Errorf("connected to %s, want the profile's server (%s)", got, wanted.URL())
		}
		if other.Last("GET", "/") != nil {
			t.Error("CDB_URL's server was contacted although --profile named another")
		}
	})

	// With nothing named, the environment is still what wins over the config
	// file — that layer is unchanged.
	t.Run("CDB_URL beats the config file", func(t *testing.T) {
		wanted, other := couchtest.New(t), couchtest.New(t)
		cfg := config.Defaults()
		cfg.SetProfile(config.Profile{Name: "local", URL: other.URL(), Auth: "none"})
		cfg.Default = "local"
		withDeps(t, cfg, map[string]string{"CDB_URL": wanted.URL()})
		s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})

		if err := Open(context.Background(), s, ""); err != nil {
			t.Fatalf("open: %v", err)
		}
		if got := s.Client.URL(); got != wanted.URL() {
			t.Errorf("connected to %s, want CDB_URL's server (%s)", got, wanted.URL())
		}
	})

	// CDB_USER and CDB_PASSWORD are credentials, not a target: naming a
	// profile must not switch them off.
	t.Run("credentials from the environment still apply to a named profile", func(t *testing.T) {
		srv := couchtest.New(t)
		cfg := config.Defaults()
		cfg.SetProfile(config.Profile{Name: "local", URL: srv.URL(), Auth: "session", Username: "fromfile"})
		withDeps(t, cfg, map[string]string{"CDB_USER": "fromenv", "CDB_PASSWORD": "hunter2"})
		s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})

		if err := Open(context.Background(), s, "local"); err != nil {
			t.Fatalf("open: %v", err)
		}
		name, password := loginCredentials(t, srv)
		if name != "fromenv" || password != "hunter2" {
			t.Errorf("login sent %q/%q, want the environment's credentials", name, password)
		}
	})
}
