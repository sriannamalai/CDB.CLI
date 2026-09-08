package render

import (
	"io"

	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/jedib0t/go-pretty/v6/text"
	"github.com/sriannamalai/CDB.CLI/internal/command"
)

// newTable builds a go-pretty writer configured for cdb.
func newTable(out io.Writer, cols []command.Column, color bool, width int) table.Writer {
	t := table.NewWriter()
	t.SetOutputMirror(out)
	if color {
		t.SetStyle(table.StyleLight)
		t.Style().Color.Header = text.Colors{text.Bold, text.FgCyan}
	} else {
		st := table.StyleLight
		st.Options.DrawBorder = false
		st.Options.SeparateColumns = true
		st.Options.SeparateHeader = true
		st.Options.SeparateRows = false
		t.SetStyle(st)
	}
	if width > 0 {
		t.SetAllowedRowLength(width)
	}
	header := make(table.Row, len(cols))
	cfgs := make([]table.ColumnConfig, len(cols))
	for i, c := range cols {
		header[i] = c.Title
		cfg := table.ColumnConfig{Number: i + 1}
		if c.Align == command.AlignRight {
			cfg.Align = text.AlignRight
			cfg.AlignHeader = text.AlignRight
		}
		cfgs[i] = cfg
	}
	t.AppendHeader(header)
	t.SetColumnConfigs(cfgs)
	return t
}

func toTableRow(cells []string) table.Row {
	row := make(table.Row, len(cells))
	for i, c := range cells {
		row[i] = c
	}
	return row
}
