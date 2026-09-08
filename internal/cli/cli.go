// Package cli builds the cobra command tree from the command registry.
package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/sriannamalai/CDB.CLI/internal/command"
	"github.com/sriannamalai/CDB.CLI/internal/config"
	"github.com/sriannamalai/CDB.CLI/internal/render"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// BuildInfo carries the ldflags-injected release identity.
type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

// NewRoot builds the cobra tree: one subcommand per registry entry that is not
// ShellOnly, plus version and completion.
func NewRoot(reg *command.Registry, s *session.Session, build BuildInfo) *cobra.Command {
	root := &cobra.Command{
		Use:           "cdb",
		Short:         "Interactive CouchDB client",
		Long:          "cdb is an interactive shell and one-shot command line client for Apache CouchDB 3.x.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetOut(s.Stdout)
	root.SetErr(s.Stderr)
	root.SetIn(s.Stdin())
	// A malformed or unknown flag is a usage error, not a command error: map it
	// to command.UsageError so Execute reports exit code 2. FlagErrorFunc is
	// inherited by every subcommand that does not set its own.
	root.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		return command.Usagef(c.Name(), "%s", err)
	})

	pf := root.PersistentFlags()
	pf.String("profile", "", "connection profile to use, overriding CDB_PROFILE and the default")
	pf.String("url", "", "server URL, overriding the profile and CDB_URL")
	pf.String("path", "", "starting virtual path")
	pf.String("format", "", "output format: table or json")
	pf.String("color", "", "colour: auto, always or never")
	pf.String("pager", "", "pager command, or off")
	pf.Bool("json", false, "print raw JSON instead of a table")
	pf.Bool("yes", false, "skip confirmation prompts")
	pf.Bool("verbose", false, "include raw status codes and reasons in errors")

	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the version, commit and build date",
		// cobra.NoArgs reports an unexpected argument as a plain error, which
		// Execute would otherwise map to exit code 1. Wrap it as a
		// command.UsageError so it takes the same exit-2 path as every other
		// usage mistake.
		Args: func(c *cobra.Command, args []string) error {
			if len(args) > 0 {
				return command.Usagef(c.Name(), "unknown command %q for %q", args[0], c.CommandPath())
			}
			return nil
		},
		RunE: func(c *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(s.Stdout, "cdb %s (commit %s, built %s)\n", orUnknown(build.Version), orUnknown(build.Commit), orUnknown(build.Date))
			return err
		},
	})

	for _, c := range reg.All() {
		if c.ShellOnly {
			continue
		}
		root.AddCommand(newSubcommand(reg, c, s))
	}

	// Cobra normally adds this during ExecuteC. Adding it here as well means
	// the tree NewRoot returns is the same tree Execute runs, which is what
	// the completion tests inspect. InitDefaultCompletionCmd is a no-op when
	// the command already exists, so calling it twice is safe. It must come
	// after the subcommand loop: cobra skips it for a command with no
	// subcommands.
	root.InitDefaultCompletionCmd()
	return root
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

func newSubcommand(reg *command.Registry, c command.Command, s *session.Session) *cobra.Command {
	use := c.Name
	if c.Usage != "" {
		use += " " + c.Usage
	}
	long := c.Summary
	if c.Details != "" {
		long += "\n\n" + c.Details
	}
	sub := &cobra.Command{
		Use:                use,
		Aliases:            c.Aliases,
		Short:              c.Summary,
		Long:               long,
		SilenceUsage:       true,
		SilenceErrors:      true,
		DisableFlagParsing: false,
		RunE: func(cc *cobra.Command, args []string) error {
			if err := c.CheckArgsErr(args); err != nil {
				return err
			}
			applyGlobalFlags(cc, s)
			if c.NeedsClient && !s.Connected() {
				target, _ := cc.Flags().GetString("profile")
				if target == "" {
					target, _ = cc.Flags().GetString("url")
				}
				if err := command.Open(cc.Context(), s, target); err != nil {
					return err
				}
			}
			// --path is applied last, and deliberately after the connection:
			// session.Attach resets the current path to "/" for the new
			// server, so a path applied before the lazy auto-connect above
			// would be silently thrown away.
			applyPathFlag(cc, s)
			inv := command.Invocation{
				Args:   args,
				Flags:  cc.Flags(),
				Stdin:  s.Stdin(),
				Stdout: s.Stdout,
				Stderr: s.Stderr,
			}
			res, err := c.Run(cc.Context(), s, inv)
			if err != nil {
				return err
			}
			forceJSON, _ := cc.Flags().GetBool("json")
			r := render.New(s.Stdout, render.OptionsFor(s.Prefs, s.Stdout, forceJSON))
			return r.Render(res)
		},
	}
	fs := reg.NewFlagSet(c)
	sub.Flags().AddFlagSet(fs)
	return sub
}

// applyGlobalFlags copies persistent and shared flags into session prefs.
func applyGlobalFlags(c *cobra.Command, s *session.Session) {
	flags := c.Flags()
	if v, err := flags.GetBool("yes"); err == nil && v {
		s.Prefs.Yes = true
	}
	if v, err := flags.GetBool("verbose"); err == nil && v {
		s.Prefs.Verbose = true
	}
	if v, err := flags.GetString("format"); err == nil && v != "" {
		s.Prefs.Format = session.Format(v)
	}
	if v, err := flags.GetString("color"); err == nil && v != "" {
		s.Prefs.Color = session.ColorMode(v)
	}
	if v, err := flags.GetString("pager"); err == nil && v != "" {
		s.Prefs.Pager = v
	}
}

// applyPathFlag sets the session's current path from --path. It is separate
// from applyGlobalFlags because it must run after any auto-connect: see the
// call site in newSubcommand.
func applyPathFlag(c *cobra.Command, s *session.Session) {
	if v, err := c.Flags().GetString("path"); err == nil && v != "" {
		s.SetPath(v)
	}
}

// Execute runs one command line and returns the process exit code. It renders
// errors to the session's stderr.
func Execute(ctx context.Context, reg *command.Registry, s *session.Session, build BuildInfo, args []string) int {
	// A one-shot run may prompt only when both ends are a real terminal. This
	// is the only place the cobra front-end sets it, and without it every
	// confirmation, the connect walk-through, find's guided builder and
	// rmdir's retype step would be unreachable outside the shell.
	s.Prefs.Interactive = render.IsTerminal(s.Stdout) && render.IsTerminalReader(s.Stdin())
	// Load the configured output format/color/pager before any flag is
	// applied (applyGlobalFlags, below, runs inside each subcommand's RunE),
	// so a one-shot run honours config.toml the same way the shell does, and
	// a flag still overrides it.
	config.ApplyOutputPrefs(s)
	root := NewRoot(reg, s, build)
	root.SetArgs(args)

	// cobra reports an unrecognised top-level command (e.g. "cdb frobnicate")
	// as a plain error out of Find, before ExecuteContext ever calls a RunE.
	// Detect it the same way cobra does and map it to command.UsageError so it
	// takes the exit-2 path below instead of falling through to ExitCode's
	// generic exit-1 branch.
	var err error
	if _, _, findErr := root.Find(args); findErr != nil {
		err = command.Usagef(root.Name(), "%s", findErr)
	} else {
		err = root.ExecuteContext(ctx)
	}
	if err == nil {
		return ExitOK
	}
	// Ctrl-C during a command, or replications --watch interrupted, wraps
	// context.Canceled: the operator asked to stop, so nothing is printed.
	if errors.Is(err, context.Canceled) {
		return ExitCode(err)
	}
	var ue *command.UsageError
	if errors.As(err, &ue) {
		fmt.Fprintln(s.Stderr, ue.Error())
		return ExitUsage
	}
	fmt.Fprintln(s.Stderr, errorText(err, s.Prefs.Verbose))
	return ExitCode(err)
}

// errorText renders an error for the terminal.
func errorText(err error, verbose bool) string { return render.ErrorMessage(err, verbose) }
