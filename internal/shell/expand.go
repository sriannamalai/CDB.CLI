package shell

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/sriannamalai/CDB.CLI/internal/command"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// expandStage returns the stage's words with every variable reference
// replaced. A word any part of which was single-quoted is returned untouched.
func expandStage(st Stage, vars *session.Vars, args []string) ([]string, error) {
	out := make([]string, len(st.Argv))
	for i, word := range st.Argv {
		if i < len(st.Literal) && st.Literal[i] {
			out[i] = word
			continue
		}
		v, err := expandWord(word, vars, args)
		if err != nil {
			return nil, err
		}
		out[i] = v
	}
	return out, nil
}

// expandWord replaces every reference in one word. A value replaces the
// reference inside the word and the word is never re-split, so a variable
// holding a space is still one argument. "$$" is a literal "$", and a "$" that
// begins no reference is left as it was.
func expandWord(word string, vars *session.Vars, args []string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(word); i++ {
		if word[i] != '$' {
			b.WriteByte(word[i])
			continue
		}
		if i+1 < len(word) && word[i+1] == '$' {
			b.WriteByte('$')
			i++
			continue
		}
		name, next, ok := referenceAt(word, i+1)
		if !ok {
			b.WriteByte('$')
			continue
		}
		text, err := valueOf(name, vars, args)
		if err != nil {
			return "", err
		}
		b.WriteString(text)
		i = next - 1
	}
	return b.String(), nil
}

// referenceAt reads the name of the reference beginning at i, which is just
// past the "$". next is the index after the name.
func referenceAt(word string, i int) (name string, next int, ok bool) {
	if i >= len(word) {
		return "", i, false
	}
	if word[i] == '{' {
		end := strings.IndexByte(word[i:], '}')
		if end < 2 {
			return "", i, false
		}
		return word[i+1 : i+end], i + end + 1, true
	}
	if word[i] == '#' {
		return "#", i + 1, true
	}
	if strings.HasPrefix(word[i:], "env.") {
		j := i + len("env.")
		for j < len(word) && isNameByte(word[j]) {
			j++
		}
		if j == i+len("env.") {
			return "", i, false
		}
		return word[i:j], j, true
	}
	j := i
	for j < len(word) && isNameByte(word[j]) {
		j++
	}
	if j == i {
		return "", i, false
	}
	return word[i:j], j, true
}

func isNameByte(c byte) bool {
	return c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// valueOf resolves one reference to the text it expands to.
func valueOf(name string, vars *session.Vars, args []string) (string, error) {
	switch {
	case name == "#":
		return strconv.Itoa(len(args)), nil
	case len(name) == 1 && name[0] >= '1' && name[0] <= '9':
		n := int(name[0] - '0')
		if n > len(args) {
			return "", unsetVariable(name)
		}
		return args[n-1], nil
	case strings.HasPrefix(name, "env."):
		if v, ok := os.LookupEnv(strings.TrimPrefix(name, "env.")); ok {
			return v, nil
		}
		return "", unsetVariable(name)
	}
	if vars != nil {
		if v, ok := vars.Lookup(name); ok {
			return v.Text(), nil
		}
	}
	return "", unsetVariable(name)
}

// unsetVariable is the sentence for a reference with nothing behind it. It is
// a usage error: the line was written wrong, so the exit code is 2.
func unsetVariable(name string) error {
	return &command.UsageError{Reason: fmt.Sprintf("variable %q is not set.", name)}
}
