// Package render turns command results into terminal output: tables, coloured
// JSON, or compact JSON for pipes.
package render

import (
	"fmt"
	"io"

	"github.com/sriannamalai/CDB.CLI/internal/command"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// Options configure a Renderer.
type Options struct {
	Format  session.Format
	Color   bool
	Pager   string
	Width   int
	Height  int
	Verbose bool
}

// OptionsFor derives render options from session preferences and the output
// stream. Output that is not a terminal is always compact JSON with no pager
// and no colour, and --json forces the same.
func OptionsFor(prefs session.Prefs, w io.Writer, forceJSON bool) Options {
	tty := IsTerminal(w)
	width, height := TerminalSize(w)
	opts := Options{
		Format:  prefs.Format,
		Color:   ResolveColor(prefs.Color, w),
		Pager:   pagerCommand(prefs.Pager),
		Width:   width,
		Height:  height,
		Verbose: prefs.Verbose,
	}
	if !tty {
		opts.Format = session.FormatRaw
		opts.Color = false
		opts.Pager = ""
		opts.Width = 0
	}
	if forceJSON {
		opts.Format = session.FormatRaw
		opts.Color = false
		opts.Pager = ""
	}
	return opts
}

// Renderer writes command results.
type Renderer struct {
	out  io.Writer
	opts Options
}

// New returns a Renderer writing to out.
func New(out io.Writer, opts Options) *Renderer { return &Renderer{out: out, opts: opts} }

// Render writes one result.
func (r *Renderer) Render(res command.Result) error {
	switch v := res.(type) {
	case command.Empty:
		return nil
	case command.Message:
		_, err := fmt.Fprintln(r.out, v.Text)
		return err
	case command.Raw:
		_, err := io.Copy(r.out, v.Reader)
		return err
	case command.Document:
		return r.renderDocument(v)
	case command.Rows:
		return r.renderRows(v)
	case command.Stream:
		return r.renderStream(v)
	default:
		return fmt.Errorf("render: unknown result kind %q", res.ResultKind())
	}
}

func (r *Renderer) renderDocument(d command.Document) error {
	switch r.opts.Format {
	case session.FormatRaw:
		b, err := CompactJSON(d.JSON)
		if err != nil {
			b = d.JSON
		}
		_, err = r.out.Write(append(b, '\n'))
		return err
	default:
		out, done := r.maybePage(estimateLines(d.JSON))
		defer done()
		return HighlightJSON(out, d.JSON, r.opts.Color)
	}
}

func (r *Renderer) renderRows(rows command.Rows) error {
	if r.opts.Format == session.FormatRaw || r.opts.Format == session.FormatJSON {
		return r.renderRowsJSON(rows)
	}
	out, done := r.maybePage(len(rows.Items) + 4)
	defer done()
	t := newTable(out, rows.Columns, r.opts.Color, r.opts.Width)
	for _, item := range rows.Items {
		t.AppendRow(toTableRow(item.Cells))
	}
	t.Render()
	if rows.Hint != "" {
		if _, err := fmt.Fprintln(out, rows.Hint); err != nil {
			return err
		}
	}
	return nil
}

func (r *Renderer) renderRowsJSON(rows command.Rows) error {
	for _, item := range rows.Items {
		if err := r.writeRowJSON(r.out, item); err != nil {
			return err
		}
	}
	return nil
}

func (r *Renderer) writeRowJSON(w io.Writer, item command.Row) error {
	if item.JSON == nil {
		return nil
	}
	if r.opts.Format == session.FormatJSON {
		return HighlightJSON(w, item.JSON, r.opts.Color)
	}
	b, err := CompactJSON(item.JSON)
	if err != nil {
		b = item.JSON
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

func (r *Renderer) renderStream(st command.Stream) error {
	if r.opts.Format == session.FormatRaw || r.opts.Format == session.FormatJSON {
		for {
			row, ok, err := st.Next()
			if err != nil {
				return err
			}
			if !ok {
				return nil
			}
			if err := r.writeRowJSON(r.out, row); err != nil {
				return err
			}
		}
	}
	out, done := r.maybePage(r.opts.Height + 1)
	defer done()
	t := newTable(out, st.Columns, r.opts.Color, r.opts.Width)
	for {
		row, ok, err := st.Next()
		if err != nil {
			t.Render()
			return err
		}
		if !ok {
			break
		}
		t.AppendRow(toTableRow(row.Cells))
	}
	t.Render()
	return nil
}

// maybePage returns a writer that may be a pager process, plus a function that
// closes it. lines is the estimated output height.
func (r *Renderer) maybePage(lines int) (io.Writer, func()) {
	if r.opts.Pager == "" || r.opts.Height <= 0 || lines <= r.opts.Height {
		return r.out, func() {}
	}
	p := startPager(r.opts.Pager, r.out)
	if p == nil {
		return r.out, func() {}
	}
	return p, func() { _ = p.Close() }
}

func estimateLines(b []byte) int {
	n := 1
	for _, c := range b {
		if c == ',' || c == '{' || c == '[' {
			n++
		}
	}
	return n
}
