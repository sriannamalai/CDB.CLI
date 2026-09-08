package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/command"
	"github.com/sriannamalai/CDB.CLI/internal/config"
	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

const anonymousNotice = "Connected anonymously; pass --anonymous to silence this or set CDB_USER/CDB_PASSWORD"

func withTempDeps(t *testing.T, env map[string]string) {
	t.Helper()
	command.SetDeps(&command.Deps{
		ConfigPath: filepath.Join(t.TempDir(), "config.toml"),
		Secrets:    config.NewMemorySecrets(),
		LookupEnv: func(k string) (string, bool) {
			v, ok := env[k]
			return v, ok
		},
	})
	t.Cleanup(func() { command.SetDeps(nil) })
}

// The auto-connect that every one-shot subcommand goes through — not just
// "cdb connect" — has to say when it connected with no credentials.
func TestOneShotAutoConnectSaysWhenItIsAnonymous(t *testing.T) {
	srv := couchtest.New(t)
	withTempDeps(t, nil)
	reg := testRegistry()
	reg.Register((&connectionSpy{}).command())

	var out, errOut bytes.Buffer
	s := session.New(strings.NewReader(""), &out, &errOut)
	code := Execute(context.Background(), reg, s, BuildInfo{}, []string{"--url", srv.URL(), "needs-conn"})
	if code != ExitOK {
		t.Fatalf("exit code = %d (stderr: %s)", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), anonymousNotice) {
		t.Errorf("stderr = %q, want the anonymous notice", errOut.String())
	}
	if strings.Contains(out.String(), anonymousNotice) {
		t.Errorf("the notice reached stdout, where it would join a pipeline: %q", out.String())
	}
}

// --anonymous is a global flag, so it silences the notice for every
// subcommand, not only for "connect".
func TestOneShotAnonymousFlagSilencesTheNotice(t *testing.T) {
	srv := couchtest.New(t)
	withTempDeps(t, nil)
	reg := testRegistry()
	reg.Register((&connectionSpy{}).command())

	var out, errOut bytes.Buffer
	s := session.New(strings.NewReader(""), &out, &errOut)
	code := Execute(context.Background(), reg, s, BuildInfo{}, []string{"--url", srv.URL(), "--anonymous", "needs-conn"})
	if code != ExitOK {
		t.Fatalf("exit code = %d (stderr: %s)", code, errOut.String())
	}
	if strings.Contains(errOut.String(), anonymousNotice) {
		t.Errorf("--anonymous did not silence the notice: %q", errOut.String())
	}
}

// Credentials in the environment are credentials wherever they come from.
func TestOneShotAutoConnectIsQuietWithEnvironmentCredentials(t *testing.T) {
	srv := couchtest.New(t)
	withTempDeps(t, map[string]string{"CDB_USER": "admin", "CDB_PASSWORD": "hunter2"})
	reg := testRegistry()
	reg.Register((&connectionSpy{}).command())

	var out, errOut bytes.Buffer
	s := session.New(strings.NewReader(""), &out, &errOut)
	code := Execute(context.Background(), reg, s, BuildInfo{}, []string{"--url", srv.URL(), "needs-conn"})
	if code != ExitOK {
		t.Fatalf("exit code = %d (stderr: %s)", code, errOut.String())
	}
	if strings.Contains(errOut.String(), anonymousNotice) {
		t.Errorf("stderr = %q, want no notice when CDB_USER/CDB_PASSWORD are set", errOut.String())
	}
}
