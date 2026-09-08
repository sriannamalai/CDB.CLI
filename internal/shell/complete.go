package shell

import (
	"context"
	"strings"
	"time"

	"github.com/reeflective/readline"
	"github.com/spf13/pflag"
	"github.com/sriannamalai/CDB.CLI/internal/command"
)

// completionTimeout bounds the server lookups completion makes. Completion
// runs while the line editor owns the terminal, so a slow or unreachable
// server must not hold the keystroke: whatever is already cached is offered
// instead.
const completionTimeout = 2 * time.Second

// CompleteLine returns the word under the cursor and the candidates for it.
// It is the pure part of completion, so tests do not need a terminal.
func (sh *Shell) CompleteLine(ctx context.Context, line string, cursor int) (string, []command.Candidate) {
	if cursor > len(line) {
		cursor = len(line)
	}
	if cursor < 0 {
		cursor = 0
	}
	head := line[:cursor]
	// Everything after an unquoted "|" is a gojq expression, not a command.
	if unquotedPipe(head) >= 0 {
		return "", nil
	}
	fields, word := splitForCompletion(head)

	// Completing the command name itself.
	if len(fields) == 0 {
		var out []command.Candidate
		for _, c := range sh.reg.All() {
			if strings.HasPrefix(c.Name, word) {
				out = append(out, command.Candidate{Value: c.Name, Description: describe(c), Tag: "commands"})
			}
		}
		return word, out
	}

	c, ok := sh.reg.Lookup(fields[0])
	if !ok {
		return word, nil
	}

	// An attached flag value ("--fields=na") is not a flag name: it is a value
	// for a declared flag, and only the command's own Complete knows what
	// belongs there. Without this the word falls into the flag-name branch
	// below and matches nothing, because no flag is called "fields=na".
	if name, value, ok := attachedFlagValue(sh.reg.NewFlagSet(c), word); ok {
		if c.Complete == nil {
			return word, nil
		}
		settled := append(append([]string{}, fields[1:]...), "--"+name)
		cands := c.Complete(ctx, sh.sess, settled, value)
		prefix := "--" + name + "="
		for i := range cands {
			// Only the value the editor will insert carries the prefix;
			// Display is left alone so the menu still shows bare names.
			if cands[i].Display == "" {
				cands[i].Display = cands[i].Value
			}
			cands[i].Value = prefix + cands[i].Value
		}
		return word, cands
	}

	// Completing a flag name.
	if strings.HasPrefix(word, "-") {
		var out []command.Candidate
		sh.reg.NewFlagSet(c).VisitAll(func(f *pflag.Flag) {
			name := "--" + f.Name
			if strings.HasPrefix(name, word) {
				out = append(out, command.Candidate{Value: name, Description: f.Usage, Tag: "flags"})
			}
		})
		return word, out
	}

	if c.Complete == nil {
		return word, nil
	}
	return word, c.Complete(ctx, sh.sess, fields[1:], word)
}

// describe is the description shown beside a command name. A destructive
// command carries the same "!" marker help puts in its own column.
func describe(c command.Command) string {
	if c.Destructive {
		return command.DestructiveMarker + " " + c.Summary
	}
	return c.Summary
}

// splitForCompletion returns the settled words before the cursor and the word
// the cursor is inside.
func splitForCompletion(head string) ([]string, string) {
	parsed, err := Parse(head)
	if err != nil {
		// An unterminated quote means the last word is still open; complete on
		// what follows the quote character.
		if i := strings.LastIndexAny(head, "\"'"); i >= 0 {
			before, _ := Parse(head[:i])
			return before.Argv, head[i+1:]
		}
		return nil, ""
	}
	if strings.HasSuffix(head, " ") {
		return parsed.Argv, ""
	}
	if len(parsed.Argv) == 0 {
		return nil, ""
	}
	return parsed.Argv[:len(parsed.Argv)-1], parsed.Argv[len(parsed.Argv)-1]
}

// attachedFlagValue splits "--name=value" when name is a declared flag of the
// command that takes a value. A bool flag is excluded: "--explain=true" is
// legal pflag syntax, but there is nothing to complete after the "=".
func attachedFlagValue(fs *pflag.FlagSet, word string) (name, value string, ok bool) {
	if !strings.HasPrefix(word, "--") {
		return "", "", false
	}
	name, value, found := strings.Cut(strings.TrimPrefix(word, "--"), "=")
	if !found || name == "" {
		return "", "", false
	}
	f := fs.Lookup(name)
	if f == nil || f.Value.Type() == "bool" {
		return "", "", false
	}
	return name, value, true
}

// unquotedPipe reports the index of the first "|" outside quotes, or -1.
func unquotedPipe(s string) int {
	var quote rune
	escaped := false
	for i, r := range s {
		switch {
		case escaped:
			escaped = false
		case quote != 0:
			if r == '\\' && quote == '"' {
				escaped = true
			} else if r == quote {
				quote = 0
			}
		case r == '\\':
			escaped = true
		case r == '\'' || r == '"':
			quote = r
		case r == '|':
			return i
		}
	}
	return -1
}

// complete adapts CompleteLine to reeflective/readline. It never prints and
// never prompts: the line editor owns the terminal while it runs.
func (sh *Shell) complete(line []rune, cursor int) readline.Completions {
	ctx, cancel := context.WithTimeout(context.Background(), completionTimeout)
	defer cancel()
	// readline counts the cursor in runes, CompleteLine in bytes.
	runes := []rune(string(line))
	if cursor > len(runes) {
		cursor = len(runes)
	}
	if cursor < 0 {
		cursor = 0
	}
	_, cands := sh.CompleteLine(ctx, string(runes), len(string(runes[:cursor])))
	if len(cands) == 0 {
		return readline.Completions{}
	}
	values := make([]readline.Completion, 0, len(cands))
	for _, c := range cands {
		display := c.Display
		if display == "" {
			display = c.Value
		}
		values = append(values, readline.Completion{
			Value:       c.Value,
			Display:     display,
			Description: c.Description,
			Tag:         c.Tag,
		})
	}
	return readline.CompleteRaw(values)
}
