package command

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// piped runs a command with a pipeline made of vals, and drains whatever
// result it returns into rows of cells.
func piped(t *testing.T, c Command, s *session.Session, argv []string, vals ...string) ([][]string, error) {
	t.Helper()
	src := make(chan json.RawMessage, len(vals))
	for _, v := range vals {
		src <- json.RawMessage(v)
	}
	close(src)
	fs := NewRegistry().NewFlagSet(c)
	if err := fs.Parse(argv); err != nil {
		return nil, err
	}
	if err := c.CheckArgsErr(fs.Args()); err != nil {
		return nil, err
	}
	res, err := c.Run(context.Background(), s, Invocation{
		Args: fs.Args(), Flags: fs, Stdin: s.Stdin(), Stdout: s.Stdout, Stderr: s.Stderr,
		Shell: true, Pipe: NewPipe(src),
	})
	if err != nil {
		return nil, err
	}
	st, ok := res.(Stream)
	if !ok {
		t.Fatalf("result is %T, want Stream", res)
	}
	var out [][]string
	for {
		row, ok, err := st.Next()
		if err != nil {
			return out, err
		}
		if !ok {
			return out, nil
		}
		out = append(out, row.Cells)
	}
}

func TestPutPipelineWritesAndReportsEveryDocument(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/movies/_bulk_docs", 201, `[
		{"ok":true,"id":"a","rev":"1-x"},
		{"id":"b","error":"conflict","reason":"Document update conflict."}]`)
	s := connected(t, srv)
	s.Prefs.Yes = true
	rows, err := piped(t, Put(), s, []string{"/movies"}, `{"_id":"a"}`, `{"_id":"b","_rev":"1-old"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %v", rows)
	}
	if rows[0][0] != "a" || rows[0][1] != "1-x" || rows[0][2] != "ok" {
		t.Errorf("row 0 = %v", rows[0])
	}
	if rows[1][2] != "conflict" {
		t.Errorf("row 1 = %v; a per-document conflict is a row, not a line failure", rows[1])
	}
}

func TestPutPipelineTakesTheDatabaseFromTheCurrentDirectory(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/movies/_bulk_docs", 201, `[{"ok":true,"id":"a","rev":"1-x"}]`)
	s := connected(t, srv)
	s.SetPath("/movies")
	rows, err := piped(t, Put(), s, nil, `{"_id":"a"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0][0] != "a" {
		t.Errorf("rows = %v", rows)
	}
}

func TestPutPipelineAtTheRootIsAUsageError(t *testing.T) {
	srv := couchtest.New(t)
	s := connected(t, srv)
	_, err := piped(t, Put(), s, nil, `{"_id":"a"}`)
	if err == nil || err.Error() != "put needs a database path when the current directory is /" {
		t.Fatalf("error = %v", err)
	}
	var ue *UsageError
	if !errorsAs(err, &ue) {
		t.Errorf("error is %T, want *UsageError so the exit code is 2", err)
	}
}

func TestPutPipelineRejectsAValueThatIsNotAnObject(t *testing.T) {
	srv := couchtest.New(t)
	s := connected(t, srv)
	s.SetPath("/movies")
	_, err := piped(t, Put(), s, nil, `{"_id":"a"}`, `7`)
	if err == nil || !strings.Contains(err.Error(), "value 2 is not a JSON object") {
		t.Fatalf("error = %v", err)
	}
}

// errorsAs is errors.As, named here so the tests above read as prose.
func errorsAs(err error, target any) bool { return errors.As(err, target) }
