package shell

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/command"
	"github.com/sriannamalai/CDB.CLI/internal/couch"
	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// testShellWithHistoryFile is testShell with a history source backed by a real
// file, so a test can read back exactly what was persisted.
func testShellWithHistoryFile(t *testing.T, out *bytes.Buffer, file string) *Shell {
	t.Helper()
	srv := couchtest.New(t)
	c, err := couch.New(couch.Config{URL: srv.URL(), Auth: couch.AuthNone})
	if err != nil {
		t.Fatal(err)
	}
	s := session.New(strings.NewReader(""), out, out)
	s.Attach(c, "test")
	t.Cleanup(func() { _ = s.Detach() })
	sh, err := New(command.Default(), s, Config{HistoryFile: file, Keymap: "emacs"})
	if err != nil {
		t.Fatal(err)
	}
	return sh
}

// A credential typed on a shell line reaches two places the operator does not
// expect: the history file, which outlives the session, and the "history"
// command, which prints every stored line straight to stdout. The line is
// rewritten rather than dropped, so it stays a command that can be re-run.
func TestHistoryRedactsCredentialsInTypedLines(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string
	}{
		{
			name: "connect with a userinfo URL",
			line: "connect https://admin:hunter2@db.example.com --save --as prod",
			want: "connect https://db.example.com --save --as prod",
		},
		{
			name: "profiles add",
			line: "profiles add prod https://admin:hunter2@db.example.com:5984/",
			want: "profiles add prod https://db.example.com:5984/",
		},
		{
			name: "replicate to a remote with credentials",
			line: "replicate movies https://admin:hunter2@remote.example.com/movies",
			want: "replicate movies https://remote.example.com/movies",
		},
		{
			name: "a schemeless user:pass@host",
			line: "connect admin:hunter2@db.example.com:5984",
			want: "connect db.example.com:5984",
		},
		{
			name: "a quoted URL keeps its quotes",
			line: "connect 'https://admin:hunter2@db.example.com'",
			want: "connect 'https://db.example.com'",
		},
		{
			name: "a document id containing an at sign is left alone",
			line: "cat /users/alice@example.com",
			want: "cat /users/alice@example.com",
		},
		{
			name: "an ordinary line is untouched",
			line: "ls /movies --limit 3",
			want: "ls /movies --limit 3",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			sh, _ := testShell(t, &out)
			if _, err := sh.hist.Write(tc.line); err != nil {
				t.Fatal(err)
			}
			got := historyLines(sh.hist)
			if len(got) != 1 || got[0] != tc.want {
				t.Fatalf("history = %q, want [%q]", got, tc.want)
			}
			if strings.Contains(got[0], "hunter2") {
				t.Errorf("the password survived into the history: %q", got[0])
			}
		})
	}
}

// The "history" command reads the same source back, so a redacted line is what
// reaches stdout too.
func TestHistoryCommandNeverPrintsACredential(t *testing.T) {
	var out bytes.Buffer
	sh, _ := testShell(t, &out)
	if _, err := sh.hist.Write("connect https://admin:hunter2@db.example.com --save"); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := sh.RunLine(context.Background(), "history"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "hunter2") {
		t.Errorf("history printed the password:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "connect https://db.example.com --save") {
		t.Errorf("history did not print the redacted line:\n%s", out.String())
	}
}

// And the file on disk, which is what a later session, a Ctrl-R search and
// "history --json | jq" all read.
func TestHistoryFileNeverHoldsACredential(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state", "cdb", "history")
	var out bytes.Buffer
	sh := testShellWithHistoryFile(t, &out, file)
	if _, err := sh.hist.Write("connect https://admin:hunter2@db.example.com --save"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "hunter2") {
		t.Errorf("the password reached the history file:\n%s", b)
	}
	if !strings.Contains(string(b), "connect https://db.example.com --save") {
		t.Errorf("the redacted line is missing from the history file:\n%s", b)
	}
}
