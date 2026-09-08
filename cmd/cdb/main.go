// Command cdb is an interactive shell and one-shot command line client for
// Apache CouchDB 3.x.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/sriannamalai/CDB.CLI/internal/cli"
	"github.com/sriannamalai/CDB.CLI/internal/command"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// Injected with -ldflags at build time.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	s := session.New(os.Stdin, os.Stdout, os.Stderr)
	build := cli.BuildInfo{Version: version, Commit: commit, Date: date}
	code := cli.Execute(ctx, command.Default(), s, build, os.Args[1:])
	_ = s.Detach()
	os.Exit(code)
}
