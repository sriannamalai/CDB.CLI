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
		Name:    "cd",
		Summary: "Change the current path",
		Example: `admin@localhost:5984:/> cd /movies
admin@localhost:5984:/movies> cd ..
admin@localhost:5984:/> cd movies/_design/app
admin@localhost:5984:/movies/_design/app>`,
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
		// Documents, design documents, views and attachments all hang off a
		// document, so reading it settles whether the database and the document
		// exist. A view and an attachment live inside that document rather than
		// at a URL of their own, so the last segment is checked against the
		// fetched body: without that, cd would accept any view or attachment
		// name at all.
		body, _, err := s.Client.GetDocument(ctx, t.Database, t.DocID, couch.GetOptions{})
		if err != nil {
			return err
		}
		switch t.Kind {
		case path.KindView:
			if !hasMember(body, "views", t.View) {
				return couch.NewError(404, "not_found",
					fmt.Sprintf("Design document %q defines no view %q.", t.DocID, t.View),
					"read", fmt.Sprintf("view %q in %q", t.View, t.Database))
			}
		case path.KindAttachment:
			if !hasMember(body, "_attachments", t.Attachment) {
				return couch.NewError(404, "not_found",
					fmt.Sprintf("Document %q has no attachment %q.", t.DocID, t.Attachment),
					"read", couch.AttachmentTarget(t.Database, t.DocID, t.Attachment))
			}
		}
		return nil
	}
}

// hasMember reports whether doc's top-level field is an object holding key. It
// is how cd checks a view against a design document's "views" and an attachment
// against a document's "_attachments".
func hasMember(doc json.RawMessage, field, key string) bool {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(doc, &top); err != nil {
		return false
	}
	raw, ok := top[field]
	if !ok {
		return false
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil {
		return false
	}
	_, ok = members[key]
	return ok
}

// Ls returns the ls command.
func Ls() Command {
	return Command{
		Name:    "ls",
		Aliases: []string{"list"},
		Summary: "List databases, documents, or the parts of a design document",
		Example: `$ cdb ls /
 NAME        | DOCS |     SIZE | PARTITIONED
-------------+------+----------+-------------
 _replicator |    0 | 124.4 KB | false
 _users      |    1 |  20.3 KB | false
 movies      |    2 |  24.4 KB | false

$ cdb ls /movies --limit 2
 ID          | REV
-------------+------------------------------------
 _design/app | 2-38f0b8babb35aaf96420b30d54bd4ca1
 tt0211915   | 1-fe587ae7ef952dbac249a78f49bb51e6
more documents: ls /movies --start "tt0245429"

$ cdb ls /movies --start tt0245429 --fields title,year`,
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
				return nil, Usagef("ls", "%s is %s %s, not something that can be listed. Use \"cat\" to read it.", t.Path, t.Kind.Article(), t.Kind)
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
			// --start composes with --all: it says where to resume, and the
			// two flags are documented on the same command. Limit is the
			// stream's page size here, not a row bound.
			errc <- s.Client.AllDocsStream(ctx, t.Database, couch.AllDocsOptions{
				Partition:     t.Partition,
				StartKeyDocID: opts.StartKeyDocID,
				Limit:         500,
				IncludeDocs:   opts.IncludeDocs,
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
		Name:    "info",
		Summary: "Show server or database information",
		Example: `$ cdb info /movies
 FIELD       | VALUE
-------------+-------------
 name        | movies
 documents   | 2
 deleted     | 0
 disk size   | 24.4 KB
 data size   | 87 B
 partitioned | false
 shards      | 2
 replicas    | 1

$ cdb info /`,
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
				// Only when it differs, so the common case stays uncluttered.
				// Validation guarantees the value carries no credentials, so
				// it is safe to render.
				if r := s.Client.ReplicationURL(); r != s.Client.URL() {
					add("replication url", r)
				}
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

// completePath keeps the short internal name every command already refers to.
func completePath(ctx context.Context, s *session.Session, args []string, cur string) []Candidate {
	return CompletePath(ctx, s, args, cur)
}

// completionPageSize bounds a completion lookup. Completion runs while the
// line editor owns the terminal, so it asks for one small page and never
// pages further.
const completionPageSize = 50

// idSentinel is the highest code point CouchDB sorts document ids against, so
// "prefix" to "prefix\ufff0" is the whole prefix range. It is what turns a
// startkey/endkey pair into a prefix query, with no skip involved.
const idSentinel = "\ufff0"

// CompletePath completes database, document, design-document and view names
// for a virtual path prefix.
func CompletePath(ctx context.Context, s *session.Session, _ []string, cur string) []Candidate {
	if !s.Connected() {
		return nil
	}
	// Split the prefix into the directory part that already exists and the
	// partial last segment being typed.
	dir, partial := splitPathPrefix(cur)
	// The line carries percent-encoded segments — path.Encode is what put them
	// there — while CouchDB keys, and the names it hands back, are raw text.
	// Every comparison and key bound below therefore works on the decoded
	// prefix, and only the candidate values are encoded again. A half-typed
	// escape ("%" with nothing after it) does not decode; the raw text is the
	// best guess then.
	if decoded, err := path.Decode(partial); err == nil {
		partial = decoded
	}
	join := func(name string) string {
		if dir == "" {
			return name
		}
		if strings.HasSuffix(dir, "/") {
			return dir + name
		}
		return dir + "/" + name
	}

	// "/db/_design/app/_view/" is not a resolvable path on its own, so trim the
	// trailing _view/ and complete view names against the design document.
	if trimmed := strings.TrimSuffix(dir, "_view/"); trimmed != dir {
		base, err := path.Resolve(s.Path(), trimmed)
		if err != nil || base.Kind != path.KindDesignDoc {
			return nil
		}
		return viewCandidates(ctx, s, base, partial, func(n string) string { return dir + path.Encode(n) })
	}

	base, err := path.Resolve(s.Path(), dir)
	if err != nil {
		return nil
	}
	switch base.Kind {
	case path.KindServer:
		names, err := s.Cache().Databases(ctx, s.Client)
		if err != nil {
			return nil
		}
		var out []Candidate
		for _, n := range names {
			if strings.HasPrefix(n, partial) {
				out = append(out, Candidate{Value: join(path.Encode(n)), Display: n, Tag: "databases"})
			}
		}
		return out

	case path.KindDatabase, path.KindPartition:
		page, err := s.Client.AllDocs(ctx, base.Database, couch.AllDocsOptions{
			Partition:     base.Partition,
			Limit:         completionPageSize,
			StartKeyDocID: partial,
			EndKeyDocID:   partial + idSentinel,
		})
		if err != nil {
			return nil
		}
		var out []Candidate
		for _, r := range page.Rows {
			if !strings.HasPrefix(r.ID, partial) {
				continue
			}
			out = append(out, Candidate{Value: join(encodeDocID(r.ID)), Display: r.ID, Description: r.Rev, Tag: "documents"})
		}
		return out

	case path.KindDesignDoc:
		// Offer the _view segment before the view names themselves.
		if partial != "_view" && strings.HasPrefix("_view", partial) {
			return []Candidate{{Value: join("_view"), Display: "_view", Tag: "views"}}
		}
		return viewCandidates(ctx, s, base, partial, join)

	default:
		return nil
	}
}

// viewCandidates lists the view names of a design document that start with
// partial, mapping each through join to build the completion value.
func viewCandidates(ctx context.Context, s *session.Session, base path.Target, partial string, join func(string) string) []Candidate {
	raw, err := s.Client.DesignDoc(ctx, base.Database, base.DocID)
	if err != nil {
		return nil
	}
	var ddoc struct {
		Views map[string]json.RawMessage `json:"views"`
	}
	if err := json.Unmarshal(raw, &ddoc); err != nil {
		return nil
	}
	names := make([]string, 0, len(ddoc.Views))
	for n := range ddoc.Views {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []Candidate
	for _, n := range names {
		if strings.HasPrefix(n, partial) {
			out = append(out, Candidate{Value: join(path.Encode(n)), Display: n, Tag: "views"})
		}
	}
	return out
}

// encodeDocID escapes a document id for use as the tail of a virtual path. A
// design document keeps its "_design/" prefix as a real separator: the rest of
// cdb speaks /db/_design/app, and percent-escaping the slash would produce
// /db/_design%2Fapp, which resolves to a plain document instead.
func encodeDocID(id string) string {
	const designPrefix = "_design/"
	if strings.HasPrefix(id, designPrefix) {
		return designPrefix + path.Encode(strings.TrimPrefix(id, designPrefix))
	}
	return path.Encode(id)
}

// splitPathPrefix separates the settled directory part of a path prefix from
// the partial segment the cursor is inside.
func splitPathPrefix(cur string) (dir, partial string) {
	i := strings.LastIndex(cur, "/")
	if i < 0 {
		return "", cur
	}
	return cur[:i+1], cur[i+1:]
}

// positionalArgs drops flag words, and the separate word a non-boolean flag
// consumes, from a partially typed argument list. Without it the "5" in
// "find --limit 5 /mydb" would be read as a path.
func positionalArgs(fs *pflag.FlagSet, args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return append(out, args[i+1:]...)
		case strings.HasPrefix(a, "--"):
			name, _, attached := strings.Cut(a[2:], "=")
			if !attached && takesValue(fs.Lookup(name)) {
				i++
			}
		case strings.HasPrefix(a, "-") && a != "-":
			// Only a lone shorthand takes the next word: in a cluster, or with
			// anything attached, pflag reads the value out of the word itself.
			body, _, attached := strings.Cut(a[1:], "=")
			if !attached && len(body) == 1 && takesValue(fs.ShorthandLookup(body)) {
				i++
			}
		default:
			out = append(out, a)
		}
	}
	return out
}

// takesValue reports whether a flag consumes the word after it. Boolean flags
// do not; every other type does. An unknown flag is treated as boolean, the
// safer guess while the line is still being typed: it leaves the next word
// available as a path instead of swallowing it.
func takesValue(f *pflag.Flag) bool {
	return f != nil && f.Value.Type() != "bool"
}

// CompleteFields completes document field names sampled from the database the
// session or one of the arguments points at.
func CompleteFields(ctx context.Context, s *session.Session, args []string, cur string) []Candidate {
	if !s.Connected() {
		return nil
	}
	target, err := s.Resolve("")
	if err != nil {
		return nil
	}
	// find is the only command that completes field names, so its own flag set
	// is what says which of the typed words are flag values rather than paths.
	for _, a := range positionalArgs(NewFlagSet(Find()), args) {
		if t, terr := s.Resolve(a); terr == nil && t.Database != "" {
			target = t
			break
		}
	}
	if target.Database == "" {
		return nil
	}
	fields, err := s.Cache().Fields(ctx, s.Client, target.Database)
	if err != nil {
		return nil
	}
	var out []Candidate
	for _, f := range fields {
		if strings.HasPrefix(f, cur) {
			out = append(out, Candidate{Value: f, Tag: "fields"})
		}
	}
	return out
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
