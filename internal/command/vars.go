package command

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// maskedValue is what a variable whose name says it holds a credential prints
// as, in "set"'s listing and in its --json output alike.
const maskedValue = "****"

// setDetails is the long help for set.
const setDetails = `"set" alone lists the variables, sorted; a name ending in "password", "secret"
or "token" prints as **** and is recorded in the history that way too.

"set <name> = <pipeline>" runs the pipeline and stores what it produced: one
value as that value, several as a JSON array. Everything after the "=" is the
pipeline, taken exactly as it was typed, so nothing needs quoting:
set rev = cat /movies/a | ._rev.

A variable is read as $name or ${name} inside a word of a command stage, never
inside single quotes, and $$ is a literal $. In a jq stage every variable is
bound as a jq variable of the same name with its stored value, so numbers stay
numbers. Nothing is persisted, and a script gets its own scope.`

// SetFrom returns the set command. capture runs a pipeline and returns the
// values its last stage produced, which is what "set <name> = <pipeline>"
// stores; a nil capture makes that form unavailable, which is what Default()
// registers before the shell swaps it out with the live runner.
func SetFrom(capture func(ctx context.Context, line string) ([]json.RawMessage, error)) Command {
	return Command{
		Name:    "set",
		Summary: "Define, list or capture a shell variable",
		Example: `admin@localhost:5984:/movies> set year 2001
admin@localhost:5984:/movies> find --field year | select(.year > $year)
admin@localhost:5984:/movies> set rev = cat tt0211915 | ._rev
admin@localhost:5984:/movies> set
 NAME | VALUE
------+-----------
 rev  | 1-fe587ae
 year | 2001`,
		Details:   setDetails,
		Usage:     "[<name> [=] <value...>]",
		MinArgs:   0,
		MaxArgs:   -1,
		ShellOnly: true,
		Run: func(ctx context.Context, s *session.Session, inv Invocation) (Result, error) {
			if len(inv.Args) == 0 {
				return listVars(s), nil
			}
			name := inv.Arg(0)
			if !session.ValidName(name) {
				return nil, Usagef("set", "%q is not a variable name; a name starts with a letter or \"_\" and holds letters, digits and \"_\".", name)
			}
			rest := inv.Args[1:]
			if len(rest) == 0 {
				return nil, Usagef("set", "set <name> <value...>, or \"set\" alone to list.")
			}
			if rest[0] == "=" {
				if capture == nil {
					return nil, Usagef("set", "capturing a pipeline works inside the shell and in a script.")
				}
				// The shell's parser keeps everything after the "=" whole on the
				// stage and hands it over here, pipes and quotes intact, so the
				// operator quotes nothing. A caller with no parser behind it — a
				// direct Run from a test — still gets the words, rejoined.
				text := inv.Capture
				if text == "" {
					text = strings.Join(rest[1:], " ")
				}
				if strings.TrimSpace(text) == "" {
					return nil, Usagef("set", "set <name> = <pipeline> needs a pipeline to run.")
				}
				vals, err := capture(ctx, text)
				if err != nil {
					return nil, err
				}
				s.Vars.Set(name, captured(vals))
				return Message{Text: fmt.Sprintf("Set %s.", name)}, nil
			}
			s.Vars.Set(name, session.StringValue(strings.Join(rest, " ")))
			return Message{Text: fmt.Sprintf("Set %s.", name)}, nil
		},
	}
}

// Set returns the set command with no pipeline runner behind it.
func Set() Command { return SetFrom(nil) }

// captured turns the values a pipeline produced into one stored value: none is
// the empty string, one is that value, several are a JSON array.
func captured(vals []json.RawMessage) session.Value {
	switch len(vals) {
	case 0:
		return session.StringValue("")
	case 1:
		return session.Value{JSON: vals[0]}
	}
	b := []byte{'['}
	for i, v := range vals {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, v...)
	}
	b = append(b, ']')
	return session.Value{JSON: json.RawMessage(b)}
}

// listVars renders the variable table. A masked value is replaced before it
// reaches either the cells or the JSON, so no caller can print it by asking
// for --json.
func listVars(s *session.Session) Result {
	bindings := s.Vars.List()
	if len(bindings) == 0 {
		return Message{Text: "No variables are set."}
	}
	rows := Rows{Columns: []Column{{Title: "name"}, {Title: "value"}}}
	for _, b := range bindings {
		text := b.Value.Text()
		if session.Masked(b.Name) {
			text = maskedValue
		}
		rows.Items = append(rows.Items, Row{
			Cells: []string{b.Name, text},
			JSON:  jsonObject("name", b.Name, "value", text),
		})
	}
	return rows
}

// Unset returns the unset command.
func Unset() Command {
	return Command{
		Name:    "unset",
		Summary: "Remove a shell variable",
		Example: `admin@localhost:5984:/movies> unset year`,
		Details: `Removing a name that was never set is not an error. Inside a script, unset
removes the script's own definition and reveals the caller's again, if there
was one.`,
		Usage:     "<name>",
		MinArgs:   1,
		MaxArgs:   1,
		ShellOnly: true,
		Run: func(_ context.Context, s *session.Session, inv Invocation) (Result, error) {
			name := inv.Arg(0)
			if !session.ValidName(name) {
				return nil, Usagef("unset", "%q is not a variable name.", name)
			}
			s.Vars.Unset(name)
			return Message{Text: fmt.Sprintf("Unset %s.", name)}, nil
		},
	}
}
