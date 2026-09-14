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
// replaced. The "$" runes the parser marked literal — single-quoted, escaped,
// or a Mango operator's own — are left as they were typed.
func expandStage(st Stage, vars *session.Vars, args []string) ([]string, error) {
	out := make([]string, len(st.Argv))
	for i, word := range st.Argv {
		var dollars []int
		if i < len(st.LiteralDollar) {
			dollars = st.LiteralDollar[i]
		}
		v, err := expandWord(word, dollars, vars, args)
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
// begins no reference is left as it was. dollars holds the offsets the parser
// marked literal: every "$" written right after a double quote, which is how a
// Mango selector spells "$gt", inside single quotes, or after a backslash.
func expandWord(word string, dollars []int, vars *session.Vars, args []string) (string, error) {
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
		if literalDollarAt(dollars, i) {
			b.WriteByte('$')
			continue
		}
		name, next, ok, err := referenceAt(word, i+1)
		if err != nil {
			return "", err
		}
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

// literalDollarAt reports whether the "$" at offset i is one the parser marked
// literal. The offsets are in the order the parser wrote them, and a word holds
// a handful at most, so a scan is the whole of it.
func literalDollarAt(dollars []int, i int) bool {
	for _, d := range dollars {
		if d == i {
			return true
		}
	}
	return false
}

// referenceAt reads the name of the reference beginning at i, which is just
// past the "$". next is the index after the name. ok is false when nothing
// there is a reference at all, which leaves the "$" as it was typed; an error
// is a reference that was meant and written wrong.
func referenceAt(word string, i int) (name string, next int, ok bool, err error) {
	if i >= len(word) {
		return "", i, false, nil
	}
	if word[i] == '{' {
		end := strings.IndexByte(word[i:], '}')
		if end < 2 {
			return "", i, false, nil
		}
		// The braces say a reference was meant, so what is between them has to
		// be a name: nothing else can be read as anything.
		name = word[i+1 : i+end]
		if !isName(name) {
			return "", i, false, notAName(name)
		}
		return name, i + end + 1, true, nil
	}
	if word[i] == '#' {
		return "#", i + 1, true, nil
	}
	// A name cannot begin with a digit, so a digit is one positional argument
	// and whatever follows it is ordinary text: "$1abc" is argument 1 then
	// "abc", and "$12" is argument 1 then "2".
	if word[i] >= '0' && word[i] <= '9' {
		return word[i : i+1], i + 1, true, nil
	}
	j := i
	for j < len(word) && isNameByte(word[j]) {
		j++
	}
	if j == i {
		return "", i, false, nil
	}
	name = word[i:j]
	// "env" owns everything after its dot, and nothing else: "$env" on its own
	// names no variable, and valueOf says so.
	if name == "env" && j < len(word) && word[j] == '.' {
		k := j + 1
		for k < len(word) && isNameByte(word[k]) {
			k++
		}
		if k > j+1 {
			return word[i:k], k, true, nil
		}
	}
	return name, j, true, nil
}

func isNameByte(c byte) bool {
	return c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// isName reports whether a braced reference holds something that could be
// resolved: a variable name, one positional digit, "#", or "env." and a name.
func isName(name string) bool {
	switch {
	case name == "#":
		return true
	case len(name) == 1 && name[0] >= '0' && name[0] <= '9':
		return true
	case strings.HasPrefix(name, "env."):
		return varNamePattern.MatchString(strings.TrimPrefix(name, "env."))
	}
	return varNamePattern.MatchString(name)
}

// notAName is the sentence for a braced reference holding something that is
// not a name.
func notAName(name string) error {
	return &command.UsageError{Reason: fmt.Sprintf("%q is not a variable name.", name)}
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
	case name == "env":
		// The environment owns the name, in a command stage as in a jq stage,
		// so a user variable called "env" is out of reach rather than shadowed
		// by whichever stage read it.
		return "", &command.UsageError{Reason: `"env" is reserved; write $env.NAME.`}
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
	return &command.UsageError{Reason: fmt.Sprintf(`variable %q is not set. Write '…' or \$%s for a literal $.`, name, name)}
}
