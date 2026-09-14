package command

import (
	"context"
	"io"
	"os"

	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// runDetails is the long help for run.
const runDetails = `A script is a file of the same lines the shell reads. A "#" that begins a token
starts a comment that runs to the end of the line, so a whole line of comment
and a note after a command are both written the same way; a "#" in quotes, in
the middle of a word, or after a backslash is an ordinary character. Blank
lines are skipped, and a line continues onto the next the way it does in the
shell. The ".cdb" extension is a convention, not a rule.

A script is never interactive: no prompt and no guided builder is reachable
while it runs, so a line that would confirm needs --yes, on the line or on
"run" itself. Execution stops at the first failing line, whose sentence goes to
standard error as "<file>:<line>: <sentence>" and whose exit code becomes the
script's; a line beginning with "-" has its failure reported and ignored.
"exit" ends the script with success.

Arguments after the file name are $1 to $9, and $# is how many there are. A
flag after the file name is one of them, not a flag of "run" itself, so a
script takes "--limit 5" the way it takes any other pair of arguments. The
script gets its own variable scope, so it never changes its caller's variables,
and "connect" and "cd" lines take effect for the rest of it. Scripts may nest
eight deep.`

// RunFrom returns the run command, executing a script through runner. A nil
// runner means script execution is unavailable, which is what Default()
// registers before either front-end swaps it out for the live one.
func RunFrom(runner func(ctx context.Context, r io.Reader, name string, args []string, yes bool) error) Command {
	return Command{
		Name:    "run",
		Summary: "Run a file of cdb commands",
		Example: `$ cdb run nightly.cdb
$ cdb run --yes purge.cdb movies 2001
admin@localhost:5984:/> run nightly.cdb
$ cdb < nightly.cdb`,
		Details: runDetails,
		Usage:   "<file> [arg...]",
		MinArgs: 1,
		MaxArgs: -1,
		// Everything after the file name belongs to the script, flags
		// included: "run job.cdb --limit 5" gives the script "$1 $2".
		StopAtFirstArg: true,
		Run: func(ctx context.Context, s *session.Session, inv Invocation) (Result, error) {
			if runner == nil {
				return nil, Usagef("run", "running a script is not available here.")
			}
			// #54: the connection is opened before the first line, the way
			// the shell opens one before its first prompt. A script whose
			// server is unreachable used to report it as "job.cdb:1: Could
			// not reach …" — the failure named a line that had nothing wrong
			// with it, and a script whose first line was a comment named a
			// different line each time it was edited. The ordinary one-shot
			// sentence is said once instead, and no line runs.
			//
			// Inside the shell the session is already connected, so this is
			// the one-shot path alone; a script with no target named anywhere
			// still connects line by line, which is what a script that starts
			// with its own "connect" needs.
			if !s.Connected() && TargetNamed(s) {
				if err := Open(ctx, s, ""); err != nil {
					return nil, err
				}
			}
			name := inv.Arg(0)
			f, err := os.Open(name)
			if err != nil {
				return nil, Errorf(nil, "could not read the script %s: %v", name, err)
			}
			defer func() { _ = f.Close() }()
			// The script's own lines render themselves as they run, so there is
			// nothing left for the front-end to print.
			// A failing line has already printed "<file>:<line>: <sentence>"
			// for itself and comes back marked as reported, so the failure
			// travels on unchanged and only its exit code is still wanted.
			if err := runner(ctx, f, name, inv.Args[1:], inv.Bool("yes")); err != nil {
				return nil, err
			}
			return Empty{}, nil
		},
	}
}

// Run returns the run command with no script runner behind it.
func Run() Command { return RunFrom(nil) }
