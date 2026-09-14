package command

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/pflag"
	"github.com/sriannamalai/CDB.CLI/internal/couch"
	"github.com/sriannamalai/CDB.CLI/internal/path"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// taskTypes are the _active_tasks types --type accepts. CouchDB may report
// others; the flag is checked against this list so that a typo is a usage
// error rather than a silently empty table, and an unlisted type still appears
// in an unfiltered listing.
var taskTypes = []string{"replication", "database_compaction", "view_compaction", "indexer", "search_indexer"}

// defaultTaskInterval is how often --watch re-reads _active_tasks.
const defaultTaskInterval = 2 * time.Second

// tasksDetails is the long help for tasks.
const tasksDetails = `Every row is one entry of _active_tasks: a running replication, a database or
view compaction, or an index build. A task that has finished is gone from the
list; "tasks" is a snapshot, not a history.

--watch re-reads the list every --interval and prints the rows again whenever
the set of tasks changes. The rows are written as they arrive rather than as an
aligned table, because a table cannot be drawn until its last row is known and
a watch has no last row. Press Ctrl-C to stop.`

// Tasks returns the tasks command.
func Tasks() Command {
	return Command{
		Name:    "tasks",
		Summary: "List the tasks CouchDB is running right now",
		Example: `$ cdb tasks
 TYPE                | PROGRESS | TARGET                 | STARTED  | UPDATED  | NODE
---------------------+----------+------------------------+----------+----------+---------------
 database_compaction | 84%      | movies                 | 15:20:00 | 15:20:31 | nonode@nohost

$ cdb tasks --type replication --watch
$ cdb tasks --db /movies`,
		Usage:       "",
		Details:     tasksDetails,
		MinArgs:     0,
		MaxArgs:     0,
		NeedsClient: true,
		Flags: func(fs *pflag.FlagSet) {
			fs.String("type", "", "show only tasks of this type ("+strings.Join(taskTypes, ", ")+")")
			fs.String("db", "", "show only tasks for this database path")
			fs.Bool("watch", false, "keep the list up to date until interrupted")
			fs.Duration("interval", defaultTaskInterval, "how often --watch re-reads the list")
		},
		Complete: completePath,
		Run: func(ctx context.Context, s *session.Session, inv Invocation) (Result, error) {
			filter, err := taskFilterFrom(s, inv)
			if err != nil {
				return nil, err
			}
			if inv.Bool("watch") {
				// The same gate, and the same sentence, "replications
				// --watch" has carried since 1.0
				// (internal/command/replicate.go:158-161): a watch with
				// nobody at the terminal is a command that never returns.
				if !s.Prefs.Interactive {
					return nil, Usagef("tasks", "--watch needs a terminal; drop it to print one snapshot")
				}
				interval := inv.Duration("interval")
				if interval <= 0 {
					return nil, Usagef("tasks", "--interval must be a positive duration, as in --interval 5s")
				}
				return watchTasks(ctx, s, filter, interval), nil
			}
			tasks, err := s.Client.ActiveTasks(ctx)
			if err != nil {
				return nil, err
			}
			tasks = filter.apply(tasks)
			if len(tasks) == 0 {
				return Message{Text: "No active tasks."}, nil
			}
			return taskRows(tasks), nil
		},
	}
}

// taskFilter is --type and --db, resolved once before any request is made.
type taskFilter struct {
	kind     string
	database string
}

// taskFilterFrom reads the two filter flags. --db is a cdb path, so "tasks --db
// ." filters by the database the session is sitting in.
func taskFilterFrom(s *session.Session, inv Invocation) (taskFilter, error) {
	f := taskFilter{kind: inv.String("type")}
	if f.kind != "" && !knownTaskType(f.kind) {
		return taskFilter{}, Usagef("tasks", "%q is not a task type; expected %s", f.kind, strings.Join(taskTypes, ", "))
	}
	if raw := inv.String("db"); raw != "" {
		t, err := s.Resolve(raw)
		if err != nil {
			return taskFilter{}, err
		}
		if t.Kind == path.KindServer {
			return taskFilter{}, Usagef("tasks", "--db needs a database path, as in --db /movies")
		}
		f.database = t.Database
	}
	return f, nil
}

func knownTaskType(kind string) bool {
	for _, t := range taskTypes {
		if t == kind {
			return true
		}
	}
	return false
}

// apply keeps the tasks both filters accept.
func (f taskFilter) apply(tasks []couch.ActiveTask) []couch.ActiveTask {
	if f.kind == "" && f.database == "" {
		return tasks
	}
	out := make([]couch.ActiveTask, 0, len(tasks))
	for _, t := range tasks {
		if f.kind != "" && t.Type != f.kind {
			continue
		}
		if f.database != "" && !taskTouches(t, f.database) {
			continue
		}
		out = append(out, t)
	}
	return out
}

// taskTouches reports whether a task is about the named database. A compaction
// or index build names it outright; a replication names it as the last path
// segment of an endpoint, which is the only part of a URL that can be compared
// with a local database name at all.
func taskTouches(t couch.ActiveTask, db string) bool {
	if t.Database == db {
		return true
	}
	return endpointDatabase(t.Source) == db || endpointDatabase(t.Target) == db
}

// endpointDatabase is the database name at the end of a replication endpoint,
// or "" when there is none. The endpoint is already redacted, so parsing it
// cannot resurrect a credential.
func endpointDatabase(endpoint string) string {
	if endpoint == "" {
		return ""
	}
	trimmed := strings.TrimSuffix(endpoint, "/")
	if i := strings.LastIndexByte(trimmed, '/'); i >= 0 {
		trimmed = trimmed[i+1:]
	}
	name, err := url.PathUnescape(trimmed)
	if err != nil {
		return trimmed
	}
	return name
}

// taskColumns is the column set both tasks and "compact --watch" render.
func taskColumns() []Column {
	return []Column{
		{Title: "type"}, {Title: "progress", Align: AlignRight}, {Title: "target"},
		{Title: "started"}, {Title: "updated"}, {Title: "node"},
	}
}

// taskRows renders a task list.
func taskRows(tasks []couch.ActiveTask) Rows {
	rows := Rows{Columns: taskColumns()}
	for _, t := range tasks {
		rows.Items = append(rows.Items, taskRow(t))
	}
	return rows
}

// taskRow renders one task. Row.JSON is the task object as the server sent it,
// with the endpoints redacted by internal/couch, so "tasks --json" is the
// server's own answer and not a summary of it.
func taskRow(t couch.ActiveTask) Row {
	return Row{
		Cells: []string{
			t.Type, taskProgress(t), taskTarget(t),
			taskTime(t.StartedOn), taskTime(t.UpdatedOn), t.Node,
		},
		JSON: t.Raw,
	}
}

// taskProgress is "42%", or blank when the server sent no progress at all.
// Zero is a progress: a just-started indexer reports it.
func taskProgress(t couch.ActiveTask) string {
	if !t.HasProgress {
		return ""
	}
	return fmt.Sprintf("%d%%", t.Progress)
}

// taskTarget is what the task is working on: both endpoints of a replication,
// or the database and design document of everything else.
func taskTarget(t couch.ActiveTask) string {
	if t.Source != "" || t.Target != "" {
		return t.Source + " → " + t.Target
	}
	if t.DesignDocument != "" {
		return t.Database + "/" + t.DesignDocument
	}
	return t.Database
}

// taskTime renders a unix timestamp as a local clock time. A task's start is
// only ever compared with now, so the date would be noise in a column; a task
// that has been running since yesterday is obvious from its progress.
func taskTime(unix int64) string {
	if unix == 0 {
		return ""
	}
	return time.Unix(unix, 0).Format("15:04:05")
}

// watchTasks returns the live stream --watch renders. It polls _active_tasks
// every interval and queues one row per task whenever the set has changed,
// which is what keeps an idle server from redrawing the same three lines every
// two seconds. An emptied list is announced once, as a single row, so that the
// operator sees the replication they were watching finish rather than a display
// that simply stops moving.
//
// The stream returns ctx.Err() when the operator interrupts, which is what
// makes the one-shot front end exit 130 in silence — the arrangement "tail
// --follow" uses.
func watchTasks(ctx context.Context, s *session.Session, filter taskFilter, interval time.Duration) Stream {
	var queue []Row
	last := ""
	first := true
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
				if !first {
					select {
					case <-ctx.Done():
						return Row{}, false, ctx.Err()
					case <-time.After(interval):
					}
				}
				first = false
				tasks, err := s.Client.ActiveTasks(ctx)
				if err != nil {
					return Row{}, false, err
				}
				tasks = filter.apply(tasks)
				fingerprint := taskFingerprint(tasks)
				if fingerprint == last {
					continue
				}
				last = fingerprint
				if len(tasks) == 0 {
					queue = []Row{{
						Cells: []string{"", "", "no active tasks", "", "", ""},
						JSON:  jsonObject("message", "no active tasks"),
					}}
					continue
				}
				queue = taskRows(tasks).Items
			}
		},
	}
}

// taskFingerprint is what "the set changed" means: the identity of every task
// and how far it has got. Progress is part of it on purpose — a compaction that
// is 40% through is news — while the updated timestamp is not, because a task
// that ticks its clock without moving is not.
func taskFingerprint(tasks []couch.ActiveTask) string {
	var b strings.Builder
	for _, t := range tasks {
		fmt.Fprintf(&b, "%s\x00%s\x00%s\x00%s\x00%d\x00%t\x00%d\n",
			t.Type, t.Node, t.Database, t.DocID, t.StartedOn, t.HasProgress, t.Progress)
	}
	return b.String()
}
