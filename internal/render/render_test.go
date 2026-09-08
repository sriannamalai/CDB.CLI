package render

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/command"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// golden compares got against a checked-in golden file. The golden files are
// written by hand (Step 2 below), never generated from the implementation, so
// that the assertion can actually fail.
func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	p := filepath.Join("testdata", name)
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read golden %s: %v", p, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("output does not match %s\n--- got ---\n%q\n--- want ---\n%q", p, got, want)
	}
}

func sampleRows() command.Rows {
	return command.Rows{
		Columns: []command.Column{
			{Title: "name", Align: command.AlignLeft},
			{Title: "docs", Align: command.AlignRight},
		},
		Items: []command.Row{
			{Cells: []string{"mydb", "42"}, JSON: json.RawMessage(`{"name":"mydb","docs":42}`)},
			{Cells: []string{"other", "7"}, JSON: json.RawMessage(`{"name":"other","docs":7}`)},
		},
		Hint: `more rows: ls --start "other"`,
	}
}

func TestRenderRowsTablePlain(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, Options{Format: session.FormatTable, Color: false, Width: 80})
	if err := r.Render(sampleRows()); err != nil {
		t.Fatal(err)
	}
	golden(t, "rows_table.golden", buf.Bytes())
}

func TestRenderRowsTableColor(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, Options{Format: session.FormatTable, Color: true, Width: 80})
	if err := r.Render(sampleRows()); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	// The coloured style keeps go-pretty's box border and paints the header
	// bold cyan (ESC[1;36m). These assertions replace a golden file: an escape-
	// laden golden is unreadable by hand, and generating it from the
	// implementation would make the test tautological.
	if !strings.Contains(got, "\x1b[1;36m") {
		t.Errorf("colour output has no bold-cyan header escape:\n%q", got)
	}
	if !strings.Contains(got, "┌") || !strings.Contains(got, "└") {
		t.Errorf("colour output has no box border:\n%q", got)
	}
	if !strings.Contains(got, "NAME") || !strings.Contains(got, "mydb") || !strings.Contains(got, "42") {
		t.Errorf("colour output is missing the header or the rows:\n%q", got)
	}
}

func TestRenderRowsRawIsOneJSONPerLine(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, Options{Format: session.FormatRaw})
	if err := r.Render(sampleRows()); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("raw rows produced %d lines, want 2:\n%s", len(lines), buf.String())
	}
	if lines[0] != `{"name":"mydb","docs":42}` {
		t.Errorf("line 0 = %q", lines[0])
	}
	if strings.Contains(buf.String(), "more rows") {
		t.Error("raw output must not include the paging hint")
	}
}

func TestRenderDocumentJSONPretty(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, Options{Format: session.FormatJSON, Color: false})
	doc := command.Document{JSON: json.RawMessage(`{"_id":"doc1","n":1,"tags":["a","b"]}`)}
	if err := r.Render(doc); err != nil {
		t.Fatal(err)
	}
	golden(t, "document_json.golden", buf.Bytes())
}

func TestRenderDocumentRawIsCompact(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, Options{Format: session.FormatRaw})
	doc := command.Document{JSON: json.RawMessage("{\n  \"a\" : 1\n}")}
	if err := r.Render(doc); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "{\"a\":1}\n" {
		t.Errorf("raw document = %q, want %q", buf.String(), "{\"a\":1}\n")
	}
}

func TestRenderMessageAndEmpty(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, Options{Format: session.FormatTable})
	if err := r.Render(command.Message{Text: "/mydb"}); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "/mydb\n" {
		t.Errorf("message = %q", buf.String())
	}
	buf.Reset()
	if err := r.Render(command.Empty{}); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Errorf("empty result wrote %q", buf.String())
	}
}

func TestRenderStream(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, Options{Format: session.FormatRaw})
	i := 0
	items := []command.Row{
		{Cells: []string{"a"}, JSON: json.RawMessage(`{"id":"a"}`)},
		{Cells: []string{"b"}, JSON: json.RawMessage(`{"id":"b"}`)},
	}
	st := command.Stream{
		Columns: []command.Column{{Title: "id"}},
		Next: func() (command.Row, bool, error) {
			if i >= len(items) {
				return command.Row{}, false, nil
			}
			row := items[i]
			i++
			return row, true, nil
		},
	}
	if err := r.Render(st); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "{\"id\":\"a\"}\n{\"id\":\"b\"}\n" {
		t.Errorf("stream = %q", buf.String())
	}
}

func TestRenderRawCopiesBytes(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, Options{Format: session.FormatTable})
	if err := r.Render(command.Raw{Reader: strings.NewReader("JPEGDATA"), ContentType: "image/jpeg"}); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "JPEGDATA" {
		t.Errorf("raw = %q", buf.String())
	}
}

func TestResolveColorHonoursNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	if ResolveColor(session.ColorAlways, io.Discard) {
		t.Error("NO_COLOR did not disable colour")
	}
}

func TestResolveColorNeverAndAlways(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	if ResolveColor(session.ColorNever, io.Discard) {
		t.Error("ColorNever enabled colour")
	}
	if !ResolveColor(session.ColorAlways, io.Discard) {
		t.Error("ColorAlways disabled colour")
	}
	if ResolveColor(session.ColorAuto, io.Discard) {
		t.Error("ColorAuto enabled colour on a non-terminal")
	}
}

func TestOptionsForForcesRawWhenNotATerminal(t *testing.T) {
	prefs := session.DefaultPrefs()
	opts := OptionsFor(prefs, io.Discard, false)
	if opts.Format != session.FormatRaw {
		t.Errorf("Format on a non-terminal = %q, want %q", opts.Format, session.FormatRaw)
	}
	if opts.Pager != "" {
		t.Errorf("Pager on a non-terminal = %q, want empty", opts.Pager)
	}
	opts = OptionsFor(prefs, io.Discard, true)
	if opts.Format != session.FormatRaw {
		t.Errorf("--json Format = %q, want %q", opts.Format, session.FormatRaw)
	}
}

func TestIsTerminalOnNonTerminals(t *testing.T) {
	if IsTerminal(io.Discard) {
		t.Error("IsTerminal(io.Discard) = true")
	}
	if IsTerminalReader(strings.NewReader("")) {
		t.Error("IsTerminalReader(*strings.Reader) = true")
	}
	f, err := os.CreateTemp(t.TempDir(), "notatty")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if IsTerminal(f) || IsTerminalReader(f) {
		t.Error("a regular file was reported as a terminal")
	}
}

func TestCompactJSON(t *testing.T) {
	got, err := CompactJSON([]byte("{\n \"a\" : [1, 2] }"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"a":[1,2]}` {
		t.Errorf("CompactJSON = %q", got)
	}
}
