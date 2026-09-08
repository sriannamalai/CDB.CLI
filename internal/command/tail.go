package command

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/pflag"
	"github.com/sriannamalai/CDB.CLI/internal/couch"
	"github.com/sriannamalai/CDB.CLI/internal/path"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// tailDefaultLimit is how many changes one normal-feed page reads.
const tailDefaultLimit = 25

// tailDefaultHeartbeatMS is how often the server is asked for a keep-alive on
// the continuous feed. Without one, a quiet database looks the same as a dead
// connection.
const tailDefaultHeartbeatMS = 30000

// tailSeqHint explains the shortened SEQ column, once, under the table. It is
// only ever seen in table output: the JSON renderers return before a stream's
// hint is written, and their sequences are whole anyway.
const tailSeqHint = "Sequences are shortened in the table; use --json for the full value and --since."

// tailDetails is the long help for tail.
const tailDetails = `Reads the _changes feed of one database, newest changes last. Without --follow
it reads one page and stops; with --follow it opens the continuous feed and
keeps reading until you stop it, reconnecting on its own if the feed drops.

--since takes an update sequence, which is what the paging hint and "info"
report. It defaults to the beginning of the feed, or to "now" under --follow,
so a follow shows what happens from the moment you start it.

A CouchDB update sequence is a long opaque string, so the table shows only its
leading number, which is the part worth reading; a sequence that number cannot
be taken from is shown whole. The full value is what --json prints and what
--since takes, so copy it from --json or from the paging hint, never from the
table.

CouchDB has no partition-scoped changes feed, so tail takes a database path.`

// Tail returns the tail command.
func Tail() Command {
	return Command{
		Name:    "tail",
		Summary: "Read a database's changes feed",
		Example: `$ cdb tail /movies --limit 3
 SEQ | ID        | REV                                | DELETED
-----+-----------+------------------------------------+---------
 3   | tt0211915 | 1-967a00dff5e02add41819138abb3284d | false
 4   | tt2543164 | 2-7051cbe5c8faecd085a3fa619e6e6337 | false
 5   | tt0245429 | 3-825cb35de44c433bfb2df415563a19de | true
more changes: tail /movies --since "5-g1AAAAFV"
Sequences are shortened in the table; use --json for the full value and --since.

$ cdb tail /movies --since "5-g1AAAAFV" --include-docs
$ cdb tail /movies --follow`,
		Usage:       "[path]",
		Details:     tailDetails,
		MinArgs:     0,
		MaxArgs:     1,
		NeedsClient: true,
		Complete:    completePath,
		Flags: func(fs *pflag.FlagSet) {
			fs.String("since", "", "update sequence to start from")
			fs.Int("limit", tailDefaultLimit, "changes to read; 0 reads to the end of the feed")
			fs.Bool("follow", false, "keep reading as changes arrive")
			fs.Bool("include-docs", false, "fetch each changed document with the change")
			fs.String("filter", "", "design document filter, as ddoc/name")
			fs.Int("heartbeat", tailDefaultHeartbeatMS, "milliseconds between server heartbeats, with --follow")
		},
		Run: func(ctx context.Context, s *session.Session, inv Invocation) (Result, error) {
			t, err := s.Resolve(inv.Arg(0))
			if err != nil {
				return nil, err
			}
			if t.Kind == path.KindPartition {
				return nil, Usagef("tail", "CouchDB has no partition-scoped changes feed; tail /%s reads the whole database.", t.Database)
			}
			if t.Kind != path.KindDatabase {
				return nil, Usagef("tail", "%s is %s %s; tail takes a database path.", t.Path, t.Kind.Article(), t.Kind)
			}
			follow := inv.Bool("follow")
			opts, err := tailOptions(inv, follow)
			if err != nil {
				return nil, err
			}
			includeDocs := opts.IncludeDocs
			cols := tailColumns(includeDocs)
			if follow {
				return tailFollow(ctx, s, t, opts, cols, includeDocs)
			}

			page, err := s.Client.Changes(ctx, t.Database, opts)
			if err != nil {
				return nil, err
			}
			rows := page.Rows
			i := 0
			st := Stream{Columns: cols, Next: func() (Row, bool, error) {
				if i >= len(rows) {
					return Row{}, false, nil
				}
				r := rows[i]
				i++
				return tailRow(r, includeDocs), true, nil
			}}
			// A page that came back exactly full is the only signal that more
			// changes exist. With no limit there is no full page to compare
			// against and nothing to continue from. The hint carries the whole
			// sequence, because it is what the operator pastes back into
			// --since, and says once that the column above it does not.
			if opts.Limit > 0 && len(rows) == opts.Limit {
				st.Hint = fmt.Sprintf("more changes: tail %s --since %q\n%s", t.Path, page.LastSeq, tailSeqHint)
			}
			return st, nil
		},
	}
}

// tailOptions validates the flags and builds the client options. Every failure
// is a usage error, so a mistyped flag exits 2 rather than reaching the server.
func tailOptions(inv Invocation, follow bool) (couch.ChangesOptions, error) {
	filter := inv.String("filter")
	if filter != "" && strings.Count(filter, "/") != 1 {
		return couch.ChangesOptions{}, Usagef("tail", "--filter takes a design document and a filter name, as \"app/by_type\".")
	}
	if inv.Changed("heartbeat") && !follow {
		return couch.ChangesOptions{}, Usagef("tail", "--heartbeat only applies to --follow; the normal feed has no idle period to keep alive.")
	}
	if inv.Changed("limit") && follow {
		return couch.ChangesOptions{}, Usagef("tail", "--limit does not apply to --follow, which reads until you stop it.")
	}
	limit := inv.Int("limit")
	if limit < 0 {
		return couch.ChangesOptions{}, Usagef("tail", "--limit cannot be negative.")
	}
	heartbeat := inv.Int("heartbeat")
	if heartbeat < 0 {
		return couch.ChangesOptions{}, Usagef("tail", "--heartbeat cannot be negative.")
	}
	since := inv.String("since")
	if follow && since == "" {
		// A follow shows what happens from now on. Starting at 0 would replay
		// the whole database first, which is what the normal feed is for.
		since = "now"
	}
	opts := couch.ChangesOptions{
		Since: since,
		// One row per change, not one per leaf revision: tail reports what
		// happened, and backup is the command that needs every leaf.
		Style:       couch.StyleMainOnly,
		IncludeDocs: inv.Bool("include-docs"),
		Filter:      filter,
	}
	if follow {
		opts.HeartbeatMS = heartbeat
	} else {
		opts.Limit = limit
	}
	return opts, nil
}

// tailColumns is the column set both feeds render.
func tailColumns(includeDocs bool) []Column {
	cols := []Column{{Title: "seq"}, {Title: "id"}, {Title: "rev"}, {Title: "deleted"}}
	if includeDocs {
		cols = append(cols, Column{Title: "doc"})
	}
	return cols
}

// shortSeq is a sequence as the table shows it: the number CouchDB puts before
// the opaque body, which is the only part of it a person reads. A whole
// sequence is around a hundred characters and would crowd every other column
// off the line. Anything not shaped "<digits>-<rest>" is returned whole rather
// than guessed at, and Row.JSON always keeps the full value.
func shortSeq(seq string) string {
	i := strings.IndexByte(seq, '-')
	if i <= 0 {
		return seq
	}
	for _, c := range []byte(seq[:i]) {
		if c < '0' || c > '9' {
			return seq
		}
	}
	return seq[:i]
}

// tailRow renders one change. Row.JSON is the machine-readable contract:
// {"seq":…,"id":…,"rev":…,"deleted":…} with "doc" present only under
// --include-docs, so "tail --json" emits one change per line. "seq" is always
// the full sequence; only the table cell is shortened.
func tailRow(r couch.ChangeRow, includeDocs bool) Row {
	rev := ""
	if len(r.Revs) > 0 {
		rev = r.Revs[0]
	}
	cells := []string{shortSeq(r.Seq), r.ID, rev, strconv.FormatBool(r.Deleted)}
	payload := map[string]any{"seq": r.Seq, "id": r.ID, "rev": rev, "deleted": r.Deleted}
	if includeDocs {
		doc := ""
		if len(r.Doc) > 0 {
			doc = summarise(r.Doc)
		}
		cells = append(cells, doc)
		payload["doc"] = r.Doc
	}
	return Row{Cells: cells, JSON: mustJSON(payload)}
}

// tailFollow opens the continuous feed. Task 5 implements the reconnect loop;
// until then this is unreachable in a released binary, and is an internal
// error rather than a usage sentence so it can never read as advice.
func tailFollow(ctx context.Context, s *session.Session, t path.Target, opts couch.ChangesOptions, cols []Column, includeDocs bool) (Result, error) {
	return nil, errors.New("tail: --follow is wired in the next commit")
}
