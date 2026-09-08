// Package shell implements the interactive cdb shell: line editing, parsing,
// gojq filters, and completion.
package shell

import (
	"errors"
	"strings"
)

// ErrUnterminatedQuote is returned when a line ends inside a quoted string.
var ErrUnterminatedQuote = errors.New("unterminated quote")

// Line is a parsed shell input line.
type Line struct {
	// Argv is the command name followed by its arguments and flags.
	Argv []string
	// Filter is the gojq expression after the single "|" stage, or "".
	Filter string
}

// Parse splits a shell input line into argv plus at most one trailing gojq
// filter stage. Words are separated by unquoted whitespace. Single quotes are
// literal; double quotes allow backslash escapes; a backslash outside quotes
// escapes the next rune. The first unquoted "|" ends argv and starts the
// filter, which is taken verbatim to end of line.
func Parse(input string) (Line, error) {
	var (
		line    Line
		cur     strings.Builder
		hasWord bool
		quote   rune
		escaped bool
	)
	flush := func() {
		if hasWord {
			line.Argv = append(line.Argv, cur.String())
			cur.Reset()
			hasWord = false
		}
	}
	runes := []rune(input)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case escaped:
			cur.WriteRune(r)
			hasWord = true
			escaped = false
		case quote == '\'':
			if r == '\'' {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case quote == '"':
			switch r {
			case '"':
				quote = 0
			case '\\':
				escaped = true
			default:
				cur.WriteRune(r)
			}
		case r == '\\':
			escaped = true
		case r == '\'' || r == '"':
			quote = r
			hasWord = true
		case r == '|':
			flush()
			line.Filter = strings.TrimSpace(string(runes[i+1:]))
			return line, nil
		case r == ' ' || r == '\t' || r == '\n':
			flush()
		default:
			cur.WriteRune(r)
			hasWord = true
		}
	}
	if quote != 0 || escaped {
		return Line{}, ErrUnterminatedQuote
	}
	flush()
	return line, nil
}

// NeedsMore reports whether the shell should read another line before parsing.
// It is true when the buffer ends in an odd number of backslashes, ends inside
// a quote, or has unbalanced JSON braces or brackets outside quotes.
func NeedsMore(input string) bool {
	var (
		quote   rune
		escaped bool
		depth   int
	)
	for _, r := range input {
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
		case r == '{' || r == '[':
			depth++
		case r == '}' || r == ']':
			if depth > 0 {
				depth--
			}
		}
	}
	if escaped {
		return true
	}
	return quote != 0 || depth > 0
}
