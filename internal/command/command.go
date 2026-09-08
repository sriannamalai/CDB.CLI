package command

import (
	"context"
	"fmt"
	"io"
	"sort"

	"github.com/spf13/pflag"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// Candidate is one completion suggestion.
type Candidate struct {
	Value       string
	Display     string
	Description string
	Tag         string
}

// Invocation is one parsed call of a command.
type Invocation struct {
	Args   []string
	Flags  *pflag.FlagSet
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// Shell is true when the call came from the interactive shell.
	Shell bool
}

// Arg returns positional argument n, or "" when it is absent.
func (i Invocation) Arg(n int) string {
	if n < 0 || n >= len(i.Args) {
		return ""
	}
	return i.Args[n]
}

// Bool reads a bool flag, defaulting to false when the flag is undeclared.
func (i Invocation) Bool(name string) bool {
	if i.Flags == nil {
		return false
	}
	v, err := i.Flags.GetBool(name)
	if err != nil {
		return false
	}
	return v
}

// String reads a string flag, defaulting to "".
func (i Invocation) String(name string) string {
	if i.Flags == nil {
		return ""
	}
	v, err := i.Flags.GetString(name)
	if err != nil {
		return ""
	}
	return v
}

// Int reads an int flag, defaulting to 0.
func (i Invocation) Int(name string) int {
	if i.Flags == nil {
		return 0
	}
	v, err := i.Flags.GetInt(name)
	if err != nil {
		return 0
	}
	return v
}

// StringSlice reads a repeated or comma-separated string flag.
func (i Invocation) StringSlice(name string) []string {
	if i.Flags == nil {
		return nil
	}
	v, err := i.Flags.GetStringSlice(name)
	if err != nil {
		return nil
	}
	return v
}

// Changed reports whether the caller set the flag explicitly.
func (i Invocation) Changed(name string) bool {
	if i.Flags == nil {
		return false
	}
	return i.Flags.Changed(name)
}

// Command is one cdb command, usable from both front-ends.
type Command struct {
	Name    string
	Aliases []string
	Summary string
	// Usage is the argument synopsis, e.g. "<path> [file]".
	Usage string
	// Details is optional prose shown by "help <command>" and by "cdb help
	// <command>", under the one-line summary. It is where a command documents
	// a caveat that does not fit on the summary line.
	Details string
	// Flags declares the command's flags on a fresh flag set.
	Flags   func(*pflag.FlagSet)
	MinArgs int
	// MaxArgs is -1 for unbounded.
	MaxArgs int
	// NeedsClient makes the front-end fail early when no connection is open.
	NeedsClient bool
	// ShellOnly hides the command from the cobra tree.
	ShellOnly bool
	// Destructive marks a command as dangerous, for help and completion. It is
	// informational only: neither front-end acts on it. A command that needs a
	// confirmation calls Confirm or ConfirmPhrase from inside its own Run, so
	// that a command whose subcommands differ (replications list versus
	// replications cancel) can confirm only where it matters.
	Destructive bool
	Complete    func(ctx context.Context, s *session.Session, args []string, cur string) []Candidate
	Run         func(ctx context.Context, s *session.Session, inv Invocation) (Result, error)
}

// CheckArgsErr validates the argument count.
func (c Command) CheckArgsErr(args []string) error {
	if len(args) < c.MinArgs {
		return Usagef(c.Name, "expected at least %d argument(s), got %d\nusage: %s %s", c.MinArgs, len(args), c.Name, c.Usage)
	}
	if c.MaxArgs >= 0 && len(args) > c.MaxArgs {
		return Usagef(c.Name, "expected at most %d argument(s), got %d\nusage: %s %s", c.MaxArgs, len(args), c.Name, c.Usage)
	}
	return nil
}

// Registry maps names and aliases to commands.
type Registry struct {
	byName map[string]Command
	order  []string
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{byName: map[string]Command{}}
}

// Register adds a command. It panics on a duplicate name or alias, because a
// duplicate is always a programming error.
func (r *Registry) Register(c Command) {
	if c.Name == "" {
		panic("command: Register with an empty name")
	}
	for _, n := range append([]string{c.Name}, c.Aliases...) {
		if _, dup := r.byName[n]; dup {
			panic(fmt.Sprintf("command: duplicate registration for %q", n))
		}
		r.byName[n] = c
	}
	r.order = append(r.order, c.Name)
	sort.Strings(r.order)
}

// Replace swaps an already-registered command for a new one with the same
// primary name, keeping its position in the sorted order. The shell uses it to
// install a history command bound to its live history source. It panics when
// nothing is registered under that name, because that is a programming error.
func (r *Registry) Replace(c Command) {
	old, ok := r.byName[c.Name]
	if !ok || old.Name != c.Name {
		panic(fmt.Sprintf("command: Replace before Register for %q", c.Name))
	}
	for _, n := range append([]string{old.Name}, old.Aliases...) {
		delete(r.byName, n)
	}
	for _, n := range append([]string{c.Name}, c.Aliases...) {
		if _, dup := r.byName[n]; dup {
			panic(fmt.Sprintf("command: duplicate registration for %q", n))
		}
		r.byName[n] = c
	}
}

// Lookup finds a command by name or alias.
func (r *Registry) Lookup(name string) (Command, bool) {
	c, ok := r.byName[name]
	return c, ok
}

// Names returns the primary names, sorted.
func (r *Registry) Names() []string {
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// All returns every command, sorted by name.
func (r *Registry) All() []Command {
	out := make([]Command, 0, len(r.order))
	for _, n := range r.order {
		out = append(out, r.byName[n])
	}
	return out
}

// NewFlagSet builds a flag set for a command, with the shared flags every
// command accepts already declared.
func (r *Registry) NewFlagSet(c Command) *pflag.FlagSet { return NewFlagSet(c) }

// NewFlagSet builds a command's flag set. It is what both front-ends parse a
// line with and what completion consults to tell a flag's value from a path.
func NewFlagSet(c Command) *pflag.FlagSet {
	fs := pflag.NewFlagSet(c.Name, pflag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Bool("json", false, "print raw JSON instead of a table")
	fs.Bool("yes", false, "skip confirmation prompts")
	fs.Bool("verbose", false, "include raw status codes and reasons in errors")
	// Shared rather than declared on "connect": every command auto-connects,
	// so every command can be the one that reaches a server URL carrying no
	// credentials.
	fs.Bool("anonymous", false, "connect without credentials, and do not ask for any")
	if c.Flags != nil {
		c.Flags(fs)
	}
	return fs
}
