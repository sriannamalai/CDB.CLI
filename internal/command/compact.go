package command

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/pflag"
	"github.com/sriannamalai/CDB.CLI/internal/couch"
	"github.com/sriannamalai/CDB.CLI/internal/path"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// compactPollInterval is how often --watch re-reads _active_tasks, and
// compactAppearWindow is how long it waits for a task to show up at all. A
// database with a few hundred documents compacts between two polls and never
// produces a task, which is a finished compaction and not a failure.
const (
	compactPollInterval = 2 * time.Second
	compactAppearWindow = 10 * time.Second
)

// compactDetails is the long help for compact.
const compactDetails = `Compaction rewrites a database file without the old revisions of its
documents, and usually reclaims a great deal of space. It is safe — the
database stays readable and writable throughout — but it is long-running on a
large database and it needs room for a second copy of the file while it runs,
so cdb asks before starting one.

--ddoc compacts one design document's view indexes instead of the database.
--cleanup additionally deletes the view index files left behind by design
documents that have changed or been removed; it runs after the compaction is
requested, not instead of it.

--watch follows the matching entry in _active_tasks until it disappears.
CouchDB compacts a small database faster than the first poll, so a watch that
has seen no task after ten seconds reports the compaction as finished.`

// Compact returns the compact command.
func Compact() Command {
	return Command{
		Name:    "compact",
		Summary: "Reclaim space by compacting a database or its view indexes",
		Example: `$ cdb compact /movies
Compact movies? [y/N] y
Compaction of movies started.

$ cdb compact /movies --watch --yes
$ cdb compact /movies --ddoc by_year --cleanup --yes`,
		Usage:       "<db-path>",
		Details:     compactDetails,
		MinArgs:     1,
		MaxArgs:     1,
		NeedsClient: true,
		Destructive: true,
		Complete:    completePath,
		Flags: func(fs *pflag.FlagSet) {
			fs.String("ddoc", "", "compact this design document's view indexes instead of the database")
			fs.Bool("cleanup", false, "also delete orphaned view index files")
			fs.Bool("watch", false, "follow the compaction until it finishes")
		},
		Run: func(ctx context.Context, s *session.Session, inv Invocation) (Result, error) {
			t, err := s.Resolve(inv.Arg(0))
			if err != nil {
				return nil, err
			}
			if t.Kind != path.KindDatabase {
				return nil, Usagef("compact", "%s is %s %s; compact acts on a database, as in \"compact /mydb\"",
					t.Path, t.Kind.Article(), t.Kind)
			}
			ddoc := inv.String("ddoc")
			if ddoc != "" && !strings.HasPrefix(ddoc, "_design/") {
				ddoc = "_design/" + ddoc
			}

			prompt := fmt.Sprintf("Compact %s?", t.Database)
			if ddoc != "" {
				prompt = fmt.Sprintf("Compact the views of %s in %s?", ddoc, t.Database)
			}
			if err := Confirm(ctx, s, prompt); err != nil {
				return nil, err
			}

			what := t.Database
			if ddoc != "" {
				what = t.Database + "/" + ddoc
				err = s.Client.CompactView(ctx, t.Database, ddoc)
			} else {
				err = s.Client.Compact(ctx, t.Database)
			}
			if err != nil {
				return nil, err
			}
			if inv.Bool("cleanup") {
				if err := s.Client.ViewCleanup(ctx, t.Database); err != nil {
					return nil, err
				}
			}

			if inv.Bool("watch") {
				if !s.Prefs.Interactive {
					return nil, Usagef("compact", "--watch needs a terminal; drop it to start the compaction and return")
				}
				return watchCompaction(ctx, s, t.Database, what), nil
			}
			text := fmt.Sprintf("Compaction of %s started.", what)
			if inv.Bool("cleanup") {
				text += " Orphaned view indexes are being cleaned up as well."
			}
			return Message{Text: text}, nil
		},
	}
}

// watchCompaction follows the compaction tasks for one database with the real
// clock and the real intervals.
func watchCompaction(ctx context.Context, s *session.Session, db, what string) Stream {
	return watchCompactionWith(ctx, s, db, what, compactPollInterval, compactAppearWindow, time.Now)
}

// watchCompactionWith is watchCompaction with the schedule given explicitly, so
// that a test can drive the ten-second window without waiting ten seconds. It
// renders with the same columns as "tasks", so a compaction looks the same
// whichever command is watching it, and its last row is the sentence that says
// the work is over — a live stream has no other way to end with a message.
func watchCompactionWith(ctx context.Context, s *session.Session, db, what string,
	poll, window time.Duration, now func() time.Time) Stream {
	var queue []Row
	last := ""
	seen := false
	first := true
	deadline := now().Add(window)
	done := false
	return Stream{
		Live:    true,
		Columns: taskColumns(),
		Next: func() (Row, bool, error) {
			for {
				if len(queue) > 0 {
					row := queue[0]
					queue = queue[1:]
					return row, true, nil
				}
				if done {
					return Row{}, false, nil
				}
				if !first {
					select {
					case <-ctx.Done():
						return Row{}, false, ctx.Err()
					case <-time.After(poll):
					}
				}
				first = false
				tasks, err := s.Client.ActiveTasks(ctx)
				if err != nil {
					return Row{}, false, err
				}
				mine := compactionTasksFor(tasks, db)
				switch {
				case len(mine) > 0:
					seen = true
					fingerprint := taskFingerprint(mine)
					if fingerprint == last {
						continue
					}
					last = fingerprint
					queue = taskRows(mine).Items
				case seen, now().After(deadline):
					// The task was there and has gone, or it never appeared
					// inside the window: either way the compaction is over.
					done = true
					queue = []Row{finishedCompactionRow(what)}
				}
			}
		},
	}
}

// compactionTasksFor keeps the compaction tasks for one database.
func compactionTasksFor(tasks []couch.ActiveTask, db string) []couch.ActiveTask {
	var out []couch.ActiveTask
	for _, t := range tasks {
		if t.Database != db {
			continue
		}
		if t.Type == "database_compaction" || t.Type == "view_compaction" {
			out = append(out, t)
		}
	}
	return out
}

// finishedCompactionRow is the last line of a watch.
func finishedCompactionRow(what string) Row {
	text := fmt.Sprintf("Compaction of %s finished.", what)
	return Row{
		Cells: []string{"", "", text, "", "", ""},
		JSON:  jsonObject("message", text),
	}
}
