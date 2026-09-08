package command

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/pflag"
	"github.com/sriannamalai/CDB.CLI/internal/couch"
	"github.com/sriannamalai/CDB.CLI/internal/path"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// Cd returns the cd command.
func Cd() Command {
	return Command{
		Name:        "cd",
		Summary:     "Change the current path",
		Usage:       "[path]",
		MinArgs:     0,
		MaxArgs:     1,
		NeedsClient: true,
		Complete:    completePath,
		Run: func(ctx context.Context, s *session.Session, inv Invocation) (Result, error) {
			arg := inv.Arg(0)
			if arg == "" {
				arg = "/"
			}
			t, err := s.Resolve(arg)
			if err != nil {
				return nil, err
			}
			if err := verifyTarget(ctx, s, t); err != nil {
				return nil, err
			}
			s.SetPath(t.Path)
			return Empty{}, nil
		},
	}
}

// verifyTarget checks that a path exists before the session moves to it.
func verifyTarget(ctx context.Context, s *session.Session, t path.Target) error {
	switch t.Kind {
	case path.KindServer:
		_, err := s.Client.ServerInfo(ctx)
		return err
	case path.KindDatabase, path.KindPartition:
		ok, err := s.Client.DatabaseExists(ctx, t.Database)
		if err != nil {
			return err
		}
		if !ok {
			return couch.NewError(404, "not_found", "Database does not exist.", "read", fmt.Sprintf("database %q", t.Database))
		}
		return nil
	default:
		// Task 10 replaces this branch with a document existence check once
		// Client.GetDocument exists. Until then, verifying the database is
		// enough to catch the common mistake of cd-ing into a missing database.
		ok, err := s.Client.DatabaseExists(ctx, t.Database)
		if err != nil {
			return err
		}
		if !ok {
			return couch.NewError(404, "not_found", "Database does not exist.", "read", fmt.Sprintf("database %q", t.Database))
		}
		return nil
	}
}

// Ls returns the ls command.
func Ls() Command {
	return Command{
		Name:        "ls",
		Aliases:     []string{"list"},
		Summary:     "List databases, documents, or the parts of a design document",
		Usage:       "[path]",
		MinArgs:     0,
		MaxArgs:     1,
		NeedsClient: true,
		Complete:    completePath,
		Flags: func(fs *pflag.FlagSet) {
			fs.Int("limit", 20, "rows per page")
			fs.String("start", "", "start listing at this document id")
			fs.Bool("all", false, "stream every row instead of one page")
			fs.StringSlice("fields", nil, "document fields to add as columns")
		},
		Run: func(ctx context.Context, s *session.Session, inv Invocation) (Result, error) {
			t, err := s.Resolve(inv.Arg(0))
			if err != nil {
				return nil, err
			}
			switch t.Kind {
			case path.KindServer:
				return lsServer(ctx, s)
			case path.KindDatabase, path.KindPartition:
				return lsDatabase(ctx, s, inv, t)
			case path.KindDesignDoc:
				return lsDesignDoc(ctx, s, t)
			default:
				return nil, Usagef("ls", "%s is a %s, not something that can be listed. Use \"cat\" to read it.", t.Path, t.Kind)
			}
		},
	}
}

func lsServer(ctx context.Context, s *session.Session) (Result, error) {
	names, err := s.Client.ListDatabases(ctx)
	if err != nil {
		return nil, err
	}
	infos, err := s.Client.DatabasesInfo(ctx, names)
	if err != nil {
		return nil, err
	}
	rows := Rows{Columns: []Column{
		{Title: "name"},
		{Title: "docs", Align: AlignRight},
		{Title: "size", Align: AlignRight},
		{Title: "partitioned"},
	}}
	for _, i := range infos {
		rows.Items = append(rows.Items, Row{
			Cells: []string{i.Name, strconv.FormatInt(i.DocCount, 10), humanBytes(i.DiskSize), strconv.FormatBool(i.Partitioned)},
			JSON:  mustJSON(i),
		})
	}
	return rows, nil
}

func lsDatabase(ctx context.Context, s *session.Session, inv Invocation, t path.Target) (Result, error) {
	fields := inv.StringSlice("fields")
	opts := couch.AllDocsOptions{
		Partition:     t.Partition,
		Limit:         inv.Int("limit"),
		StartKeyDocID: inv.String("start"),
		IncludeDocs:   len(fields) > 0,
	}
	cols := []Column{{Title: "id"}, {Title: "rev"}}
	for _, f := range fields {
		cols = append(cols, Column{Title: f})
	}
	toRow := func(r couch.DocRow) Row {
		cells := []string{r.ID, r.Rev}
		for _, f := range fields {
			cells = append(cells, fieldString(r.Doc, f))
		}
		payload := r.Doc
		if payload == nil {
			payload = jsonObject("id", r.ID, "rev", r.Rev)
		}
		return Row{Cells: cells, JSON: payload}
	}

	if inv.Bool("all") {
		ch := make(chan couch.DocRow)
		errc := make(chan error, 1)
		go func() {
			defer close(ch)
			errc <- s.Client.AllDocsStream(ctx, t.Database, couch.AllDocsOptions{
				Partition:   t.Partition,
				Limit:       500,
				IncludeDocs: opts.IncludeDocs,
			}, func(r couch.DocRow) error {
				select {
				case ch <- r:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
		}()
		return Stream{Columns: cols, Next: func() (Row, bool, error) {
			r, ok := <-ch
			if !ok {
				return Row{}, false, <-errc
			}
			return toRow(r), true, nil
		}}, nil
	}

	page, err := s.Client.AllDocs(ctx, t.Database, opts)
	if err != nil {
		return nil, err
	}
	rows := Rows{Columns: cols}
	for _, r := range page.Rows {
		rows.Items = append(rows.Items, toRow(r))
	}
	if page.NextStartKeyDocID != "" {
		rows.Hint = fmt.Sprintf("more documents: ls %s --start %q", t.Path, page.NextStartKeyDocID)
	}
	return rows, nil
}

func lsDesignDoc(ctx context.Context, s *session.Session, t path.Target) (Result, error) {
	raw, err := s.Client.DesignDoc(ctx, t.Database, t.DocID)
	if err != nil {
		return nil, err
	}
	var ddoc struct {
		Views   map[string]json.RawMessage `json:"views"`
		Filters map[string]json.RawMessage `json:"filters"`
		Updates map[string]json.RawMessage `json:"updates"`
	}
	if err := json.Unmarshal(raw, &ddoc); err != nil {
		return nil, err
	}
	rows := Rows{Columns: []Column{{Title: "kind"}, {Title: "name"}}}
	add := func(kind string, m map[string]json.RawMessage) {
		names := make([]string, 0, len(m))
		for n := range m {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			rows.Items = append(rows.Items, Row{Cells: []string{kind, n}, JSON: jsonObject("kind", kind, "name", n)})
		}
	}
	add("view", ddoc.Views)
	add("filter", ddoc.Filters)
	add("update", ddoc.Updates)
	if len(rows.Items) == 0 {
		return Message{Text: fmt.Sprintf("%s defines no views, filters or updates.", t.Path)}, nil
	}
	return rows, nil
}

// Info returns the info command.
func Info() Command {
	return Command{
		Name:        "info",
		Summary:     "Show server or database information",
		Usage:       "[path]",
		MinArgs:     0,
		MaxArgs:     1,
		NeedsClient: true,
		Complete:    completePath,
		Run: func(ctx context.Context, s *session.Session, inv Invocation) (Result, error) {
			t, err := s.Resolve(inv.Arg(0))
			if err != nil {
				return nil, err
			}
			rows := Rows{Columns: []Column{{Title: "field"}, {Title: "value"}}}
			add := func(k, v string) {
				rows.Items = append(rows.Items, Row{Cells: []string{k, v}, JSON: jsonObject("field", k, "value", v)})
			}
			if t.Kind == path.KindServer {
				si, err := s.Client.ServerInfo(ctx)
				if err != nil {
					return nil, err
				}
				add("url", s.Client.URL())
				add("version", si.Version)
				add("vendor", si.Vendor)
				add("features", strings.Join(si.Features, ", "))
				return rows, nil
			}
			if t.Database == "" {
				return nil, Usagef("info", "%s is not a server or a database", t.Path)
			}
			di, err := s.Client.DatabaseInfo(ctx, t.Database)
			if err != nil {
				return nil, err
			}
			add("name", di.Name)
			add("documents", strconv.FormatInt(di.DocCount, 10))
			add("deleted", strconv.FormatInt(di.DeletedCount, 10))
			add("disk size", humanBytes(di.DiskSize))
			add("data size", humanBytes(di.ExternalSize))
			add("update seq", di.UpdateSeq)
			add("partitioned", strconv.FormatBool(di.Partitioned))
			add("shards", strconv.Itoa(di.Q))
			add("replicas", strconv.Itoa(di.N))
			return rows, nil
		},
	}
}

// completePath is a stub until Task 16 replaces it with live completion.
func completePath(_ context.Context, _ *session.Session, _ []string, _ string) []Candidate {
	return nil
}

// fieldString reads a top-level field out of a document as display text.
func fieldString(doc json.RawMessage, field string) string {
	if len(doc) == 0 {
		return ""
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(doc, &m); err != nil {
		return ""
	}
	raw, ok := m[field]
	if !ok {
		return ""
	}
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		return str
	}
	return string(raw)
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

// humanBytes formats a byte count for a table cell.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
