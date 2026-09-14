package main

import (
	"bytes"
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/command"
	"github.com/sriannamalai/CDB.CLI/internal/config"
	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
	"github.com/sriannamalai/CDB.CLI/internal/session"
	"github.com/sriannamalai/CDB.CLI/internal/shell"
)

// stdinScript runs text as "cdb < file" against the server at url and returns
// the exit code and everything the process wrote.
func stdinScript(t *testing.T, url, text string) (int, string, *session.Session) {
	t.Helper()
	command.SetDeps(&command.Deps{
		ConfigPath: filepath.Join(t.TempDir(), "config.toml"),
		Secrets:    config.NewMemorySecrets(),
		LookupEnv:  func(string) (string, bool) { return "", false },
	})
	t.Cleanup(func() { command.SetDeps(nil) })
	var out bytes.Buffer
	s := session.New(strings.NewReader(""), &out, &out)
	t.Cleanup(func() { _ = s.Detach() })
	s.Prefs.URL = url
	sh, err := shell.New(command.Default(), s, shell.Config{})
	if err != nil {
		t.Fatal(err)
	}
	code := runStdinScript(context.Background(), sh, s, strings.NewReader(text))
	return code, out.String(), s
}

// #54: "cdb < job.cdb" opens the connection before the first line, so a server
// that is not listening is reported once as the connection failure it is --
// exit 3, the ordinary sentence, and no line named.
func TestStdinScriptReportsAConnectionFailureOnce(t *testing.T) {
	srv := httptest.NewServer(nil)
	url := srv.URL + "/"
	srv.Close()

	code, out, s := stdinScript(t, url, "ls /\n")
	if code != 3 {
		t.Errorf("exit code = %d, want 3 (output: %s)", code, out)
	}
	if !strings.Contains(out, "Could not reach") {
		t.Errorf("output = %q, want the unreachable-server sentence", out)
	}
	if strings.Contains(out, "stdin:1:") {
		t.Errorf("the connection failure was blamed on a line:\n%s", out)
	}
	if n := strings.Count(out, "Could not reach"); n != 1 {
		t.Errorf("the sentence was printed %d times:\n%s", n, out)
	}
	if s.Connected() {
		t.Error("the session was left connected to a server that never answered")
	}
}

// A server that answers: the connection is open before line 1, and the lines
// run.
func TestStdinScriptConnectsThenRunsTheLines(t *testing.T) {
	srv := couchtest.New(t)
	code, out, s := stdinScript(t, srv.URL(), "pwd\n")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (output: %s)", code, out)
	}
	if !s.Connected() {
		t.Error("the session was left unconnected")
	}
	if !strings.Contains(out, "/") {
		t.Errorf("output = %q, want the line's own output", out)
	}
}
