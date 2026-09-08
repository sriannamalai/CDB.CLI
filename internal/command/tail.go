package command

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

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
report. It defaults to the beginning of the feed, or to the database's current
sequence under --follow, so a follow shows what happens from the moment you
start it.

A dropped feed is reopened from the last change it showed you, backing off 1s,
2s, 4s, 8s, 16s and then every 30s, and saying so on stderr each time. A
deleted database or a rejected token ends the command instead, and so does a
single change larger than 4 MiB, which no reconnect could get past: re-run
without --include-docs.

A CouchDB update sequence is a long opaque string, so the table shows only its
leading number followed by an ellipsis, which is the part worth reading; a
sequence that number cannot be taken from is shown whole, unmarked. The full
value is what --json prints and what --since takes, so copy it from --json or
from the paging hint and never from the table: CouchDB accepts a bare number
in --since and answers it by replaying the feed from the beginning.

CouchDB has no partition-scoped changes feed, so tail takes a database path.`

// Tail returns the tail command.
func Tail() Command { return tailCommand(tailSleep) }

// tailCommand builds tail around a sleeper, which is how the reconnect backoff
// is driven in tests without waiting out the real schedule. Injecting it here,
// per command, rather than through a package variable keeps two tests from
// writing the same sleeper while another test's follow goroutine reads it.
func tailCommand(sleep tailSleeper) Command {
	return Command{
		Name:    "tail",
		Summary: "Read a database's changes feed",
		Example: `$ cdb tail /movies --limit 3
 SEQ | ID        | REV                                | DELETED
-----+-----------+------------------------------------+---------
 3-… | tt0211915 | 1-967a00dff5e02add41819138abb3284d | false
 4-… | tt2543164 | 2-7051cbe5c8faecd085a3fa619e6e6337 | false
 5-… | tt0245429 | 3-825cb35de44c433bfb2df415563a19de | true
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
			opts, err := tailOptions(ctx, s.Client, t.Database, inv, follow)
			if err != nil {
				return nil, err
			}
			includeDocs := opts.IncludeDocs
			cols := tailColumns(includeDocs)
			if follow {
				return tailFollow(ctx, s, t, opts, cols, includeDocs, sleep)
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

// tailOptions validates the flags and builds the client options. Every flag
// failure is a usage error, so a mistyped flag exits 2 rather than reaching the
// server; validation happens first, and only then does a follow ask the server
// where "now" is.
func tailOptions(ctx context.Context, c *couch.Client, db string, inv Invocation, follow bool) (couch.ChangesOptions, error) {
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
		//
		// "now" is resolved to a real sequence here, once, before anything is
		// opened, and never sent to the feed itself: a connection that drops
		// before it delivers a change has nothing of its own to resume from,
		// and reopening at whatever "now" had become by then would silently
		// skip every change written in between.
		info, err := c.DatabaseInfo(ctx, db)
		if err != nil {
			return couch.ChangesOptions{}, err
		}
		since = info.UpdateSeq
		if since == "" {
			// A server that reported no sequence. "now" still beats 0, which
			// would replay the whole database.
			since = "now"
		}
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
// off the line.
//
// A truncated cell keeps the "-" and gains an ellipsis, because CouchDB 3
// accepts a bare integer in --since and answers it by replaying the feed from
// the beginning: a cell reading "25" would look like a sequence an operator
// could paste back, and would silently hand them the whole database. "25-…"
// cannot be mistaken for one. Anything not shaped "<digits>-<something>" is
// returned whole and unmarked, because nothing was dropped from it, and
// Row.JSON always keeps the full value either way.
func shortSeq(seq string) string {
	i := strings.IndexByte(seq, '-')
	if i <= 0 || i == len(seq)-1 {
		return seq
	}
	for _, c := range []byte(seq[:i]) {
		if c < '0' || c > '9' {
			return seq
		}
	}
	return seq[:i+1] + "…"
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

// tailSleeper waits out one reconnect backoff, returning ctx.Err() if the
// operator interrupts first. tailCommand takes one so tests can drive the
// schedule without sleeping through it.
type tailSleeper func(ctx context.Context, d time.Duration) error

// tailSleep is the real wait.
func tailSleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// tailBackoff is the documented reconnect schedule: 1s, 2s, 4s, 8s, 16s, then
// 30s for every attempt after that. Retries are unbounded — a tail is meant to
// be left running — so the wait stops growing rather than the attempts.
func tailBackoff(attempt int) time.Duration {
	schedule := []time.Duration{
		1 * time.Second, 2 * time.Second, 4 * time.Second,
		8 * time.Second, 16 * time.Second,
	}
	if attempt >= 0 && attempt < len(schedule) {
		return schedule[attempt]
	}
	return 30 * time.Second
}

// tailReconnectable reports whether a finished follow should be reopened. A
// body that simply ended (nil), a transport failure, and a 5xx are all the
// feed dropping. A 4xx is not: a deleted database, a rejected token, or a
// change too large for the feed to read will not fix itself, and retrying
// forever would hide the reason behind a reconnect notice. Neither is a
// cancelled context, which is not a *couch.Error at all.
func tailReconnectable(err error) bool {
	if err == nil {
		return true
	}
	ce, ok := couch.AsError(err)
	if !ok {
		return false
	}
	return ce.Status == couch.StatusUnreachable || ce.Status >= 500
}

// tailFollow opens the continuous feed and keeps it open, reconnecting from
// the last change it delivered, or from the sequence the follow started at if
// it has not delivered one yet. It returns a Live stream, so each change is
// written the moment it arrives instead of waiting for a full table page.
func tailFollow(ctx context.Context, s *session.Session, t path.Target, opts couch.ChangesOptions, cols []Column, includeDocs bool, sleep tailSleeper) (Result, error) {
	rows := make(chan couch.ChangeRow)
	// Buffered, so the producer can report why it stopped and exit even if
	// nothing is reading yet.
	errc := make(chan error, 1)

	go func() {
		defer close(rows)
		// tailOptions has already resolved this, so it is a real sequence and
		// not "now": every reconnect that delivered nothing comes back here.
		since := opts.Since
		attempt := 0
		for {
			round := opts
			round.Since = since
			err := s.Client.ChangesFollow(ctx, t.Database, round, func(r couch.ChangeRow) error {
				select {
				case rows <- r:
					since = r.Seq
					// One change through means the feed is healthy again.
					attempt = 0
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
			// Ctrl-C wins over everything: the operator asked to stop, and the
			// front-ends print nothing for a cancelled context.
			if ctxErr := ctx.Err(); ctxErr != nil {
				errc <- ctxErr
				return
			}
			if !tailReconnectable(err) {
				errc <- err
				return
			}
			wait := tailBackoff(attempt)
			attempt++
			fmt.Fprintf(s.Stderr, "Lost the changes feed for %q; reconnecting from %s in %s.\n",
				t.Database, since, wait)
			if err := sleep(ctx, wait); err != nil {
				errc <- err
				return
			}
		}
	}()

	return Stream{Live: true, Columns: cols, Next: func() (Row, bool, error) {
		r, ok := <-rows
		if !ok {
			return Row{}, false, <-errc
		}
		return tailRow(r, includeDocs), true, nil
	}}, nil
}
