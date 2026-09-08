// Command cdb is an interactive shell and one-shot command line client for
// Apache CouchDB 3.x.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/sriannamalai/CDB.CLI/internal/cli"
	"github.com/sriannamalai/CDB.CLI/internal/command"
	"github.com/sriannamalai/CDB.CLI/internal/config"
	"github.com/sriannamalai/CDB.CLI/internal/render"
	"github.com/sriannamalai/CDB.CLI/internal/session"
	"github.com/sriannamalai/CDB.CLI/internal/shell"
)

// Injected with -ldflags at build time.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	s := session.New(os.Stdin, os.Stdout, os.Stderr)
	build := cli.BuildInfo{Version: version, Commit: commit, Date: date}
	reg := command.Default()

	if len(os.Args) == 1 && render.IsTerminal(os.Stdout) && render.IsTerminalReader(os.Stdin) {
		// No process-wide signal context on the shell path. Shell.Run derives
		// a fresh signal context per command; if the parent were also watching
		// SIGINT, the first Ctrl-C would cancel it for good and every later
		// command would start already cancelled. Spec section 7 requires
		// Ctrl-C to return to the prompt.
		os.Exit(runShell(context.Background(), reg, s))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	code := cli.Execute(ctx, reg, s, build, os.Args[1:])
	_ = s.Detach()
	os.Exit(code)
}

// runShell loads config, opens the shell, and returns the exit code.
func runShell(ctx context.Context, reg *command.Registry, s *session.Session) int {
	defer func() { _ = s.Detach() }()
	// The shell always has a terminal at both ends, so prompts are allowed.
	// This must be set before the auto-connect below, or the connect
	// walk-through cannot ask for a URL on a first run.
	s.Prefs.Interactive = true
	config.ApplyOutputPrefs(s)
	histPath, _ := config.HistoryPath()
	sh, err := shell.New(reg, s, shell.Config{HistoryFile: histPath, Keymap: s.Prefs.Keymap})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return cli.ExitError
	}
	if err := command.Open(ctx, s, ""); err != nil {
		if msg := notConnectedMessage(err, s.Prefs.Verbose); msg != "" {
			fmt.Fprintln(os.Stderr, msg)
		}
	}
	if err := sh.Run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return cli.ExitError
	}
	return cli.ExitOK
}

// notConnectedMessage renders the shell's opening auto-connect failure. It is
// the first message a new user sees, so it goes through the same section 11
// mapper every other error does — "Could not reach localhost:5984. Is CouchDB
// running?", not a raw dial error. It returns "" when nothing should be
// printed.
func notConnectedMessage(err error, verbose bool) string {
	if err == nil || errors.Is(err, context.Canceled) {
		return ""
	}
	const hint = "Run \"connect <url>\" to connect."
	var ue *command.UsageError
	if errors.As(err, &ue) {
		// Already prefixed with the command that raised it ("connect: no
		// profile is saved."); a "Not connected:" in front would double it.
		return ue.Error() + "\n" + hint
	}
	return "Not connected: " + render.ErrorMessage(err, verbose) + "\n" + hint
}
