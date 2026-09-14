package command

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/config"
	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// Issue #36: "connect" is the one command that opens its own connection, so
// the front-end's auto-connect — the only reader --url and --profile had —
// never ran for it, and "cdb connect --url http://elsewhere" dialled the
// default profile instead. The flags are a target for the run, so they are
// resolved where every other target is resolved, and "connect" inherits that
// rather than repeating it.
func TestConnectFlagTargetBeatsTheDefaultProfile(t *testing.T) {
	// withPrefsTarget puts the two flags on the session the way the cobra
	// front-end does, before the command runs.
	target := func(s *session.Session, profile, url string) {
		s.Prefs.Profile, s.Prefs.URL = profile, url
	}

	t.Run("--url beats the default profile", func(t *testing.T) {
		wanted, other := couchtest.New(t), couchtest.New(t)
		cfg := config.Defaults()
		cfg.SetProfile(config.Profile{Name: "local", URL: other.URL(), Auth: "none"})
		cfg.Default = "local"
		withDeps(t, cfg, nil)
		s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
		target(s, "", wanted.URL())

		if _, err := invoke(t, Connect(), s, "--anonymous"); err != nil {
			t.Fatalf("connect: %v", err)
		}
		if got := s.Client.URL(); got != wanted.URL() {
			t.Errorf("connected to %s, want --url's server (%s)", got, wanted.URL())
		}
		if other.Last("GET", "/") != nil {
			t.Error("the default profile's server was contacted although --url named another")
		}
	})

	t.Run("--profile beats the default profile", func(t *testing.T) {
		wanted, other := couchtest.New(t), couchtest.New(t)
		cfg := config.Defaults()
		cfg.SetProfile(config.Profile{Name: "local", URL: other.URL(), Auth: "none"})
		cfg.SetProfile(config.Profile{Name: "far", URL: wanted.URL(), Auth: "none"})
		cfg.Default = "local"
		withDeps(t, cfg, nil)
		s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
		target(s, "far", "")

		if _, err := invoke(t, Connect(), s); err != nil {
			t.Fatalf("connect: %v", err)
		}
		if got := s.Client.URL(); got != wanted.URL() {
			t.Errorf("connected to %s, want the --profile server (%s)", got, wanted.URL())
		}
		if other.Last("GET", "/") != nil {
			t.Error("the default profile's server was contacted although --profile named another")
		}
	})

	// The reference page promises --url overrides CDB_URL; nothing about
	// "connect" changes that.
	t.Run("--url beats CDB_URL", func(t *testing.T) {
		wanted, other := couchtest.New(t), couchtest.New(t)
		withDeps(t, config.Defaults(), map[string]string{"CDB_URL": other.URL()})
		s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
		target(s, "", wanted.URL())

		if _, err := invoke(t, Connect(), s, "--anonymous"); err != nil {
			t.Fatalf("connect: %v", err)
		}
		if got := s.Client.URL(); got != wanted.URL() {
			t.Errorf("connected to %s, want --url's server (%s)", got, wanted.URL())
		}
		if other.Last("GET", "/") != nil {
			t.Error("CDB_URL's server was contacted although --url named another")
		}
	})

	// A name the flag gives is resolved like any other: an unknown one is a
	// usage error, not a silent fall back to the default.
	t.Run("--profile names an unknown profile", func(t *testing.T) {
		other := couchtest.New(t)
		cfg := config.Defaults()
		cfg.SetProfile(config.Profile{Name: "local", URL: other.URL(), Auth: "none"})
		cfg.Default = "local"
		withDeps(t, cfg, nil)
		s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
		target(s, "nosuch", "")

		_, err := invoke(t, Connect(), s)
		if !isUsage(err) {
			t.Fatalf("err = %v, want a usage error", err)
		}
		if other.Last("GET", "/") != nil {
			t.Error("the default profile's server was contacted for an unknown --profile")
		}
	})
}

// docs/reference/connect.md says what --url overrides — the profile and
// CDB_URL — and says nothing about the argument "connect" takes. Rather than
// invent a precedence between two targets the operator named on the same line,
// say they disagree.
func TestConnectFlagAndArgumentDisagreeing(t *testing.T) {
	cases := []struct {
		name         string
		profile, url string
		arg          string
	}{
		{name: "--url against an argument", url: "http://flag.example.com/", arg: "http://arg.example.com/"},
		{name: "--profile against an argument", profile: "far", arg: "http://arg.example.com/"},
		{name: "--url against a profile argument", url: "http://flag.example.com/", arg: "far"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := couchtest.New(t)
			cfg := config.Defaults()
			cfg.SetProfile(config.Profile{Name: "far", URL: srv.URL(), Auth: "none"})
			withDeps(t, cfg, nil)
			s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
			s.Prefs.Profile, s.Prefs.URL = tc.profile, tc.url

			_, err := invoke(t, Connect(), s, tc.arg, "--anonymous")
			if !isUsage(err) {
				t.Fatalf("err = %v, want a usage error", err)
			}
			if srv.Last("GET", "/") != nil {
				t.Error("a server was contacted although the two targets disagreed")
			}
		})
	}

	// Naming the same server twice is not a disagreement.
	t.Run("--url repeating the argument is accepted", func(t *testing.T) {
		srv := couchtest.New(t)
		withDeps(t, config.Defaults(), nil)
		s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
		s.Prefs.URL = srv.URL()

		if _, err := invoke(t, Connect(), s, srv.URL(), "--anonymous"); err != nil {
			t.Fatalf("connect: %v", err)
		}
	})
}

// A named target still silences CDB_URL when the name arrived as a flag: that
// is the whole point of the 1.1 ruling, and the flag is the most explicit
// target there is.
func TestFlagTargetSilencesCDBURLForEveryCommand(t *testing.T) {
	wanted, other := couchtest.New(t), couchtest.New(t)
	withDeps(t, config.Defaults(), map[string]string{"CDB_URL": other.URL()})
	s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	s.Prefs.URL = wanted.URL()

	if err := Open(context.Background(), s, ""); err != nil {
		t.Fatalf("open: %v", err)
	}
	if got := s.Client.URL(); got != wanted.URL() {
		t.Errorf("connected to %s, want --url's server (%s)", got, wanted.URL())
	}
}

// isUsage reports whether err is the exit-2 kind.
func isUsage(err error) bool {
	var ue *UsageError
	return errors.As(err, &ue)
}
