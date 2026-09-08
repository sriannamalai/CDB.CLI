package command

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/config"
	"github.com/sriannamalai/CDB.CLI/internal/couch"
	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// profilesAdd runs "profiles add" with the shared flags parsed, the way both
// front-ends call it.
func profilesAdd(t *testing.T, s *session.Session, argv ...string) (Result, error) {
	t.Helper()
	fs := NewFlagSet(Profiles())
	if err := fs.Parse(argv); err != nil {
		t.Fatal(err)
	}
	return Profiles().Run(context.Background(), s, Invocation{Args: fs.Args(), Flags: fs})
}

// A bare URL saved a profile with no secret and no username, so "cdb connect"
// on it 401'd on every command afterwards. On a terminal, ask — the same two
// questions the guided connect asks — and prove the answers against the server
// before writing anything.
func TestProfilesAddPromptsForCredentialsOnABareURL(t *testing.T) {
	srv := couchtest.New(t)
	path := withDeps(t, config.Defaults(), nil)
	s, out := guidedSession("admin\ns3cret\n")

	res, err := profilesAdd(t, s, "add", "local", srv.URL())
	if err != nil {
		t.Fatalf("profiles add: %v (output: %s)", err, out.String())
	}
	if _, ok := res.(Message); !ok {
		t.Fatalf("result = %#v, want a Message", res)
	}
	name, password := loginCredentials(t, srv)
	if name != "admin" || password != "s3cret" {
		t.Errorf("the credentials were not verified against the server (name %q)", name)
	}
	back, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := back.Profile("local")
	if !ok {
		t.Fatalf("profiles = %v, want one named local", back.Profiles)
	}
	if p.Auth != "session" || p.Username != "admin" {
		t.Errorf("saved profile = %+v, want the answers from the prompt", p)
	}
	if got, err := CurrentDeps().Secrets.Get("local"); err != nil || got != "s3cret" {
		t.Errorf("keyring secret = %q, %v; want the typed password", got, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "s3cret") {
		t.Errorf("the password was written into the config file:\n%s", raw)
	}
	if strings.Contains(out.String(), "s3cret") {
		t.Errorf("the password was echoed to the terminal:\n%s", out.String())
	}
}

// A password the server rejects must leave nothing behind, and must be
// reported as the 401 it is, which is what renders as "Login failed for admin
// at <host>. Check the password with "profiles" or "connect"." and exits 3.
func TestProfilesAddSavesNothingWhenTheLoginIsRejected(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/_session", 401, `{"error":"unauthorized","reason":"Name or password is incorrect."}`)
	path := withDeps(t, config.Defaults(), nil)
	s, out := guidedSession("admin\nwrong-password\n")

	_, err := profilesAdd(t, s, "add", "local", srv.URL())
	if err == nil {
		t.Fatalf("profiles add saved a profile the server would not accept (output: %s)", out.String())
	}
	ce, ok := couch.AsError(err)
	if !ok || ce.Status != 401 {
		t.Fatalf("err = %v (%T), want a 401 from the server", err, err)
	}
	if strings.Contains(err.Error(), "wrong-password") {
		t.Errorf("the rejected password reached the error message: %v", err)
	}
	back, loadErr := config.Load(path)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(back.Profiles) != 0 {
		t.Errorf("profiles = %v, want none written when the login was refused", back.Profiles)
	}
	if names, _ := CurrentDeps().Secrets.List(); len(names) != 0 {
		t.Errorf("secrets = %v, want none written when the login was refused", names)
	}
	// Verifying a profile must not become a connection: the session was
	// pointed at nothing, and it stays that way.
	if s.Connected() {
		t.Error("profiles add attached a client to the session")
	}
}

// --anonymous is the operator saying there are no credentials. Saving auth
// "session" with no secret is the state this issue is about; "none" is the
// state that connects.
func TestProfilesAddAnonymousSavesAuthNone(t *testing.T) {
	srv := couchtest.New(t)
	path := withDeps(t, config.Defaults(), nil)
	s, out := guidedSession("")

	if _, err := profilesAdd(t, s, "add", "local", srv.URL(), "--anonymous"); err != nil {
		t.Fatalf("profiles add --anonymous: %v (output: %s)", err, out.String())
	}
	if strings.Contains(out.String(), "Username") {
		t.Errorf("--anonymous still asked for credentials:\n%s", out.String())
	}
	back, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := back.Profile("local"); !ok || p.Auth != "none" {
		t.Errorf("saved profile = %+v (ok=%v), want auth \"none\"", p, ok)
	}
}

// Pressing Enter at the password is the same answer --anonymous gives, and
// connect already reads it that way.
func TestProfilesAddEmptyPasswordSavesAuthNone(t *testing.T) {
	srv := couchtest.New(t)
	path := withDeps(t, config.Defaults(), nil)
	s, out := guidedSession("admin\n\n")

	if _, err := profilesAdd(t, s, "add", "local", srv.URL()); err != nil {
		t.Fatalf("profiles add: %v (output: %s)", err, out.String())
	}
	back, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := back.Profile("local"); !ok || p.Auth != "none" {
		t.Errorf("saved profile = %+v (ok=%v), want auth \"none\"", p, ok)
	}
	if names, _ := CurrentDeps().Secrets.List(); len(names) != 0 {
		t.Errorf("secrets = %v, want none for an anonymous profile", names)
	}
}

// Without a terminal there is nobody to ask, so the command does exactly what
// it did before: it stores the URL as typed and touches no server.
func TestProfilesAddWithoutATerminalIsUnchanged(t *testing.T) {
	srv := couchtest.New(t)
	path := withDeps(t, config.Defaults(), nil)
	var out bytes.Buffer
	s := session.New(strings.NewReader("admin\ns3cret\n"), &out, &out)

	if _, err := profilesAdd(t, s, "add", "local", srv.URL()); err != nil {
		t.Fatalf("profiles add: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("a non-interactive profiles add printed a prompt:\n%s", out.String())
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("a non-interactive profiles add reached the server: %d requests", len(srv.Requests()))
	}
	back, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := back.Profile("local")
	if !ok || p.Auth != "session" || p.Username != "" {
		t.Errorf("saved profile = %+v (ok=%v), want the 1.1.0 shape", p, ok)
	}
}

// A URL that carries its own userinfo is already answered for: the credentials
// come out of it, and nothing is asked.
func TestProfilesAddWithURLCredentialsDoesNotPrompt(t *testing.T) {
	path := withDeps(t, config.Defaults(), nil)
	s, out := guidedSession("")

	if _, err := profilesAdd(t, s, "add", "local", "http://admin:hunter2@db.example.com:5984/"); err != nil {
		t.Fatalf("profiles add: %v (output: %s)", err, out.String())
	}
	if out.Len() != 0 {
		t.Errorf("a URL with credentials still asked for them:\n%s", out.String())
	}
	back, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := back.Profile("local"); !ok || p.Username != "admin" || p.URL != "http://db.example.com:5984/" {
		t.Errorf("saved profile = %+v (ok=%v)", p, ok)
	}
	if got, _ := CurrentDeps().Secrets.Get("local"); got != "hunter2" {
		t.Errorf("keyring secret = %q, want the URL's password", got)
	}
}
