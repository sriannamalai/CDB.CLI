package command

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/spf13/pflag"
	"github.com/sriannamalai/CDB.CLI/internal/couch"
	"github.com/sriannamalai/CDB.CLI/internal/path"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// Search returns the search command.
func Search() Command {
	return Command{
		Name:    "search",
		Summary: "Run a full-text query against a search index",
		Example: `$ cdb search /movies/_design/app/_search/by_title 'title:arrival'
 ID | SCORE | FIELDS
----+-------+---------------------------
 m1 | 1.25  | {"title":"Arrival"}

$ cdb search /movies/_design/app/_nouveau/by_body 'alien AND linguist' --include-docs
$ cdb search /movies/_design/app/_search/by_title 'title:a*' --limit 10 --counts genre`,
		Usage:       "<path> <query>",
		MinArgs:     2,
		MaxArgs:     2,
		NeedsClient: true,
		Details: "The query is Lucene syntax, sent to the server as it stands. --sort and --ranges are\n" +
			"passed through verbatim too, because their grammar belongs to the backend. Paging is\n" +
			"by bookmark: run the command again with the --bookmark the last page printed.",
		Complete: completePath,
		Flags: func(fs *pflag.FlagSet) {
			fs.Int("limit", 25, "results per page")
			fs.String("bookmark", "", "continue from a previous page")
			fs.String("sort", "", "sort specification, passed to the server as given")
			fs.Bool("include-docs", false, "include each matching document")
			fs.StringSlice("counts", nil, "fields to count facet values for")
			fs.String("ranges", "", "range facets, as a JSON object")
			fs.StringArray("drilldown", nil, "restrict to a facet value, as field:value; repeatable")
		},
		Run: func(ctx context.Context, s *session.Session, inv Invocation) (Result, error) {
			t, err := s.Resolve(inv.Arg(0))
			if err != nil {
				return nil, err
			}
			if t.Kind != path.KindSearch {
				return nil, Usagef("search", "%s is %s %s; search needs an index path such as /db/_design/app/_search/<index> or /db/_design/app/_nouveau/<index>.",
					t.Path, t.Kind.Article(), t.Kind)
			}
			query := inv.Arg(1)
			if strings.TrimSpace(query) == "" {
				return nil, Usagef("search", "the query is empty; pass a Lucene query such as 'title:arrival', or '*:*' for every document")
			}
			opts := couch.SearchOptions{
				Query:       query,
				Limit:       inv.Int("limit"),
				Bookmark:    inv.String("bookmark"),
				Sort:        inv.String("sort"),
				IncludeDocs: inv.Bool("include-docs"),
				Counts:      inv.StringSlice("counts"),
			}
			if raw := inv.String("ranges"); raw != "" {
				if !json.Valid([]byte(raw)) {
					return nil, Usagef("search", "--ranges is not valid JSON")
				}
				opts.Ranges = json.RawMessage(raw)
			}
			for _, d := range inv.StringArray("drilldown") {
				field, value, ok := strings.Cut(d, ":")
				if !ok || field == "" {
					return nil, Usagef("search", "--drilldown takes field:value; %q has no \":\"", d)
				}
				opts.Drilldown = append(opts.Drilldown, []string{field, value})
			}

			page, err := s.Client.Search(ctx, t, opts)
			if err != nil {
				return nil, searchSentence(ctx, s, t, err)
			}

			cols := []Column{{Title: "id"}, {Title: "score"}, {Title: "fields"}}
			if opts.IncludeDocs {
				cols = append(cols, Column{Title: "document"})
			}
			rows := Rows{Columns: cols}
			for _, r := range page.Rows {
				fields := compactJSON(r.Fields)
				cells := []string{r.ID, scoreCell(r.Order), summarise(fields)}
				if opts.IncludeDocs {
					cells = append(cells, summarise(compactJSON(r.Doc)))
				}
				rows.Items = append(rows.Items, Row{Cells: cells, JSON: searchRowJSON(r, opts.IncludeDocs)})
			}

			var hints []string
			if len(page.Counts) > 0 {
				raw := compactJSON(page.Counts)
				hints = append(hints, "counts: "+string(raw))
				rows.Extra = setExtra(rows.Extra, "counts", raw)
			}
			if len(page.Ranges) > 0 {
				raw := compactJSON(page.Ranges)
				hints = append(hints, "ranges: "+string(raw))
				rows.Extra = setExtra(rows.Extra, "ranges", raw)
			}
			// Both backends return a bookmark whether or not more hits exist,
			// so a full page is the only signal that there is a next one —
			// exactly the reasoning find's hint follows.
			if page.Bookmark != "" && opts.Limit > 0 && len(page.Rows) == opts.Limit {
				hints = append(hints, fmt.Sprintf("more results: search %s %q --bookmark %q", t.Path, query, page.Bookmark))
			}
			rows.Hint = strings.Join(hints, "\n")
			return rows, nil
		},
	}
}

// searchSentence turns a failed search into the error sentence spec section 5.3
// names. It is the command's job rather than internal/render's for the middle
// case: a 404 from _nouveau is "missing" both when Nouveau is switched off and
// when the design document is not there, and only a second request can tell
// them apart.
func searchSentence(ctx context.Context, s *session.Session, t path.Target, err error) error {
	ce, ok := couch.AsError(err)
	if !ok {
		return err
	}
	name := strings.TrimPrefix(t.DocID, "_design/") + "/" + t.Index
	switch {
	case t.Backend == path.BackendClouseau && missingClouseau(ce):
		return Errorf(err, "This server has no search service running; Clouseau must be installed and started for _search indexes.")
	case ce.Status == http.StatusNotFound && t.Backend == path.BackendNouveau:
		// Ask whether the design document exists. If it does not, its own 404
		// is the actionable answer and it is returned as it stands.
		if _, derr := s.Client.DesignDoc(ctx, t.Database, t.DocID); derr != nil {
			return derr
		}
		// The reason is not read: a 3.5 with Nouveau switched off says
		// "missing" and a server before 3.4 says "Document is missing
		// attachment", because there is no _nouveau route and the request fell
		// through to the attachment handler. Both mean the same thing to the
		// operator, and the design document has already been ruled out.
		sentence := fmt.Sprintf("Nouveau is not enabled on this server, or %q is not a nouveau index.", name)
		if hint := nouveauVersionHint(ctx, s); hint != "" {
			sentence += " " + hint
		}
		return Errorf(err, "%s", sentence)
	case ce.Status == http.StatusBadRequest:
		return Errorf(err, "The search query was rejected: %s.", strings.TrimRight(ce.Reason, "."))
	}
	return err
}

// missingClouseau reports whether a failed _search query means no search
// service is running. 3.5 answers 503 "Search is not available", which is the
// clean case. Older servers do not: the call to a process that is not there
// simply fails, and the operator gets a 500 carrying an Erlang badarg or
// gen_server tuple. The node name is inside that tuple, so it is the tuple
// that is read -- an unrelated 500 keeps the server's own error, because a
// fault in a search that works is not a search service that is absent.
func missingClouseau(e *couch.Error) bool {
	if e.Status == http.StatusServiceUnavailable {
		return true
	}
	return e.Status == http.StatusInternalServerError &&
		strings.Contains(strings.ToLower(e.Name+" "+e.Reason), "clouseau")
}

// nouveauVersionHint is the clause added to the Nouveau sentence when the
// server is too old to have the endpoint at all: CouchDB gained _nouveau in
// 3.4, so 3.0 through 3.3 answer 404 for every nouveau path whatever the
// design document defines, and "not enabled" alone would send the operator to
// a configuration file that has nothing to switch on. It mirrors
// jwtVersionHint, which says the same thing about the JWT handler 3.1 added.
//
// The version costs one request, on the error path only, and a server that
// will not answer it simply gets no clause: a hint that cannot be confirmed is
// worse than none.
func nouveauVersionHint(ctx context.Context, s *session.Session) string {
	info, err := s.Client.ServerInfo(ctx)
	if err != nil || !beforeNouveau(info.Version) {
		return ""
	}
	return "Nouveau needs CouchDB 3.4 or later; this server is " + info.Version + "."
}

// beforeNouveau reports whether a reported version is a supported server from
// before _nouveau existed. An unparseable or unexpected version is treated as
// new enough, so the clause is never printed on a guess.
func beforeNouveau(version string) bool {
	major, minor, ok := strings.Cut(version, ".")
	if !ok || major != "3" {
		return false
	}
	minor, _, _ = strings.Cut(minor, ".")
	switch minor {
	case "0", "1", "2", "3":
		return true
	}
	return false
}

// setExtra lazily builds the Extra map.
func setExtra(m map[string]json.RawMessage, k string, v json.RawMessage) map[string]json.RawMessage {
	if m == nil {
		m = map[string]json.RawMessage{}
	}
	m[k] = v
	return m
}

// scoreCell renders the first sort value. It is the relevance score for an
// unsorted query, which is the common case, and whatever the first sort field
// holds otherwise — so it is formatted as a number when it is one and printed
// as it stands when it is not.
func scoreCell(order []any) string {
	if len(order) == 0 {
		return ""
	}
	switch v := order[0].(type) {
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64)
	case string:
		return v
	case bool:
		return strconv.FormatBool(v)
	case nil:
		return ""
	default:
		return string(compactJSON(v))
	}
}

// searchRowJSON is the normalised row, which is what --json and the gojq
// filter see. The cells are a rendering of it and never the other way round.
func searchRowJSON(r couch.SearchRow, withDoc bool) json.RawMessage {
	m := map[string]any{"id": r.ID, "order": r.Order, "fields": r.Fields}
	if withDoc {
		m["doc"] = r.Doc
	}
	return compactJSON(m)
}

// compactJSON marshals a value for a cell or a JSON member, falling back to
// "null" rather than failing a command over a value the server sent.
func compactJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("null")
	}
	return b
}
