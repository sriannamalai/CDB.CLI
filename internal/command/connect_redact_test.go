package command

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/config"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// TestOpenKeepsURLCredentialsOutOfItsErrors pins the global constraint that a
// secret never reaches an error message. A URL with an unexpected scheme falls
// out of openProfile's http/https branch, so whatever the operator typed —
// password and all — reaches the message it prints.
func TestOpenKeepsURLCredentialsOutOfItsErrors(t *testing.T) {
	const secret = "hunter2"
	for _, tc := range []struct {
		name, arg string
		env       map[string]string
	}{
		{name: "explicit argument", arg: "ftp://admin:hunter2@localhost:5984/"},
		{name: "profile from the environment", env: map[string]string{"CDB_PROFILE": "ftp://admin:hunter2@localhost:5984/"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withDeps(t, config.Defaults(), tc.env)
			var out, errOut bytes.Buffer
			s := session.New(strings.NewReader(""), &out, &errOut)
			err := Open(context.Background(), s, tc.arg)
			if err == nil {
				t.Fatal("Open succeeded, want an error")
			}
			if strings.Contains(err.Error(), secret) {
				t.Errorf("error leaks the password: %q", err.Error())
			}
			if strings.Contains(out.String()+errOut.String(), secret) {
				t.Errorf("output leaks the password: %q %q", out.String(), errOut.String())
			}
			if !strings.Contains(err.Error(), "localhost:5984") {
				t.Errorf("error does not name the host: %q", err.Error())
			}
		})
	}
}
