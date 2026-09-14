// Package shell implements the interactive cdb shell: line editing, parsing,
// gojq filters, and completion.
package shell

import (
	"errors"
	"regexp"
	"strings"
)

// ErrUnterminatedQuote is returned when a line ends inside a quoted string.
var ErrUnterminatedQuote = errors.New("unterminated quote")

// ErrLeadingPipe is returned for a line whose first stage is empty.
var ErrLeadingPipe = errors.New("a line cannot start with |")

// ErrTrailingPipe is returned for a line whose last stage is empty.
var ErrTrailingPipe = errors.New("a line cannot end with |")

// Stage is one stage of a pipeline. Exactly one of Argv and Expr is set: a
// command stage carries the command name followed by its arguments and flags,
// a jq stage carries one gojq expression, taken verbatim.
type Stage struct {
	Argv []string
	// LiteralDollar[i] holds the byte offsets inside word i of every "$" that
	// expansion must leave alone: one written immediately after a double
	// quote, which is how Mango spells its operators ("$gt", "$and") in a bare
	// word {"year":{"$gt":2000}} and in a double-quoted word with escaped
	// quotes alike; one written inside single quotes; and one a backslash
	// protected. The mark is per rune, not per word, so `"\$literal and $var"`
	// keeps the first and expands the second, and '$a'$b expands $b. A
	// word-opening double quote is not part of the word, so "$doc" still
	// expands.
	LiteralDollar [][]int
	Expr          string
	// Capture is the raw pipeline text of a "set <name> = <pipeline>" line,
	// kept exactly as it was typed. It is set on no other stage. "set" parses
	// it into a Line of its own when it runs, which is why the operator needs
	// no quoting around a pipeline that contains "|".
	Capture string
}

// varNamePattern is what a variable name looks like. It is spelled here as well
// as in internal/session because the parser has to recognise "set <name> = …"
// before any command has run, and reaching into the session package for a name
// check is a layering this file has no other reason to take on.
var varNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// cutWord takes the next whitespace-separated word, with no quote handling: the
// three words captureLine looks at are "set", a variable name and "=", none of
// which can be quoted and still be those three words.
func cutWord(s string) (word, rest string, ok bool) {
	s = strings.TrimLeft(s, " \t")
	if s == "" {
		return "", "", false
	}
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return s[:i], s[i:], true
	}
	return s, "", true
}

// captureLine recognises "set <name> = <pipeline>", the one line whose
// remainder is not split into stages. Everything after the "=" is handed back
// whole, pipes and quotes intact, so that capturing a pipeline needs no quoting
// from the operator; "set" re-parses it at run time. ok is false for every
// other line, which then parses by the ordinary rules — "set year 2001" and
// "set" included.
func captureLine(input string) (argv []string, capture string, ok bool) {
	word, rest, found := cutWord(input)
	if !found || word != "set" {
		return nil, "", false
	}
	name, rest, found := cutWord(rest)
	if !found || !varNamePattern.MatchString(name) {
		return nil, "", false
	}
	eq, rest, found := cutWord(rest)
	if !found || eq != "=" {
		return nil, "", false
	}
	capture = strings.TrimSpace(rest)
	if capture == "" {
		return nil, "", false
	}
	return []string{"set", name, "="}, capture, true
}

// Line is a parsed shell input line: one or more stages separated by an
// unquoted "|".
type Line struct {
	Stages []Stage
}

// IsCommand reports whether stage n is a command stage.
func (l Line) IsCommand(n int) bool {
	return n >= 0 && n < len(l.Stages) && l.Stages[n].Argv != nil
}

// Parse splits a shell input line into stages separated by unquoted "|" runes.
// Words are separated by unquoted whitespace. Single quotes are literal; double
// quotes allow backslash escapes; a backslash outside quotes escapes the next
// rune. Stage 1 is always a command stage; every later stage is a command stage
// when isCommand reports its first word to be a registered command, and a jq
// stage holding the stage's raw text otherwise. A nil isCommand makes every
// later stage a jq stage.
//
// "set <name> = <pipeline>" is the one exception: it is a single stage whose
// Capture holds the pipeline text verbatim, so that capturing needs no quoting.
func Parse(input string, isCommand func(name string) bool) (Line, error) {
	if argv, capture, ok := captureLine(input); ok {
		return Line{Stages: []Stage{{Argv: argv, Capture: capture}}}, nil
	}
	var (
		line     Line
		cur      strings.Builder
		argv     []string
		dollarOf [][]int
		dollars  []int
		hasWord  bool
		// quotedDollar says the last rune added to the word was a double
		// quote, so a "$" added next is a Mango operator's own "$".
		quotedDollar bool
		// anyDollar says the stage marked at least one "$" literal, which is
		// the only reason to hand LiteralDollar to the stage at all.
		anyDollar bool
		quote     rune
		escaped   bool
	)
	runes := []rune(input)
	// start is where the current stage's raw text begins, so that a jq stage
	// can be handed to gojq exactly as it was typed.
	start := 0
	// write adds one rune to the word being built. literal says the rune was
	// written inside single quotes or after a backslash; a "$" that is literal,
	// or that follows a double quote, has its offset recorded so that
	// expansion leaves that one rune alone.
	write := func(r rune, literal bool) {
		if r == '$' && (literal || quotedDollar) {
			dollars = append(dollars, cur.Len())
			anyDollar = true
		}
		quotedDollar = r == '"'
		cur.WriteRune(r)
		hasWord = true
	}
	flush := func() {
		if hasWord {
			argv = append(argv, cur.String())
			dollarOf = append(dollarOf, dollars)
			cur.Reset()
			dollars = nil
			hasWord, quotedDollar = false, false
		}
	}
	// closeStage ends the stage that began at start and runs to end.
	closeStage := func(end int) error {
		flush()
		raw := strings.TrimSpace(string(runes[start:end]))
		if len(argv) == 0 && raw == "" {
			if len(line.Stages) == 0 {
				return ErrLeadingPipe
			}
			return ErrTrailingPipe
		}
		if len(line.Stages) > 0 && (isCommand == nil || len(argv) == 0 || !isCommand(argv[0])) {
			line.Stages = append(line.Stages, Stage{Expr: raw})
		} else {
			marked := dollarOf
			if !anyDollar {
				marked = nil
			}
			line.Stages = append(line.Stages, Stage{Argv: argv, LiteralDollar: marked})
		}
		argv, dollarOf, anyDollar = nil, nil, false
		return nil
	}
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case escaped:
			// A backslash protects the rune after it and nothing else, so
			// `"\$literal and $var"` keeps the first "$" and expands the
			// second.
			write(r, true)
			escaped = false
		case quote == '\'':
			if r == '\'' {
				quote, quotedDollar = 0, false
			} else {
				write(r, true)
			}
		case quote == '"':
			switch r {
			case '"':
				quote, quotedDollar = 0, false
			case '\\':
				escaped = true
			default:
				write(r, false)
			}
		case r == '\\':
			escaped = true
		case r == '\'' || r == '"':
			quote = r
			// A quote that opens the word is not part of it, so a "$" right
			// after it is an ordinary reference; one that opens a quoted run
			// inside the word is the quote a Mango operator is written after.
			quotedDollar = r == '"' && cur.Len() > 0
			hasWord = true
		case r == '#' && !hasWord:
			// A "#" that begins a token starts a comment, which runs to the
			// end of its own physical line: the rest of a continued line is
			// still input. Inside quotes, after a backslash, or in the middle
			// of a word ("a#b") it is an ordinary character — the rule jq
			// applies to its own comments.
			for i+1 < len(runes) && runes[i+1] != '\n' {
				i++
			}
		case r == '|':
			if err := closeStage(i); err != nil {
				return Line{}, err
			}
			start = i + 1
		case r == ' ' || r == '\t' || r == '\n':
			flush()
		default:
			write(r, false)
		}
	}
	if quote != 0 || escaped {
		return Line{}, ErrUnterminatedQuote
	}
	// A line that is entirely blank is not a pipeline error; it is a no-op, and
	// RunLine has always treated it as one. A line of nothing but a comment is
	// the same no-op, which is why the words are counted rather than the text.
	if len(line.Stages) == 0 && len(argv) == 0 && !hasWord {
		return Line{}, nil
	}
	if err := closeStage(len(runes)); err != nil {
		return Line{}, err
	}
	return line, nil
}

// NeedsMore reports whether the shell should read another line before parsing.
// It is true when the buffer ends in an odd number of backslashes, ends inside
// a quote, or has unbalanced JSON braces or brackets outside quotes. A comment
// counts for none of those: what Parse will throw away cannot ask for another
// line.
func NeedsMore(input string) bool {
	var (
		quote   rune
		escaped bool
		depth   int
		// atTokenStart says the next rune would begin a word, which is where
		// a "#" is a comment rather than a character.
		atTokenStart = true
	)
	runes := []rune(input)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case escaped:
			escaped = false
			atTokenStart = false
		case quote != 0:
			if r == '\\' && quote == '"' {
				escaped = true
			} else if r == quote {
				quote = 0
			}
			atTokenStart = false
		case r == '#' && atTokenStart:
			for i+1 < len(runes) && runes[i+1] != '\n' {
				i++
			}
		case r == '\\':
			escaped = true
			atTokenStart = false
		case r == '\'' || r == '"':
			quote = r
			atTokenStart = false
		case r == ' ' || r == '\t' || r == '\n' || r == '|':
			atTokenStart = true
		case r == '{' || r == '[':
			depth++
			atTokenStart = false
		case r == '}' || r == ']':
			if depth > 0 {
				depth--
			}
			atTokenStart = false
		default:
			atTokenStart = false
		}
	}
	if escaped {
		return true
	}
	return quote != 0 || depth > 0
}
