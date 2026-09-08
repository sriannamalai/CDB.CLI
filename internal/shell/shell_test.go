package shell

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/command"
	"github.com/sriannamalai/CDB.CLI/internal/couch"
	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

func testShell(t *testing.T, out *bytes.Buffer) (*Shell, *session.Session) {
	t.Helper()
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb/doc1", 200, `{"_id":"doc1","_rev":"1-a","name":"alice"}`)
	c, err := couch.New(couch.Config{URL: srv.URL(), Auth: couch.AuthNone})
	if err != nil {
		t.Fatal(err)
	}
	s := session.New(strings.NewReader(""), out, out)
	s.Attach(c, "test")
	t.Cleanup(func() { _ = s.Detach() })
	sh, err := New(command.Default(), s, Config{HistoryFile: "", Keymap: "emacs"})
	if err != nil {
		t.Fatal(err)
	}
	return sh, s
}

func TestRunLineExecutesACommand(t *testing.T) {
	var out bytes.Buffer
	sh, _ := testShell(t, &out)
	if err := sh.RunLine(context.Background(), "pwd"); err != nil {
		t.Fatal(err)
	}
	if out.String() != "/\n" {
		t.Errorf("output = %q, want %q", out.String(), "/\n")
	}
}

func TestRunLineAppliesAFilter(t *testing.T) {
	var out bytes.Buffer
	sh, s := testShell(t, &out)
	s.SetPath("/mydb")
	if err := sh.RunLine(context.Background(), "cat doc1 | .name"); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != `"alice"` {
		t.Errorf("output = %q, want %q", out.String(), `"alice"`)
	}
}

// --yes and --verbose on a shell line apply to that invocation only: the
// session's own settings must be back in place once the command has run.
func TestRunLineScopesFlagsToOneInvocation(t *testing.T) {
	var out bytes.Buffer
	sh, s := testShell(t, &out)
	if err := sh.RunLine(context.Background(), "pwd --yes --verbose"); err != nil {
		t.Fatal(err)
	}
	if s.Prefs.Yes || s.Prefs.Verbose {
		t.Errorf("flags leaked into the session: Yes=%v Verbose=%v", s.Prefs.Yes, s.Prefs.Verbose)
	}
	s.Prefs.Yes, s.Prefs.Verbose = true, true
	if err := sh.RunLine(context.Background(), "pwd"); err != nil {
		t.Fatal(err)
	}
	if !s.Prefs.Yes || !s.Prefs.Verbose {
		t.Errorf("a plain line cleared the session settings: Yes=%v Verbose=%v", s.Prefs.Yes, s.Prefs.Verbose)
	}
}

func TestRunLineIgnoresBlankLines(t *testing.T) {
	var out bytes.Buffer
	sh, _ := testShell(t, &out)
	if err := sh.RunLine(context.Background(), "   "); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Errorf("blank line produced %q", out.String())
	}
}

func TestRunLineUnknownCommand(t *testing.T) {
	var out bytes.Buffer
	sh, _ := testShell(t, &out)
	err := sh.RunLine(context.Background(), "nosuchcommand")
	if err == nil {
		t.Fatal("unknown command returned no error")
	}
	if !strings.Contains(err.Error(), "nosuchcommand") {
		t.Errorf("error = %v, want it to name the command", err)
	}
}

func TestRunLineExitReturnsErrExit(t *testing.T) {
	var out bytes.Buffer
	sh, _ := testShell(t, &out)
	if err := sh.RunLine(context.Background(), "exit"); err != command.ErrExit {
		t.Fatalf("exit returned %v, want command.ErrExit", err)
	}
}

// TestReportErrorPrintsNothingOnACancelledContext covers the controller ruling
// that Ctrl-C during a command, or replications --watch interrupted, must
// print nothing and just return to the prompt. Run's readline loop needs a
// real terminal, so the print-or-not decision lives in reportError, which is
// exercised directly here the same way RunLine is exercised above.
func TestReportErrorPrintsNothingOnACancelledContext(t *testing.T) {
	var out bytes.Buffer
	sh, _ := testShell(t, &out)
	sh.reportError(context.Canceled)
	if out.String() != "" {
		t.Errorf("stderr = %q, want nothing printed on a cancelled context", out.String())
	}
}

// TestReportErrorPrintsOtherErrors is the control for the test above: a
// cancelled context is the only case reportError swallows.
func TestReportErrorPrintsOtherErrors(t *testing.T) {
	var out bytes.Buffer
	sh, _ := testShell(t, &out)
	sh.reportError(errors.New("boom"))
	if !strings.Contains(out.String(), "boom") {
		t.Errorf("stderr = %q, want it to contain the error", out.String())
	}
}

func TestHelpListsCommands(t *testing.T) {
	var out bytes.Buffer
	sh, _ := testShell(t, &out)
	if err := sh.RunLine(context.Background(), "help"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ls", "cat", "find", "resolve"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("help output is missing %q:\n%s", want, out.String())
		}
	}
}

func TestHistoryDropsDuplicatesAndUnparseableLines(t *testing.T) {
	var out bytes.Buffer
	sh, _ := testShell(t, &out)
	for _, line := range []string{"pwd", "pwd", "cat 'unterminated", "   ", "ls", "pwd"} {
		if _, err := sh.hist.Write(line); err != nil {
			t.Fatal(err)
		}
	}
	got := historyLines(sh.hist)
	want := []string{"pwd", "ls", "pwd"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("history = %v, want %v", got, want)
	}
}

func TestHistoryCommandShowsTheLiveHistory(t *testing.T) {
	var out bytes.Buffer
	sh, _ := testShell(t, &out)
	if _, err := sh.hist.Write("ls /mydb"); err != nil {
		t.Fatal(err)
	}
	if _, err := sh.hist.Write("cat doc1"); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := sh.RunLine(context.Background(), "history"); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if strings.Contains(text, "only available inside") {
		t.Fatalf("the shell is still running the placeholder history command:\n%s", text)
	}
	for _, want := range []string{"ls /mydb", "cat doc1"} {
		if !strings.Contains(text, want) {
			t.Errorf("history output is missing %q:\n%s", want, text)
		}
	}
}

func TestHistoryFileIsWrittenThroughTheFilter(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state", "cdb", "history")
	var out bytes.Buffer
	srv := couchtest.New(t)
	c, err := couch.New(couch.Config{URL: srv.URL(), Auth: couch.AuthNone})
	if err != nil {
		t.Fatal(err)
	}
	s := session.New(strings.NewReader(""), &out, &out)
	s.Attach(c, "test")
	t.Cleanup(func() { _ = s.Detach() })
	sh, err := New(command.Default(), s, Config{HistoryFile: file, Keymap: "emacs"})
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"pwd", "pwd", "ls '"} {
		if _, err := sh.hist.Write(line); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(strings.TrimSpace(string(b)), "\n") + 1; n != 1 {
		t.Errorf("history file has %d entries, want 1:\n%s", n, b)
	}
	if strings.Contains(string(b), "ls '") {
		t.Errorf("an unparseable line reached the history file:\n%s", b)
	}
}
