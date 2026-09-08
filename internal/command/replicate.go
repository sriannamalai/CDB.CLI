package command

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/spf13/pflag"
	"github.com/sriannamalai/CDB.CLI/internal/replicate"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// watchInterval is how often --watch redraws.
const watchInterval = 2 * time.Second

// unreachableEndpointNote explains the one failure mode of a same-server
// replication that looks like a cdb bug and is not: CouchDB 3.x has no local
// endpoints, so cdb has to name a full URL, and the only address it knows is
// the one it connected with.
const unreachableEndpointNote = `A same-server replication is written with the URL cdb itself connected with,
because CouchDB 3.x rejects a bare database name (local_endpoints_not_supported).
If the server cannot reach that address from where it runs — a remapped Docker
port, an SSH tunnel — the job is accepted and then fails with econnrefused;
"replications show <id>" reports it.`

// replicateDetails is the long help for replicate.
const replicateDetails = `Each endpoint is a database path on the connected server (/mydb) or a full
http(s) URL. Credentials in a URL are moved into CouchDB's per-endpoint auth
object, so they never reach the stored document's URL or the screen.

` + unreachableEndpointNote

// replicationsDetails is the long help for replications.
const replicationsDetails = `"replications list" reads _scheduler/docs, one row per replication document.
"replications show" reads the same entry and joins the _scheduler/jobs entry
for it, which adds the start time, the process id and the scheduler history; a
replication that has completed, failed or not started yet has no job, and show
prints the document alone. "replications cancel" deletes the _replicator
document, which stops the job. It confirms first, or fails without a terminal
unless --yes is given.

` + unreachableEndpointNote

// Replicate returns the replicate command.
func Replicate() Command {
	return Command{
		Name:    "replicate",
		Summary: "Start a replication between two databases",
		Example: `$ cdb replicate /movies /movies-backup --create-target --id movies-job
Started replication movies-job from http://localhost:5984/movies to http://localhost:5984/movies-backup. Run "cdb replications" to watch it.

$ cdb replicate /movies https://user:secret@backup.example.com/movies --continuous`,
		Usage:       "<source> <target>",
		Details:     replicateDetails,
		MinArgs:     2,
		MaxArgs:     2,
		NeedsClient: true,
		Complete:    completePath,
		Flags: func(fs *pflag.FlagSet) {
			fs.Bool("continuous", false, "keep replicating as changes arrive")
			fs.Bool("create-target", false, "create the target database if it is missing")
			fs.String("filter", "", "design document filter, as ddoc/name")
			fs.String("id", "", "document id for the replication job")
		},
		Run: func(ctx context.Context, s *session.Session, inv Invocation) (Result, error) {
			source, err := replicate.ResolveEndpoint(s.Client, s.Path(), inv.Arg(0))
			if err != nil {
				return nil, Usagef("replicate", "%v", err)
			}
			target, err := replicate.ResolveEndpoint(s.Client, s.Path(), inv.Arg(1))
			if err != nil {
				return nil, Usagef("replicate", "%v", err)
			}
			id, err := replicate.Create(ctx, s.Client, replicate.Request{
				ID:           inv.String("id"),
				Source:       source,
				Target:       target,
				Continuous:   inv.Bool("continuous"),
				CreateTarget: inv.Bool("create-target"),
				Filter:       inv.String("filter"),
			})
			if err != nil {
				return nil, err
			}
			// Endpoint.URL never carries userinfo, so this message is safe to
			// print even when the operator typed a password into a URL.
			return Message{Text: fmt.Sprintf("Started replication %s from %s to %s. Run \"cdb replications\" to watch it.", id, source.URL, target.URL)}, nil
		},
	}
}

// Replications returns the replications command.
func Replications() Command {
	return Command{
		Name:    "replications",
		Summary: "List, inspect and cancel replications",
		Example: `$ cdb replications
 ID         | STATE   | SOURCE                             | TARGET
------------+---------+------------------------------------+------------------------------------------
 movies-job | running | http://localhost:5984/movies/      | http://localhost:5984/movies-backup/

$ cdb replications show movies-job
$ cdb replications cancel movies-job --yes
Cancelled replication "movies-job".`,
		Usage:       "[list | show ID | cancel ID]",
		Details:     replicationsDetails,
		MinArgs:     0,
		MaxArgs:     2,
		NeedsClient: true,
		// Not Destructive: "replications list" and "replications show" are
		// read-only. Only the cancel branch below is dangerous, and it calls
		// Confirm itself.
		Flags: func(fs *pflag.FlagSet) {
			fs.Bool("watch", false, "redraw every two seconds until interrupted")
		},
		Run: func(ctx context.Context, s *session.Session, inv Invocation) (Result, error) {
			sub := inv.Arg(0)
			if sub == "" {
				sub = "list"
			}
			switch sub {
			case "list":
				if inv.Bool("watch") {
					if !s.Prefs.Interactive {
						return nil, Usagef("replications", "--watch needs a terminal; drop it to print one snapshot")
					}
					return watchReplications(ctx, s)
				}
				list, err := replicate.List(ctx, s.Client)
				if err != nil {
					return nil, err
				}
				return replicationRows(list), nil

			case "show":
				id := inv.Arg(1)
				if id == "" {
					return nil, Usagef("replications", "usage: replications show <id>")
				}
				st, err := replicate.Show(ctx, s.Client, id)
				if err != nil {
					return nil, err
				}
				// _scheduler/docs describes the document; _scheduler/jobs
				// describes the process running it, which is the only place
				// the history, pid and start time live. A replication that has
				// completed, failed or is still pending has no job, and that
				// is not an error.
				job, running, err := replicate.ShowJob(ctx, s.Client, st.ID)
				if err != nil {
					return nil, err
				}
				rows := Rows{Columns: []Column{{Title: "field"}, {Title: "value"}}}
				add := func(k, v string) {
					rows.Items = append(rows.Items, Row{Cells: []string{k, v}, JSON: jsonObject("field", k, "value", v)})
				}
				source, target, node := st.Source, st.Target, st.Node
				if running {
					// The job is the live view of the same replication, so it
					// wins wherever both report a field.
					source, target = orDefault(job.Source, source), orDefault(job.Target, target)
					node = orDefault(job.Node, node)
				}
				add("doc id", st.DocID)
				add("job id", st.ID)
				add("source", source)
				add("target", target)
				add("state", st.State)
				add("node", node)
				add("errors", strconv.FormatInt(st.ErrorCount, 10))
				add("last updated", st.LastUpdated)
				if st.Error != "" {
					add("error", st.Error)
				}
				if running {
					if job.StartTime != "" {
						add("start time", job.StartTime)
					}
					if job.PID != "" {
						add("pid", job.PID)
					}
					for _, e := range recentHistory(job.History) {
						add("history", historyLine(e))
					}
				}
				if len(st.Info) > 0 {
					add("info", string(st.Info))
				}
				return rows, nil

			case "cancel":
				id := inv.Arg(1)
				if id == "" {
					return nil, Usagef("replications", "usage: replications cancel <id>")
				}
				if err := Confirm(s, fmt.Sprintf("Cancel replication %q?", id)); err != nil {
					return nil, err
				}
				if err := replicate.Cancel(ctx, s.Client, id); err != nil {
					return nil, err
				}
				return Message{Text: fmt.Sprintf("Cancelled replication %q.", id)}, nil

			default:
				return nil, Usagef("replications", "unknown subcommand %q; expected list, show or cancel", sub)
			}
		},
	}
}

// historyEvents is how many scheduler history entries "replications show"
// prints. CouchDB keeps the most recent first and up to a few dozen of them,
// which is more than a status table should carry.
const historyEvents = 5

// orDefault returns s, or fallback when s is empty.
func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// recentHistory returns the newest events, newest first, as CouchDB orders them.
func recentHistory(h []replicate.HistoryEvent) []replicate.HistoryEvent {
	if len(h) > historyEvents {
		return h[:historyEvents]
	}
	return h
}

// historyLine renders one scheduler history event.
func historyLine(e replicate.HistoryEvent) string {
	line := e.Timestamp + " " + e.Type
	if e.Reason != "" {
		line += ": " + e.Reason
	}
	return line
}

// replicationRows renders scheduler entries. Every field it shows comes from
// replicate.Status, whose source and target are already redacted.
func replicationRows(list []replicate.Status) Rows {
	rows := Rows{Columns: []Column{
		{Title: "id"}, {Title: "state"}, {Title: "source"}, {Title: "target"},
		{Title: "errors", Align: AlignRight}, {Title: "updated"},
	}}
	for _, st := range list {
		rows.Items = append(rows.Items, Row{
			Cells: []string{st.DocID, st.State, st.Source, st.Target, strconv.FormatInt(st.ErrorCount, 10), st.LastUpdated},
			JSON:  mustJSON(st),
		})
	}
	if len(rows.Items) == 0 {
		rows.Hint = "no replications are scheduled"
	}
	return rows
}

// watchReplications redraws the list until the context is cancelled. It is one
// of the few commands that writes to the session directly: a live display has
// no Result to hand the renderer until it ends. Cancellation is reported as
// context.Canceled so that the one-shot front-end exits 130.
func watchReplications(ctx context.Context, s *session.Session) (Result, error) {
	ticker := time.NewTicker(watchInterval)
	defer ticker.Stop()
	for {
		list, err := replicate.List(ctx, s.Client)
		if err != nil {
			return nil, err
		}
		rows := replicationRows(list)
		// ESC[H moves the cursor home; ESC[2J erases the display.
		fmt.Fprint(s.Stdout, "\x1b[H\x1b[2J")
		for _, item := range rows.Items {
			fmt.Fprintf(s.Stdout, "%-24s %-12s %-6s %s\n", item.Cells[0], item.Cells[1], item.Cells[4], item.Cells[5])
		}
		if len(rows.Items) == 0 {
			fmt.Fprintln(s.Stdout, "no replications are scheduled")
		}
		fmt.Fprintln(s.Stdout, "\nwatching; press Ctrl-C to stop")
		select {
		case <-ctx.Done():
			return Empty{}, ctx.Err()
		case <-ticker.C:
		}
	}
}
