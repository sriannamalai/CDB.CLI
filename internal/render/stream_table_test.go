package render

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/command"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// countingStream yields n rows and then fails, counting how many rows it has
// handed over so far.
func countingStream(n int, after error, produced *int) command.Stream {
	i := 0
	return command.Stream{
		Columns: []command.Column{{Title: "id"}, {Title: "rev"}},
		Next: func() (command.Row, bool, error) {
			if i >= n {
				return command.Row{}, false, after
			}
			id := fmt.Sprintf("doc%05d", i)
			i++
			if produced != nil {
				*produced = i
			}
			return command.Row{
				Cells: []string{id, "1-a"},
				JSON:  json.RawMessage(`{"id":"` + id + `"}`),
			}, true, nil
		},
	}
}

// watchWriter records how far the stream had got when the first byte was
// written.
type watchWriter struct {
	buf      bytes.Buffer
	produced *int
	firstAt  int
}

func (w *watchWriter) Write(p []byte) (int, error) {
	if w.firstAt == 0 && len(p) > 0 {
		w.firstAt = *w.produced
	}
	return w.buf.Write(p)
}

// Spec section 10: nothing loads an unbounded result into memory. The table
// branch used to append every row to a go-pretty writer and render once at the
// end, so "ls /db --all" on a terminal held the whole database in memory and
// printed nothing until it had finished — indistinguishable from a hang. It
// must now emit each page as it fills, so at most one chunk is ever held.
func TestStreamTableHoldsAtMostOneChunk(t *testing.T) {
	const rows = 10000
	var produced int
	w := &watchWriter{produced: &produced}
	r := New(w, Options{Format: session.FormatTable})

	if err := r.Render(countingStream(rows, nil, &produced)); err != nil {
		t.Fatal(err)
	}
	if w.firstAt == 0 {
		t.Fatal("the renderer wrote nothing")
	}
	if w.firstAt > streamTableChunk+1 {
		t.Errorf("the first byte was written only after %d of %d rows had been read; want no more than one chunk (%d) buffered",
			w.firstAt, rows, streamTableChunk)
	}
}

// A stream that fails part way through still shows the rows that arrived, and
// surfaces the failure.
func TestStreamTableFlushesWhatArrivedBeforeAFailure(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, Options{Format: session.FormatTable})
	boom := errors.New("the server went away")

	err := r.Render(countingStream(streamTableChunk+10, boom, nil))
	if !errors.Is(err, boom) {
		t.Fatalf("Render error = %v, want the stream's own error", err)
	}
	out := buf.String()
	if !strings.Contains(out, "doc00000") {
		t.Errorf("the first row was never written:\n%s", out)
	}
	last := fmt.Sprintf("doc%05d", streamTableChunk+9)
	if !strings.Contains(out, last) {
		t.Errorf("the rows read before the failure were dropped (%s missing)", last)
	}
}

// A stream that ends normally still renders every row, chunk boundaries and
// all.
func TestStreamTableRendersEveryRowAcrossChunks(t *testing.T) {
	const rows = 10000
	var buf bytes.Buffer
	r := New(&buf, Options{Format: session.FormatTable})
	if err := r.Render(countingStream(rows, nil, nil)); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"doc00000", "doc00199", "doc00200", "doc09999"} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q", want)
		}
	}
	// The header is repeated once per chunk, which is what makes a long
	// listing readable as it scrolls past.
	if got, want := strings.Count(out, "REV"), rows/streamTableChunk; got != want {
		t.Errorf("header appeared %d times, want %d (one per chunk)", got, want)
	}
}

// A stream shorter than one chunk is still one table.
func TestStreamTableRendersAShortStreamAsOneTable(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, Options{Format: session.FormatTable})
	if err := r.Render(countingStream(3, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(buf.String(), "REV"); got != 1 {
		t.Errorf("header appeared %d times for 3 rows, want 1", got)
	}
}

// An empty stream prints the header and nothing else, as it did before.
func TestStreamTableRendersAnEmptyStream(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, Options{Format: session.FormatTable})
	if err := r.Render(countingStream(0, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "ID") {
		t.Errorf("empty stream = %q, want the header", buf.String())
	}
}
