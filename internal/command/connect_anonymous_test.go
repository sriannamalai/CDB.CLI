package command

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/config"
	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

const anonymousNotice = "Connected anonymously; pass --anonymous to silence this or set CDB_USER/CDB_PASSWORD"

// connectWith runs "connect" with a parsed flag line.
func connectWith(t *testing.T, s *session.Session, args ...string) (Result, error) {
	t.Helper()
	fs := NewFlagSet(Connect())
	if err := fs.Parse(args); err != nil {
		t.Fatal(err)
	}
	return Connect().Run(context.Background(), s, Invocation{Args: fs.Args(), Flags: fs})
}

// "connect http://host" on a stock CouchDB with an admin succeeds anonymously
// — GET / and GET /_session both answer an anonymous client — and then every
// command 401s. On a terminal the credentials are asked for up front instead.
func TestConnectPromptsForCredentialsWhenTheURLHasNone(t *testing.T) {
	srv := couchtest.New(t)
	withDeps(t, config.Defaults(), nil)
	var out bytes.Buffer
	s := session.New(strings.NewReader(""), &out, &out)
	s.Prefs.Interactive = true
	s.SetStdin(strings.NewReader("admin\ns3cret\n"))

	if _, err := connectWith(t, s, srv.URL()); err != nil {
		t.Fatalf("connect: %v (output: %s)", err, out.String())
	}
	name, password := loginCredentials(t, srv)
	if name != "admin" || password != "s3cret" {
		t.Errorf("login sent %q/%q, want the answers typed at the prompt", name, password)
	}
	if strings.Contains(out.String(), "s3cret") {
		t.Errorf("the password was echoed:\n%s", out.String())
	}
	if strings.Contains(out.String(), anonymousNotice) {
		t.Errorf("the anonymous notice was printed for a connection that did log in:\n%s", out.String())
	}
}

// Pressing Enter at the password means "no credentials": send nothing rather
// than an empty password the server can only reject.
func TestConnectPromptAcceptsAnEmptyPasswordAsAnonymous(t *testing.T) {
	srv := couchtest.New(t)
	withDeps(t, config.Defaults(), nil)
	var out bytes.Buffer
	s := session.New(strings.NewReader(""), &out, &out)
	s.Prefs.Interactive = true
	s.SetStdin(strings.NewReader("admin\n\n"))

	if _, err := connectWith(t, s, srv.URL()); err != nil {
		t.Fatalf("connect: %v (output: %s)", err, out.String())
	}
	if !s.Connected() {
		t.Fatal("not connected")
	}
	if srv.Last("POST", "/_session") != nil {
		t.Error("connect attempted a login with an empty password")
	}
}

// --anonymous says the operator meant it: no prompt, no notice.
func TestConnectAnonymousFlagSkipsThePrompt(t *testing.T) {
	srv := couchtest.New(t)
	withDeps(t, config.Defaults(), nil)
	var out bytes.Buffer
	s := session.New(strings.NewReader(""), &out, &out)
	s.Prefs.Interactive = true
	s.SetStdin(strings.NewReader("admin\ns3cret\n"))

	if _, err := connectWith(t, s, srv.URL(), "--anonymous"); err != nil {
		t.Fatalf("connect --anonymous: %v (output: %s)", err, out.String())
	}
	if strings.Contains(out.String(), "Username") {
		t.Errorf("--anonymous still prompted:\n%s", out.String())
	}
	if strings.Contains(out.String(), anonymousNotice) {
		t.Errorf("--anonymous still printed the notice:\n%s", out.String())
	}
	if srv.Last("POST", "/_session") != nil {
		t.Error("--anonymous attempted a login")
	}
}

// A script cannot be asked anything, so it connects anonymously as before and
// is told once, on stderr, what happened and how to change it.
func TestConnectSaysSoWhenItConnectsAnonymouslyWithoutATerminal(t *testing.T) {
	srv := couchtest.New(t)
	withDeps(t, config.Defaults(), nil)
	var stdout, stderr bytes.Buffer
	s := session.New(strings.NewReader(""), &stdout, &stderr)

	if _, err := connectWith(t, s, srv.URL()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if !strings.Contains(stderr.String(), anonymousNotice) {
		t.Errorf("stderr = %q, want the anonymous notice", stderr.String())
	}
	if n := strings.Count(stderr.String(), anonymousNotice); n != 1 {
		t.Errorf("the notice appeared %d times, want once", n)
	}
	if strings.Contains(stdout.String(), anonymousNotice) {
		t.Errorf("the notice went to stdout, where it would join a pipeline:\n%s", stdout.String())
	}
}

func TestConnectIsSilentWhenAnonymousWasAskedFor(t *testing.T) {
	srv := couchtest.New(t)
	withDeps(t, config.Defaults(), nil)
	var stdout, stderr bytes.Buffer
	s := session.New(strings.NewReader(""), &stdout, &stderr)

	if _, err := connectWith(t, s, srv.URL(), "--anonymous"); err != nil {
		t.Fatalf("connect --anonymous: %v", err)
	}
	if strings.Contains(stderr.String(), anonymousNotice) {
		t.Errorf("--anonymous still printed the notice: %q", stderr.String())
	}
}

// Credentials in the environment are credentials: neither prompt nor notice.
func TestConnectDoesNotPromptWhenTheEnvironmentHasCredentials(t *testing.T) {
	srv := couchtest.New(t)
	withDeps(t, config.Defaults(), map[string]string{"CDB_USER": "admin", "CDB_PASSWORD": "hunter2"})
	var out bytes.Buffer
	s := session.New(strings.NewReader(""), &out, &out)
	s.Prefs.Interactive = true
	s.SetStdin(strings.NewReader("someone\nelse\n"))

	if _, err := connectWith(t, s, srv.URL()); err != nil {
		t.Fatalf("connect: %v (output: %s)", err, out.String())
	}
	if strings.Contains(out.String(), "Username") {
		t.Errorf("connect prompted although CDB_USER and CDB_PASSWORD were set:\n%s", out.String())
	}
	name, password := loginCredentials(t, srv)
	if name != "admin" || password != "hunter2" {
		t.Errorf("login sent %q/%q, want the environment's credentials", name, password)
	}
}

// A URL that carries its own userinfo needs nothing asked.
func TestConnectDoesNotPromptForAURLWithCredentials(t *testing.T) {
	srv := couchtest.New(t)
	withDeps(t, config.Defaults(), nil)
	var out bytes.Buffer
	s := session.New(strings.NewReader(""), &out, &out)
	s.Prefs.Interactive = true
	s.SetStdin(strings.NewReader("someone\nelse\n"))

	withCreds := strings.Replace(srv.URL(), "http://", "http://admin:hunter2@", 1)
	res, err := connectWith(t, s, withCreds)
	if err != nil {
		t.Fatalf("connect: %v (output: %s)", err, out.String())
	}
	if strings.Contains(out.String(), "Username") {
		t.Errorf("connect prompted for a URL that already carried credentials:\n%s", out.String())
	}
	// net/http authenticates such a URL with a Basic header, so reporting the
	// session as anonymous would be untrue — and is what the README's own
	// transcript shows not happening.
	msg, ok := res.(Message)
	if !ok {
		t.Fatalf("connect returned %#v, want a Message", res)
	}
	if !strings.HasSuffix(msg.Text, "as admin.") {
		t.Errorf("connect said %q, want it to name the user the URL carried", msg.Text)
	}
	if strings.Contains(msg.Text, "hunter2") {
		t.Errorf("the password was echoed: %s", msg.Text)
	}
}

// A saved profile is not a bare URL: connecting one must not start asking.
func TestConnectDoesNotPromptForASavedProfile(t *testing.T) {
	srv := couchtest.New(t)
	cfg := config.Defaults()
	cfg.SetProfile(config.Profile{Name: "local", URL: srv.URL(), Auth: "none"})
	withDeps(t, cfg, nil)
	var out bytes.Buffer
	s := session.New(strings.NewReader(""), &out, &out)
	s.Prefs.Interactive = true
	s.SetStdin(strings.NewReader("someone\nelse\n"))

	if _, err := connectWith(t, s, "local"); err != nil {
		t.Fatalf("connect local: %v (output: %s)", err, out.String())
	}
	if strings.Contains(out.String(), "Username") {
		t.Errorf("connect prompted for a saved profile:\n%s", out.String())
	}
	if strings.Contains(out.String(), anonymousNotice) {
		t.Errorf("the notice was printed for a saved profile:\n%s", out.String())
	}
}

// The credentials typed at the prompt are the ones "--save" persists, and the
// password still goes to the keyring rather than the config file.
func TestConnectPromptedCredentialsCanBeSaved(t *testing.T) {
	srv := couchtest.New(t)
	path := withDeps(t, config.Defaults(), nil)
	var out bytes.Buffer
	s := session.New(strings.NewReader(""), &out, &out)
	s.Prefs.Interactive = true
	s.SetStdin(strings.NewReader("admin\ns3cret\n"))

	if _, err := connectWith(t, s, srv.URL(), "--save", "--as", "local"); err != nil {
		t.Fatalf("connect --save: %v (output: %s)", err, out.String())
	}
	back, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := back.Profile("local")
	if !ok || p.Username != "admin" || p.Auth != "session" {
		t.Errorf("saved profile = %+v (ok=%v), want the answers from the prompt", p, ok)
	}
	if got, err := CurrentDeps().Secrets.Get("local"); err != nil || got != "s3cret" {
		t.Errorf("keyring secret = %q, %v; want the password from the prompt", got, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "s3cret") {
		t.Errorf("the password was written into the config file:\n%s", raw)
	}
}
