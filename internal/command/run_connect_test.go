package command

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/config"
	"github.com/sriannamalai/CDB.CLI/internal/couch"
	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// scriptFile writes a one-line script and returns its path.
func scriptFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "job.cdb")
	if err := os.WriteFile(path, []byte("ls /\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// deadURL is the address of a server that is not listening: an httptest
// server that has been shut down, so the port is real and refuses.
func deadURL(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(nil)
	url := srv.URL
	srv.Close()
	return url + "/"
}

// runSession returns a disconnected session and the output both its streams
// share.
func runSession(t *testing.T) (*session.Session, *bytes.Buffer) {
	t.Helper()
	var out bytes.Buffer
	s := session.New(strings.NewReader(""), &out, &out)
	t.Cleanup(func() { _ = s.Detach() })
	return s, &out
}

// #54: "cdb run --url … job.cdb" opens the connection before line 1, so an
// unreachable server is reported as the connection failure it is — once, in
// the ordinary sentence — instead of as a fault of whichever line happened to
// be first.
func TestRunOpensTheConnectionBeforeTheFirstLine(t *testing.T) {
	withDeps(t, config.Defaults(), nil)
	ran := false
	c := RunFrom(func(context.Context, io.Reader, string, []string, bool) error {
		ran = true
		return nil
	})
	s, out := runSession(t)
	s.Prefs.URL = deadURL(t)

	_, err := invoke(t, c, s, scriptFile(t))
	if err == nil {
		t.Fatalf("run against an unreachable server succeeded (output: %s)", out.String())
	}
	// Exit 3: either kind of connection failure the front-end maps there —
	// a ConnectionError, or the server being unreachable.
	var ce *ConnectionError
	ue, isCouch := couch.AsError(err)
	if !errors.As(err, &ce) && !(isCouch && ue.Status == couch.StatusUnreachable) {
		t.Fatalf("error = %#v, want a connection failure so the exit code is 3", err)
	}
	if ran {
		t.Error("the script ran although the connection could not be opened")
	}
}

// The same flag with a server that answers: the connection is open before the
// first line, and the script runs.
func TestRunConnectsThenRunsTheScript(t *testing.T) {
	withDeps(t, config.Defaults(), nil)
	srv := couchtest.New(t)
	ran := false
	c := RunFrom(func(context.Context, io.Reader, string, []string, bool) error {
		ran = true
		return nil
	})
	s, out := runSession(t)
	s.Prefs.URL = srv.URL()

	if _, err := invoke(t, c, s, scriptFile(t)); err != nil {
		t.Fatalf("run: %v (output: %s)", err, out.String())
	}
	if !ran {
		t.Error("the script did not run")
	}
	if !s.Connected() {
		t.Error("the session was left unconnected")
	}
}

// A script with no target named anywhere keeps today's behaviour: nothing is
// dialled up front, and the first line that needs a client says so. This is
// what a script whose own first line is "connect" depends on.
func TestRunWithNoTargetNamedConnectsNothing(t *testing.T) {
	withDeps(t, config.Defaults(), nil)
	ran := false
	c := RunFrom(func(context.Context, io.Reader, string, []string, bool) error {
		ran = true
		return nil
	})
	s, out := runSession(t)

	if _, err := invoke(t, c, s, scriptFile(t)); err != nil {
		t.Fatalf("run: %v (output: %s)", err, out.String())
	}
	if !ran {
		t.Error("the script did not run")
	}
	if s.Connected() {
		t.Error("a connection was opened although nothing named a server")
	}
}

// Inside the shell the session is already connected, and "run" keeps that
// connection rather than opening one of its own -- even with --url left on the
// session's preferences from the command that started it.
func TestRunKeepsAnOpenConnection(t *testing.T) {
	withDeps(t, config.Defaults(), nil)
	srv := couchtest.New(t)
	s := connected(t, srv)
	s.Prefs.URL = deadURL(t)
	c := RunFrom(func(context.Context, io.Reader, string, []string, bool) error { return nil })

	if _, err := invoke(t, c, s, scriptFile(t)); err != nil {
		t.Fatalf("run in a connected session: %v", err)
	}
}
