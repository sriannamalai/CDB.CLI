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

// Issue #36, end to end through the flag parser: "connect" opens its own
// connection, so it never took the auto-connect branch that was the only
// reader --url and --profile had, and the flag was parsed and dropped.
func TestOneShotConnectURLFlagBeatsTheDefaultProfile(t *testing.T) {
	wanted, other := couchtest.New(t), couchtest.New(t)
	cfg := config.Defaults()
	cfg.SetProfile(config.Profile{Name: "local", URL: other.URL(), Auth: "none"})
	cfg.Default = "local"
	withConfigDeps(t, cfg)

	var out, errOut bytes.Buffer
	s := session.New(strings.NewReader(""), &out, &errOut)
	code := Execute(context.Background(), command.Default(), s, BuildInfo{},
		[]string{"connect", "--anonymous", "--url", wanted.URL()})
	if code != ExitOK {
		t.Fatalf("exit code = %d (stderr: %s)", code, errOut.String())
	}
	if wanted.Last("GET", "/") == nil {
		t.Errorf("the --url server was never contacted: %s", out.String())
	}
	if other.Last("GET", "/") != nil {
		t.Error("the default profile server was contacted although --url named another")
	}
}

// The flag and the argument naming different servers is a usage error, and it
// never echoes what was typed: a URL argument may carry a password.
func TestOneShotConnectURLFlagAgainstAnArgument(t *testing.T) {
	srv := couchtest.New(t)
	withConfigDeps(t, config.Defaults())

	var out, errOut bytes.Buffer
	s := session.New(strings.NewReader(""), &out, &errOut)
	code := Execute(context.Background(), command.Default(), s, BuildInfo{},
		[]string{"connect", "--url", srv.URL(), "http://admin:hunter2@other.example.com/"})
	if code != ExitUsage {
		t.Fatalf("exit code = %d, want the usage code %d (stderr: %s)", code, ExitUsage, errOut.String())
	}
	if strings.Contains(out.String()+errOut.String(), "hunter2") {
		t.Errorf("the error echoed the secret: %s%s", out.String(), errOut.String())
	}
	if srv.Last("GET", "/") != nil {
		t.Error("a server was contacted although the two targets disagreed")
	}
}

// withConfigDeps installs deps whose config file holds cfg, which withTempDeps
// (anonymous_test.go) cannot do: it takes an environment, not a config.
func withConfigDeps(t *testing.T, cfg *config.Config) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	command.SetDeps(&command.Deps{
		ConfigPath: path,
		Secrets:    config.NewMemorySecrets(),
		LookupEnv:  func(string) (string, bool) { return "", false },
	})
	t.Cleanup(func() { command.SetDeps(nil) })
}
