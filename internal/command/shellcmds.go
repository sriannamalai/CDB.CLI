package command

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// ErrExit tells the shell loop to stop.
var ErrExit = errors.New("exit")

// Help returns the help command, which lists the registry it is built from.
func Help(reg *Registry) Command {
	return Command{
		Name:      "help",
		Aliases:   []string{"?"},
		Summary:   "List commands, or explain one command",
		Usage:     "[command]",
		MinArgs:   0,
		MaxArgs:   1,
		ShellOnly: true,
		Complete: func(_ context.Context, _ *session.Session, _ []string, cur string) []Candidate {
			var out []Candidate
			for _, c := range reg.All() {
				if strings.HasPrefix(c.Name, cur) {
					out = append(out, Candidate{Value: c.Name, Description: c.Summary, Tag: "commands"})
				}
			}
			return out
		},
		Run: func(_ context.Context, _ *session.Session, inv Invocation) (Result, error) {
			if name := inv.Arg(0); name != "" {
				c, ok := reg.Lookup(name)
				if !ok {
					return nil, Usagef("help", "no command named %q", name)
				}
				text := fmt.Sprintf("%s %s\n  %s", c.Name, c.Usage, c.Summary)
				if len(c.Aliases) > 0 {
					text += "\n  aliases: " + strings.Join(c.Aliases, ", ")
				}
				if c.Flags != nil {
					fs := reg.NewFlagSet(c)
					text += "\n\n" + fs.FlagUsages()
				}
				return Message{Text: text}, nil
			}
			rows := Rows{Columns: []Column{{Title: "command"}, {Title: "summary"}}}
			for _, c := range reg.All() {
				rows.Items = append(rows.Items, Row{
					Cells: []string{c.Name, c.Summary},
					JSON:  jsonObject("command", c.Name, "summary", c.Summary),
				})
			}
			rows.Hint = "help <command> explains one command"
			return rows, nil
		},
	}
}

// HistoryFrom returns the history command reading from lines, which the shell
// supplies from its live, deduplicated history source. A nil lines function
// means there is no shell history to show, which is what Default() registers
// before shell.New swaps it out with Registry.Replace.
func HistoryFrom(lines func() []string) Command {
	return Command{
		Name:      "history",
		Summary:   "Show the command history",
		ShellOnly: true,
		MinArgs:   0,
		MaxArgs:   0,
		Run: func(_ context.Context, _ *session.Session, _ Invocation) (Result, error) {
			if lines == nil {
				return Message{Text: "History is only available inside the interactive shell."}, nil
			}
			all := lines()
			if len(all) == 0 {
				return Message{Text: "The history is empty."}, nil
			}
			rows := Rows{Columns: []Column{{Title: "n", Align: AlignRight}, {Title: "line"}}}
			for i, line := range all {
				n := strconv.Itoa(i + 1)
				rows.Items = append(rows.Items, Row{
					Cells: []string{n, line},
					JSON:  jsonObject("n", n, "line", line),
				})
			}
			return rows, nil
		},
	}
}

// History returns the history command with no live source behind it.
func History() Command { return HistoryFrom(nil) }

// Clear returns the clear command.
func Clear() Command {
	return Command{
		Name:      "clear",
		Summary:   "Clear the screen",
		ShellOnly: true,
		MinArgs:   0,
		MaxArgs:   0,
		Run: func(_ context.Context, s *session.Session, _ Invocation) (Result, error) {
			// ESC[H moves the cursor home; ESC[2J erases the display.
			fmt.Fprint(s.Stdout, "\x1b[H\x1b[2J")
			return Empty{}, nil
		},
	}
}

// Exit returns the exit command.
func Exit() Command {
	return Command{
		Name:      "exit",
		Aliases:   []string{"quit"},
		Summary:   "Leave the shell",
		ShellOnly: true,
		MinArgs:   0,
		MaxArgs:   0,
		Run: func(context.Context, *session.Session, Invocation) (Result, error) {
			return nil, ErrExit
		},
	}
}
