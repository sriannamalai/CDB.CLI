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

// newSession returns a disconnected session with buffered streams.
func newSession(t *testing.T) *session.Session {
	t.Helper()
	s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	t.Cleanup(func() { _ = s.Detach() })
	return s
}

func TestReplicationURLComesFromTheProfile(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/_replicator", 201, `{"ok":true,"id":"job1","rev":"1-a"}`)
	cfg := config.Defaults()
	cfg.SetProfile(config.Profile{
		Name:           "local",
		URL:            srv.URL(),
		Auth:           "none",
		ReplicationURL: "http://couchdb:5984",
	})
	withDeps(t, cfg, nil)

	s := newSession(t)
	if err := Open(context.Background(), s, "local"); err != nil {
		t.Fatal(err)
	}
	defer s.Detach()

	if _, err := invoke(t, Replicate(), s, "/src", "/dst"); err != nil {
		t.Fatal(err)
	}
	body := string(srv.Last("POST", "/_replicator").Body)
	if !strings.Contains(body, `"url":"http://couchdb:5984/src"`) {
		t.Errorf("_replicator body did not use the replication URL: %s", body)
	}
	if strings.Contains(body, srv.URL()) {
		t.Errorf("_replicator body still names the client URL: %s", body)
	}
}

func TestReplicationURLEnvironmentBeatsTheProfile(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/_replicator", 201, `{"ok":true,"id":"job1","rev":"1-a"}`)
	cfg := config.Defaults()
	cfg.SetProfile(config.Profile{Name: "local", URL: srv.URL(), Auth: "none", ReplicationURL: "http://from-file:5984"})
	withDeps(t, cfg, map[string]string{"CDB_REPLICATION_URL": "http://from-env:5984"})

	s := newSession(t)
	if err := Open(context.Background(), s, "local"); err != nil {
		t.Fatal(err)
	}
	defer s.Detach()
	if got := s.Client.ReplicationURL(); got != "http://from-env:5984" {
		t.Errorf("ReplicationURL() = %q, want the environment's value", got)
	}
}

func TestReplicationURLFlagBeatsTheEnvironment(t *testing.T) {
	srv := couchtest.New(t)
	cfg := config.Defaults()
	cfg.SetProfile(config.Profile{Name: "local", URL: srv.URL(), Auth: "none", ReplicationURL: "http://from-file:5984"})
	withDeps(t, cfg, map[string]string{"CDB_REPLICATION_URL": "http://from-env:5984"})

	s := newSession(t)
	s.Prefs.ReplicationURL = "http://from-flag:5984"
	if err := Open(context.Background(), s, "local"); err != nil {
		t.Fatal(err)
	}
	defer s.Detach()
	if got := s.Client.ReplicationURL(); got != "http://from-flag:5984" {
		t.Errorf("ReplicationURL() = %q, want the flag's value", got)
	}
}

func TestInfoShowsTheReplicationURLOnlyWhenItDiffers(t *testing.T) {
	srv := couchtest.New(t)
	cfg := config.Defaults()
	cfg.SetProfile(config.Profile{Name: "local", URL: srv.URL(), Auth: "none"})
	withDeps(t, cfg, nil)

	s := newSession(t)
	if err := Open(context.Background(), s, "local"); err != nil {
		t.Fatal(err)
	}
	defer s.Detach()

	res, err := invoke(t, Info(), s, "/")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range res.(Rows).Items {
		if row.Cells[0] == "replication url" {
			t.Fatal("info / showed a replication url row when none is configured")
		}
	}

	s.Detach()
	withDeps(t, cfg, map[string]string{"CDB_REPLICATION_URL": "http://couchdb:5984"})
	if err := Open(context.Background(), s, "local"); err != nil {
		t.Fatal(err)
	}
	res, err = invoke(t, Info(), s, "/")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range res.(Rows).Items {
		if row.Cells[0] == "replication url" {
			found = true
			if row.Cells[1] != "http://couchdb:5984" {
				t.Errorf("replication url row = %v", row.Cells)
			}
		}
	}
	if !found {
		t.Error("info / did not show the configured replication url")
	}
}

func TestProfileRoundTripsTheReplicationURL(t *testing.T) {
	path := withDeps(t, config.Defaults(), nil)
	stored, err := storeProfile("local", config.Profile{
		Name:           "local",
		URL:            "http://localhost:15984",
		Auth:           "none",
		ReplicationURL: "http://couchdb:5984",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if stored.ReplicationURL != "http://couchdb:5984" {
		t.Errorf("stored profile = %+v", stored)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := cfg.Profile("local")
	if p.ReplicationURL != "http://couchdb:5984" {
		t.Errorf("reloaded profile = %+v", p)
	}
}

// The guided walk-through never asks for a replication URL, so the flag, the
// environment and the profile defaults have to be layered onto the prompted
// answers before the connection is dialled and saved. Otherwise a first run —
// which is exactly when the walk-through appears — cannot persist one.
func TestGuidedConnectSavesTheReplicationURLFromTheFlag(t *testing.T) {
	srv := couchtest.New(t)
	path := withDeps(t, config.Defaults(), nil)
	s := newSession(t)
	s.Prefs.Interactive = true
	s.Prefs.ReplicationURL = "http://couchdb:5984"
	// Server URL, authentication kind, profile name, then "y" to save.
	s.SetStdin(strings.NewReader(srv.URL() + "\nnone\nlocal\ny\n"))

	if _, err := Connect().Run(context.Background(), s, Invocation{}); err != nil {
		t.Fatal(err)
	}
	if got := s.Client.ReplicationURL(); got != "http://couchdb:5984" {
		t.Errorf("ReplicationURL() = %q, want the flag's value", got)
	}
	back, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := back.Profile("local")
	if !ok || p.ReplicationURL != "http://couchdb:5984" {
		t.Errorf("saved profile = %+v (ok=%v), want replication_url persisted", p, ok)
	}
}

// The environment reaches the guided answers too, and the flag still wins.
func TestGuidedConnectTakesTheReplicationURLFromTheEnvironment(t *testing.T) {
	srv := couchtest.New(t)
	path := withDeps(t, config.Defaults(), map[string]string{"CDB_REPLICATION_URL": "http://from-env:5984"})
	s := newSession(t)
	s.Prefs.Interactive = true
	s.SetStdin(strings.NewReader(srv.URL() + "\nnone\nlocal\ny\n"))

	if _, err := Connect().Run(context.Background(), s, Invocation{}); err != nil {
		t.Fatal(err)
	}
	if got := s.Client.ReplicationURL(); got != "http://from-env:5984" {
		t.Errorf("ReplicationURL() = %q, want the environment's value", got)
	}
	back, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := back.Profile("local"); p.ReplicationURL != "http://from-env:5984" {
		t.Errorf("saved profile = %+v", p)
	}
}

// Only the replication URL is layered onto the walk-through's answers. An
// answer typed at a prompt is not the config file the CDB_* overrides exist to
// beat: overriding the URL, user name or auth kind a second after the operator
// typed them would be a silent contradiction of what they were just shown.
func TestGuidedConnectDoesNotOverrideTheTypedAnswersFromTheEnvironment(t *testing.T) {
	srv := couchtest.New(t)
	other := couchtest.New(t)
	path := withDeps(t, config.Defaults(), map[string]string{
		"CDB_URL":             other.URL(),
		"CDB_USER":            "someone-else",
		"CDB_PASSWORD":        "hunter2",
		"CDB_REPLICATION_URL": "http://couchdb:5984",
	})
	s := newSession(t)
	s.Prefs.Interactive = true
	s.SetStdin(strings.NewReader(srv.URL() + "\nnone\nlocal\ny\n"))

	if _, err := Connect().Run(context.Background(), s, Invocation{}); err != nil {
		t.Fatal(err)
	}
	if got := s.Client.URL(); got != srv.URL() {
		t.Errorf("connected to %q, want the URL typed at the prompt %q", got, srv.URL())
	}
	if len(other.Requests()) != 0 {
		t.Errorf("CDB_URL was dialled instead of the typed answer: %d requests", len(other.Requests()))
	}
	// The replication URL is the one key the walk-through never asks for, so it
	// is the one key the environment may still supply.
	if got := s.Client.ReplicationURL(); got != "http://couchdb:5984" {
		t.Errorf("ReplicationURL() = %q, want the environment's value", got)
	}
	back, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := back.Profile("local")
	if !ok {
		t.Fatal("the guided walk-through saved no profile")
	}
	if p.URL != srv.URL() {
		t.Errorf("saved url = %q, want the typed answer", p.URL)
	}
	if p.Auth != "none" {
		t.Errorf("saved auth = %q, want the typed answer %q", p.Auth, "none")
	}
	if p.Username != "" {
		t.Errorf("saved username = %q, want the typed answers to stand alone", p.Username)
	}
	if p.ReplicationURL != "http://couchdb:5984" {
		t.Errorf("saved replication_url = %q", p.ReplicationURL)
	}
}
