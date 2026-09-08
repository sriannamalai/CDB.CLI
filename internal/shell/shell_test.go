package shell

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

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

// syncBuffer is a buffer a test can read while the shell writes to it from
// another goroutine.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// liveFeedCommand is a stand-in for "tail --follow": it hands over two changes
// and then blocks, as a feed does between changes, until block is closed.
func liveFeedCommand(block <-chan struct{}) command.Command {
	return command.Command{
		Name:    "feed",
		Summary: "a live stream, for tests",
		Example: "$ feed",
		MaxArgs: 0,
		Run: func(ctx context.Context, s *session.Session, inv command.Invocation) (command.Result, error) {
			rows := []command.Row{
				{Cells: []string{"1-x", "a"}, JSON: []byte(`{"seq":"1-x","id":"a"}`)},
				{Cells: []string{"2-y", "b"}, JSON: []byte(`{"seq":"2-y","id":"b"}`)},
			}
			i := 0
			return command.Stream{
				Live:    true,
				Columns: []command.Column{{Title: "seq"}, {Title: "id"}},
				Next: func() (command.Row, bool, error) {
					if i < len(rows) {
						i++
						return rows[i-1], true, nil
					}
					<-block
					return command.Row{}, false, nil
				},
			}, nil
		},
	}
}

// A filter over a feed with no end must be applied change by change. Collecting
// the stream first, as every other result is collected, means "tail --follow |
// .id" prints nothing for as long as it runs — which is forever.
func TestRunLineFiltersALiveStreamAsRowsArrive(t *testing.T) {
	out := &syncBuffer{}
	srv := couchtest.New(t)
	c, err := couch.New(couch.Config{URL: srv.URL(), Auth: couch.AuthNone})
	if err != nil {
		t.Fatal(err)
	}
	s := session.New(strings.NewReader(""), out, out)
	s.Attach(c, "test")
	t.Cleanup(func() { _ = s.Detach() })
	block := make(chan struct{})
	reg := command.Default()
	reg.Register(liveFeedCommand(block))
	sh, err := New(reg, s, Config{HistoryFile: "", Keymap: "emacs"})
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- sh.RunLine(context.Background(), "feed | .id") }()

	// Both filtered values are written while the stream is still open, which
	// is the whole point: nothing waits for an end that is not coming.
	const want = "\"a\"\n\"b\"\n"
	deadline := time.Now().Add(5 * time.Second)
	for out.String() != want {
		if time.Now().After(deadline) {
			t.Fatalf("output = %q, want %q while the feed is still open", out.String(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(block)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}
