package command

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/config"
	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// A credential typed as a positional argument must not survive anywhere the
// operator, a later reader of config.toml, or a log can see it. Both write
// paths that take a URL argument are driven here rather than only the one that
// was found leaking: this is a class of defect, not a single site.

func TestProfilesAddKeepsTheURLPasswordOutOfTheConfigAndTheTerminal(t *testing.T) {
	path := withDeps(t, config.Defaults(), nil)
	var out bytes.Buffer
	s := session.New(strings.NewReader(""), &out, &out)

	res, err := Profiles().Run(context.Background(), s, Invocation{
		Args: []string{"add", "local", "http://admin:hunter2@db.example.com:5984/"},
	})
	if err != nil {
		t.Fatalf("profiles add: %v", err)
	}
	msg, ok := res.(Message)
	if !ok {
		t.Fatalf("profiles add returned %#v, want a Message", res)
	}
	if strings.Contains(msg.Text, "hunter2") {
		t.Errorf("the password was echoed to stdout: %s", msg.Text)
	}
	if strings.Contains(out.String(), "hunter2") {
		t.Errorf("the password reached the session output: %s", out.String())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "hunter2") {
		t.Errorf("the password was written into the config file:\n%s", raw)
	}

	back, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := back.Profile("local")
	if !ok {
		t.Fatalf("profiles = %v, want one named local", back.Profiles)
	}
	if p.URL != "http://db.example.com:5984/" {
		t.Errorf("saved URL = %q, want the URL with its credentials removed", p.URL)
	}
	if p.Username != "admin" {
		t.Errorf("saved username = %q, want admin", p.Username)
	}
	if got, err := CurrentDeps().Secrets.Get("local"); err != nil || got != "hunter2" {
		t.Errorf("keyring secret = %q, %v; want the URL password moved into the keyring", got, err)
	}
}

// A URL with no password stores cleanly and needs no keyring at all: a broken
// keyring must not stop "profiles add http://host" from working.
func TestProfilesAddWithNoCredentialsNeedsNoKeyring(t *testing.T) {
	path := withBrokenKeyring(t, config.Defaults(), nil)
	var out bytes.Buffer
	s := session.New(strings.NewReader(""), &out, &out)

	if _, err := Profiles().Run(context.Background(), s, Invocation{
		Args: []string{"add", "local", "http://db.example.com:5984/"},
	}); err != nil {
		t.Fatalf("profiles add without credentials: %v", err)
	}
	back, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := back.Profile("local"); !ok || p.URL != "http://db.example.com:5984/" {
		t.Errorf("saved profile = %+v (ok=%v), want the URL as typed", p, ok)
	}
}

// setFailingSecrets is a keyring that opens and reads but will not accept a
// write: a full disk, a locked collection, a file backend whose passphrase
// prompt has nowhere to go.
type setFailingSecrets struct {
	config.Secrets
	err error
}

func (f setFailingSecrets) Set(string, string) error { return f.err }

// A password that could not be stored is a profile that will fail on the next
// run. Reporting success and telling the operator to "cdb connect p2" — which
// storeProfile's own comment says must not happen — sends them to debug a
// profile that was never complete.
func TestProfilesAddFailsWhenTheSecretCannotBeStored(t *testing.T) {
	path := withDeps(t, config.Defaults(), nil)
	d := CurrentDeps()
	d.Secrets = setFailingSecrets{Secrets: d.Secrets, err: errors.New("operation not supported by device")}

	var out bytes.Buffer
	s := session.New(strings.NewReader(""), &out, &out)
	_, err := Profiles().Run(context.Background(), s, Invocation{
		Args: []string{"add", "p2", "http://admin:topsecret9@localhost:15984/"},
	})
	if err == nil {
		t.Fatal("profiles add reported success although the password was lost")
	}
	for _, want := range []string{"keyring", "p2", "operation not supported by device"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q is missing %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "topsecret9") {
		t.Errorf("the password reached the error message: %v", err)
	}
	if strings.Contains(err.Error(), "..") {
		t.Errorf("error %q has a doubled period", err)
	}

	// And nothing may be left behind: a half-added profile is the thing the
	// operator would trip over next.
	back, loadErr := config.Load(path)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if _, ok := back.Profile("p2"); ok {
		t.Errorf("profiles = %v, want the profile not written when its secret was lost", back.Profiles)
	}
}

// The same rule on the other write path.
func TestConnectSaveFailsWhenTheSecretCannotBeStored(t *testing.T) {
	srv := couchtest.New(t)
	path := withDeps(t, config.Defaults(), map[string]string{"CDB_USER": "admin", "CDB_PASSWORD": "hunter2"})
	d := CurrentDeps()
	d.Secrets = setFailingSecrets{Secrets: d.Secrets, err: errors.New("keyring is locked")}

	var out bytes.Buffer
	s := session.New(strings.NewReader(""), &out, &out)
	fs := NewFlagSet(Connect())
	if err := fs.Parse([]string{srv.URL(), "--save", "--as", "p3"}); err != nil {
		t.Fatal(err)
	}
	_, err := Connect().Run(context.Background(), s, Invocation{Args: fs.Args(), Flags: fs})
	if err == nil {
		t.Fatal("connect --save reported success although the password was lost")
	}
	back, loadErr := config.Load(path)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if _, ok := back.Profile("p3"); ok {
		t.Errorf("profiles = %v, want the profile not written when its secret was lost", back.Profiles)
	}
}

// The file keyring cannot prompt on a pipe, so without an environment
// override a script could never store a password at all — which matters now
// that failing to store one stops the save.
func TestPromptPassphraseReadsTheEnvironment(t *testing.T) {
	t.Setenv("CDB_KEYRING_PASSPHRASE", "from-the-environment")
	got, err := promptPassphrase("Enter passphrase")
	if err != nil {
		t.Fatal(err)
	}
	if got != "from-the-environment" {
		t.Errorf("promptPassphrase = %q, want the environment's value", got)
	}
}

// A profiles add that cannot reach the keyring must fail before writing the
// profile, rather than saving one whose password was silently dropped.
func TestProfilesAddFailsWhenTheKeyringWillNotOpen(t *testing.T) {
	path := withBrokenKeyring(t, config.Defaults(), nil)
	var out bytes.Buffer
	s := session.New(strings.NewReader(""), &out, &out)

	_, err := Profiles().Run(context.Background(), s, Invocation{
		Args: []string{"add", "local", "http://admin:hunter2@db.example.com:5984/"},
	})
	if err == nil {
		t.Fatal("profiles add saved a profile whose password had nowhere to go")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("the password reached the error message: %v", err)
	}
	raw, readErr := os.ReadFile(path)
	if readErr == nil && strings.Contains(string(raw), "hunter2") {
		t.Errorf("the password was written into the config file:\n%s", raw)
	}
}
