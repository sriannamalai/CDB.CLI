package command

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/pflag"
	"github.com/sriannamalai/CDB.CLI/internal/couch"
	"github.com/sriannamalai/CDB.CLI/internal/path"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// Find returns the find command.
func Find() Command {
	return Command{
		Name:        "find",
		Summary:     "Run a Mango query",
		Usage:       "[path] [selector-json]",
		MinArgs:     0,
		MaxArgs:     2,
		NeedsClient: true,
		Complete:    completePath,
		Flags: func(fs *pflag.FlagSet) {
			fs.StringSlice("fields", nil, "fields to return")
			fs.StringSlice("sort", nil, "sort keys, as field:asc or field:desc")
			fs.Int("limit", 25, "documents per page")
			fs.String("bookmark", "", "continue from a previous page")
			fs.Bool("explain", false, "show the query plan instead of running it")
			fs.String("use-index", "", "design document, or design document and index name, to use")
		},
		Run: func(ctx context.Context, s *session.Session, inv Invocation) (Result, error) {
			target, selectorArg, err := splitFindArgs(s, inv)
			if err != nil {
				return nil, err
			}
			if target.Database == "" {
				return nil, Usagef("find", "find needs a database; cd into one or pass a path")
			}
			selector, err := resolveSelector(s, selectorArg)
			if err != nil {
				return nil, err
			}
			sort, err := parseSort(inv.StringSlice("sort"))
			if err != nil {
				return nil, err
			}
			opts := couch.FindOptions{
				Selector:  selector,
				Fields:    inv.StringSlice("fields"),
				Sort:      sort,
				Limit:     inv.Int("limit"),
				Bookmark:  inv.String("bookmark"),
				Partition: target.Partition,
			}
			if idx := inv.String("use-index"); idx != "" {
				opts.UseIndex = strings.Split(idx, ",")
			}
			if inv.Bool("explain") {
				plan, err := s.Client.Explain(ctx, target.Database, opts)
				if err != nil {
					return nil, err
				}
				return Document{JSON: plan}, nil
			}
			page, err := s.Client.Find(ctx, target.Database, opts)
			if err != nil {
				return nil, err
			}
			rows := Rows{Columns: []Column{{Title: "id"}, {Title: "document"}}}
			for _, d := range page.Docs {
				rows.Items = append(rows.Items, Row{Cells: []string{fieldString(d, "_id"), summarise(d)}, JSON: d})
			}
			// Commands never print (see Global Constraints); the Mango
			// "no matching index" warning rides along in the Hint instead.
			var hints []string
			if page.Warning != "" {
				hints = append(hints, "note: "+page.Warning)
			}
			// CouchDB returns a bookmark on every _find, whether or not more
			// documents exist, so a full page is the only signal that there
			// is a next one. With no limit there is no full page to compare
			// against and no paging to suggest.
			if page.Bookmark != "" && opts.Limit > 0 && len(page.Docs) == opts.Limit {
				hints = append(hints, fmt.Sprintf("more documents: find %s --bookmark %q", target.Path, page.Bookmark))
			}
			rows.Hint = strings.Join(hints, "\n")
			return rows, nil
		},
	}
}

// splitFindArgs works out which argument is a path and which is a selector.
func splitFindArgs(s *session.Session, inv Invocation) (path.Target, string, error) {
	a0, a1 := inv.Arg(0), inv.Arg(1)
	switch {
	case a1 != "":
		t, err := s.Resolve(a0)
		return t, a1, err
	case strings.HasPrefix(strings.TrimSpace(a0), "{"):
		t, err := s.Resolve("")
		return t, a0, err
	case a0 != "":
		t, err := s.Resolve(a0)
		return t, "", err
	default:
		t, err := s.Resolve("")
		return t, "", err
	}
}

// resolveSelector accepts a bare selector or a full Mango query object, and
// falls back to the guided builder on a terminal.
func resolveSelector(s *session.Session, arg string) (json.RawMessage, error) {
	if arg == "" {
		if !s.Prefs.Interactive {
			return nil, Usagef("find", "no selector given. Pass one as JSON, for example: find '{\"name\":\"alice\"}'")
		}
		return buildSelector(s)
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(arg), &probe); err != nil {
		return nil, Usagef("find", "the selector is not a JSON object: %v", err)
	}
	if inner, ok := probe["selector"]; ok {
		return inner, nil
	}
	return json.RawMessage(arg), nil
}

// buildSelector walks the operator through field, operator and value, one
// condition at a time. It is one of the few places a command may write to the
// session's own stdout, and it reads through the session's shared line reader
// so that a later prompt still sees the bytes this one buffered.
func buildSelector(s *session.Session) (json.RawMessage, error) {
	r := s.Reader()
	ask := func(label string) (string, error) {
		fmt.Fprintf(s.Stdout, "%s: ", label)
		line, err := r.ReadString('\n')
		if err != nil && line == "" {
			return "", err
		}
		return strings.TrimSpace(line), nil
	}
	conditions := map[string]any{}
	for {
		field, err := ask("Field")
		if err != nil {
			return nil, err
		}
		if field == "" {
			break
		}
		op, err := ask("Operator (=, !=, <, <=, >, >=, in, exists)")
		if err != nil {
			return nil, err
		}
		value, err := ask("Value")
		if err != nil {
			return nil, err
		}
		cond, err := mangoCondition(op, value)
		if err != nil {
			return nil, err
		}
		conditions[field] = cond
		more, err := ask("Add another condition? [y/N]")
		if err != nil {
			return nil, err
		}
		if strings.ToLower(more) != "y" && strings.ToLower(more) != "yes" {
			break
		}
	}
	b, err := json.Marshal(conditions)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(s.Stdout, "Selector: %s\n", b)
	return b, nil
}

// mangoCondition turns a friendly operator and a text value into Mango syntax.
func mangoCondition(op, value string) (any, error) {
	typed := typedValue(value)
	switch op {
	case "=", "==", "eq":
		return typed, nil
	case "!=", "ne":
		return map[string]any{"$ne": typed}, nil
	case "<", "lt":
		return map[string]any{"$lt": typed}, nil
	case "<=", "lte":
		return map[string]any{"$lte": typed}, nil
	case ">", "gt":
		return map[string]any{"$gt": typed}, nil
	case ">=", "gte":
		return map[string]any{"$gte": typed}, nil
	case "in":
		parts := strings.Split(value, ",")
		vals := make([]any, 0, len(parts))
		for _, p := range parts {
			vals = append(vals, typedValue(strings.TrimSpace(p)))
		}
		return map[string]any{"$in": vals}, nil
	case "exists":
		return map[string]any{"$exists": value != "false"}, nil
	default:
		return nil, Usagef("find", "unknown operator %q; use =, !=, <, <=, >, >=, in or exists", op)
	}
}

// typedValue converts a text value to a number or bool when it looks like one.
func typedValue(v string) any {
	var out any
	if err := json.Unmarshal([]byte(v), &out); err == nil {
		switch out.(type) {
		case float64, bool, nil:
			return out
		}
	}
	return v
}

// parseSort turns "field:desc" strings into Mango sort clauses.
func parseSort(specs []string) ([]json.RawMessage, error) {
	var out []json.RawMessage
	for _, spec := range specs {
		field, dir := spec, "asc"
		if i := strings.LastIndex(spec, ":"); i > 0 {
			field, dir = spec[:i], spec[i+1:]
		}
		if dir != "asc" && dir != "desc" {
			return nil, Usagef("find", "sort direction must be asc or desc, got %q", dir)
		}
		b, err := json.Marshal(map[string]string{field: dir})
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

// summarise renders a compact one-line preview of a document. It truncates on
// a rune boundary: cutting a byte slice mid-rune produces mojibake.
func summarise(doc json.RawMessage) string {
	const max = 80
	compact, err := json.Marshal(json.RawMessage(doc))
	if err != nil {
		compact = doc
	}
	r := []rune(string(compact))
	if len(r) > max {
		return string(r[:max-1]) + "…"
	}
	return string(r)
}

// Query returns the query command.
func Query() Command {
	return Command{
		Name:        "query",
		Summary:     "Run a map/reduce view",
		Usage:       "<view-path>",
		MinArgs:     1,
		MaxArgs:     1,
		NeedsClient: true,
		Complete:    completePath,
		Flags: func(fs *pflag.FlagSet) {
			fs.String("key", "", "exact key, as JSON")
			fs.String("startkey", "", "start key, as JSON")
			fs.String("endkey", "", "end key, as JSON")
			fs.Bool("reduce", false, "run the reduce function")
			fs.Int("group-level", 0, "group reduce results to this key depth")
			fs.Bool("include-docs", false, "include the full documents")
			fs.Int("limit", 25, "rows per page")
			fs.Bool("descending", false, "reverse the key order")
		},
		Run: func(ctx context.Context, s *session.Session, inv Invocation) (Result, error) {
			t, err := s.Resolve(inv.Arg(0))
			if err != nil {
				return nil, err
			}
			if t.Kind != path.KindView {
				return nil, Usagef("query", "%s is a %s; a view path looks like /db/_design/app/_view/name", t.Path, t.Kind)
			}
			opts := couch.ViewOptions{
				Limit:       inv.Int("limit"),
				Descending:  inv.Bool("descending"),
				IncludeDocs: inv.Bool("include-docs"),
			}
			if raw := inv.String("key"); raw != "" {
				opts.Key = json.RawMessage(raw)
			}
			if raw := inv.String("startkey"); raw != "" {
				opts.StartKey = json.RawMessage(raw)
			}
			if raw := inv.String("endkey"); raw != "" {
				opts.EndKey = json.RawMessage(raw)
			}
			if inv.Changed("reduce") {
				v := inv.Bool("reduce")
				opts.Reduce = &v
			}
			if inv.Changed("group-level") {
				v := inv.Int("group-level")
				opts.GroupLevel = &v
			}
			page, err := s.Client.Query(ctx, t.Database, t.DocID, t.View, opts)
			if err != nil {
				return nil, err
			}
			rows := Rows{Columns: []Column{{Title: "key"}, {Title: "id"}, {Title: "value"}}}
			for _, r := range page.Rows {
				rows.Items = append(rows.Items, Row{
					Cells: []string{string(r.Key), r.ID, string(r.Value)},
					JSON:  mustJSON(map[string]any{"key": r.Key, "id": r.ID, "value": r.Value, "doc": r.Doc}),
				})
			}
			if page.NextStartKeyDocID != "" {
				rows.Hint = fmt.Sprintf("more rows: query %s --startkey %q", t.Path, string(page.NextStartKey))
			}
			return rows, nil
		},
	}
}
