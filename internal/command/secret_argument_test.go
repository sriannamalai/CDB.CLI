package command

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/config"
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
