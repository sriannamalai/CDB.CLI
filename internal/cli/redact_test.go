package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/command"
	"github.com/sriannamalai/CDB.CLI/internal/config"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// TestExecuteKeepsURLCredentialsOffStderr drives the two front-end routes a
// password-bearing URL can take — the --url flag and CDB_URL — all the way to
// the rendered stderr text, because that is where a leak would actually land:
// in CI logs, shell scrollback and bug reports.
func TestExecuteKeepsURLCredentialsOffStderr(t *testing.T) {
	const secret = "hunter2"
	for _, tc := range []struct {
		name string
		args []string
		env  map[string]string
	}{
		{name: "url flag", args: []string{"--url", "ftp://admin:hunter2@localhost:5984/", "needs-conn"}},
		{name: "CDB_URL", args: []string{"needs-conn"}, env: map[string]string{"CDB_URL": "ftp://admin:hunter2@localhost:5984/"}},
		{name: "malformed CDB_URL", args: []string{"needs-conn"}, env: map[string]string{"CDB_URL": "http://admin:hunter2@local host:5984/"}},
		// No scheme at all: url.Parse reads "admin" as the scheme and reports
		// no userinfo, so nothing that trusts it can be relied on to redact.
		{name: "schemeless url flag", args: []string{"--url", "admin:hunter2@localhost:5984", "needs-conn"}},
		{name: "schemeless CDB_URL", args: []string{"needs-conn"}, env: map[string]string{"CDB_URL": "admin:hunter2@localhost:5984"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command.SetDeps(&command.Deps{
				ConfigPath: filepath.Join(t.TempDir(), "config.toml"),
				Secrets:    config.NewMemorySecrets(),
				LookupEnv: func(k string) (string, bool) {
					v, ok := tc.env[k]
					return v, ok
				},
			})
			t.Cleanup(func() { command.SetDeps(nil) })

			reg := testRegistry()
			reg.Register((&connectionSpy{}).command())
			var out, errOut bytes.Buffer
			s := session.New(strings.NewReader(""), &out, &errOut)
			code := Execute(context.Background(), reg, s, BuildInfo{}, tc.args)
			if code == ExitOK {
				t.Fatalf("exit code = %d, want a failure (stderr: %s)", code, errOut.String())
			}
			printed := out.String() + errOut.String()
			for _, leak := range []string{secret, "admin"} {
				if strings.Contains(printed, leak) {
					t.Errorf("output leaks %q: %q %q", leak, out.String(), errOut.String())
				}
			}
			if !strings.Contains(errOut.String(), "host:5984") {
				t.Errorf("stderr does not name the host: %q", errOut.String())
			}
		})
	}
}
