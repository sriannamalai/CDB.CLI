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

// failingSecrets is a keyring that opens but will not hand a secret over: a
// denied macOS Keychain prompt, a locked Secret Service, a wrong file-backend
// passphrase.
type failingSecrets struct {
	config.Secrets
	err error
}

func (f failingSecrets) Get(string) (string, error) { return "", f.err }

func withFailingSecrets(t *testing.T, cfg *config.Config, err error) {
	t.Helper()
	withDeps(t, cfg, nil)
	d := CurrentDeps()
	d.Secrets = failingSecrets{Secrets: d.Secrets, err: err}
}

// A keyring that refuses must be reported as a keyring failure. Reported as a
// 401 instead, it tells the operator their password is wrong — which sends
// them to change a password that was never read.
func TestKeyringReadFailureIsNotReportedAsALoginFailure(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/_session", 401, `{"error":"unauthorized","reason":"Name or password is incorrect."}`)
	cfg := config.Defaults()
	cfg.SetProfile(config.Profile{Name: "local", URL: srv.URL(), Auth: "session", Username: "admin"})
	withFailingSecrets(t, cfg, errors.New("keychain access denied"))

	var out bytes.Buffer
	s := session.New(strings.NewReader(""), &out, &out)
	_, err := Connect().Run(context.Background(), s, Invocation{Args: []string{"local"}})
	if err == nil {
		t.Fatal("connect succeeded although the keyring refused to hand over the password")
	}
	msg := err.Error()
	if strings.Contains(msg, "Login failed") || strings.Contains(msg, "Name or password is incorrect") {
		t.Errorf("a keyring failure was reported as a wrong password: %q", msg)
	}
	for _, want := range []string{"keyring", "local", "keychain access denied", "CDB_PASSWORD"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q is missing %q", msg, want)
		}
	}
	var ce *ConnectionError
	if !errors.As(err, &ce) {
		t.Errorf("error is %T, want a *ConnectionError so the front-ends exit 3", err)
	}
}

// "This profile has no saved secret" is not a failure: a profile may
// legitimately have none, and CDB_PASSWORD or an anonymous server still works.
func TestMissingSecretIsNotAKeyringFailure(t *testing.T) {
	srv := couchtest.New(t)
	cfg := config.Defaults()
	cfg.SetProfile(config.Profile{Name: "local", URL: srv.URL(), Auth: "session", Username: "admin"})
	withFailingSecrets(t, cfg, config.ErrSecretNotFound)

	var out bytes.Buffer
	s := session.New(strings.NewReader(""), &out, &out)
	if _, err := Connect().Run(context.Background(), s, Invocation{Args: []string{"local"}}); err != nil {
		t.Fatalf("connect with no stored secret: %v", err)
	}
}
