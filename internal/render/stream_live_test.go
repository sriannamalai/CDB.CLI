package render

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/command"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// lineCounter records how many newline-terminated writes it has taken, so a
// test can ask how much had been written at the moment a row was produced.
type lineCounter struct {
	buf    bytes.Buffer
	writes int
}

func (w *lineCounter) Write(p []byte) (int, error) {
	w.writes++
	return w.buf.Write(p)
}

// Spec section 4: a Live stream's rows must appear as they arrive. The
// buffered table path holds 200 rows before it emits anything, which for
// "tail --follow" means a change is invisible until 199 more have happened.
func TestLiveStreamWritesEachRowBeforeTheNextIsProduced(t *testing.T) {
	const rows = 5
	w := &lineCounter{}
	seen := make([]int, 0, rows)
	i := 0
	st := command.Stream{
		Live:    true,
		Columns: []command.Column{{Title: "seq"}, {Title: "id"}},
		Next: func() (command.Row, bool, error) {
			// Record how much output existed when this row was asked for.
			seen = append(seen, w.writes)
			if i >= rows {
				return command.Row{}, false, nil
			}
			id := fmt.Sprintf("doc%d", i)
			i++
			return command.Row{
				Cells: []string{fmt.Sprintf("%d-x", i), id},
				JSON:  json.RawMessage(`{"id":"` + id + `"}`),
			}, true, nil
		},
	}
	if err := New(w, Options{Format: session.FormatTable}).Render(st); err != nil {
		t.Fatal(err)
	}
	// The header is written before the first row is asked for, and every row
	// is written before the next is asked for.
	for n, writes := range seen {
		if want := n + 1; writes != want {
			t.Errorf("row %d was produced after %d writes, want %d — rows are being buffered", n, writes, want)
		}
	}
	out := w.buf.String()
	if !strings.HasPrefix(out, "seq  id\n") {
		t.Errorf("output does not start with the column titles:\n%s", out)
	}
	if !strings.Contains(out, "1-x  doc0\n") {
		t.Errorf("output has no plain row line:\n%s", out)
	}
}

func TestLiveStreamJSONWritesOneRowPerLine(t *testing.T) {
	w := &lineCounter{}
	i := 0
	st := command.Stream{
		Live:    true,
		Columns: []command.Column{{Title: "seq"}},
		Next: func() (command.Row, bool, error) {
			if i >= 2 {
				return command.Row{}, false, nil
			}
			i++
			return command.Row{JSON: json.RawMessage(fmt.Sprintf(`{"seq":%d}`, i))}, true, nil
		},
	}
	if err := New(w, Options{Format: session.FormatRaw}).Render(st); err != nil {
		t.Fatal(err)
	}
	if got := w.buf.String(); got != "{\"seq\":1}\n{\"seq\":2}\n" {
		t.Errorf("raw output = %q", got)
	}
}

// A Live stream must not be handed to the pager: a pager holds the output
// until the process exits, which for a feed that never ends is forever.
func TestLiveStreamIsNotPaged(t *testing.T) {
	w := &lineCounter{}
	i := 0
	st := command.Stream{
		Live:    true,
		Columns: []command.Column{{Title: "seq"}},
		Next: func() (command.Row, bool, error) {
			if i >= 3 {
				return command.Row{}, false, nil
			}
			i++
			return command.Row{Cells: []string{fmt.Sprintf("%d-x", i)}}, true, nil
		},
	}
	// Height 1 with a working pager is exactly the case the buffered path
	// pages on.
	if err := New(w, Options{Format: session.FormatTable, Pager: "cat", Height: 1}).Render(st); err != nil {
		t.Fatal(err)
	}
	if got := w.buf.String(); !strings.Contains(got, "3-x") {
		t.Errorf("output did not reach the writer directly:\n%s", got)
	}
}

// A non-Live stream keeps the buffered table and now prints its hint.
func TestBufferedStreamPrintsItsHint(t *testing.T) {
	var buf bytes.Buffer
	i := 0
	st := command.Stream{
		Columns: []command.Column{{Title: "id"}},
		Hint:    `more changes: tail /mydb --since "2-y"`,
		Next: func() (command.Row, bool, error) {
			if i >= 1 {
				return command.Row{}, false, nil
			}
			i++
			return command.Row{Cells: []string{"a"}}, true, nil
		},
	}
	if err := New(&buf, Options{Format: session.FormatTable}).Render(st); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `more changes: tail /mydb --since "2-y"`) {
		t.Errorf("hint missing from:\n%s", buf.String())
	}
}
