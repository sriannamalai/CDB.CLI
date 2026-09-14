package shell

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/session"
)

func expandLine(t *testing.T, input string, vars *session.Vars, args []string) ([]string, error) {
	t.Helper()
	line, err := Parse(input, nil)
	if err != nil {
		t.Fatal(err)
	}
	return expandStage(line.Stages[0], vars, args)
}

func TestExpandReplacesAReferenceInsideAWord(t *testing.T) {
	v := session.NewVars()
	v.Set("id", session.StringValue("tt0211915"))
	got, err := expandLine(t, "cat /movies/$id", v, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1] != "/movies/tt0211915" {
		t.Errorf("argv = %#v", got)
	}
}

func TestExpandNeverReSplitsAWord(t *testing.T) {
	v := session.NewVars()
	v.Set("title", session.StringValue("two words"))
	got, err := expandLine(t, "put $title", v, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1] != "two words" {
		t.Errorf("argv = %#v; a value holding a space stays one argument", got)
	}
}

func TestExpandSkipsSingleQuotedWords(t *testing.T) {
	v := session.NewVars()
	v.Set("a", session.StringValue("no"))
	got, err := expandLine(t, `find '{"selector":{"x":"$a"}}'`, v, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got[1] != `{"selector":{"x":"$a"}}` {
		t.Errorf("argv[1] = %q; single quotes are literal", got[1])
	}
}

func TestExpandInDoubleQuotes(t *testing.T) {
	v := session.NewVars()
	v.Set("a", session.StringValue("yes"))
	got, err := expandLine(t, `put "x $a y"`, v, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got[1] != "x yes y" {
		t.Errorf("argv[1] = %q", got[1])
	}
}

func TestExpandDollarDollarIsALiteralDollar(t *testing.T) {
	got, err := expandLine(t, "put a$$b", session.NewVars(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got[1] != "a$b" {
		t.Errorf("argv[1] = %q", got[1])
	}
}

func TestExpandBracedName(t *testing.T) {
	v := session.NewVars()
	v.Set("id", session.StringValue("a"))
	got, err := expandLine(t, "cat ${id}x", v, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got[1] != "ax" {
		t.Errorf("argv[1] = %q", got[1])
	}
}

func TestExpandPositionalArgumentsAndCount(t *testing.T) {
	got, err := expandLine(t, "put $1 $2 $#", session.NewVars(), []string{"one", "two"})
	if err != nil {
		t.Fatal(err)
	}
	if got[1] != "one" || got[2] != "two" || got[3] != "2" {
		t.Errorf("argv = %#v", got)
	}
	none, err := expandLine(t, "put $#", session.NewVars(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if none[1] != "0" {
		t.Errorf("$# outside a script = %q, want 0", none[1])
	}
}

func TestExpandEnvironment(t *testing.T) {
	t.Setenv("CDB_TEST_EXPAND", "from-env")
	got, err := expandLine(t, "put $env.CDB_TEST_EXPAND", session.NewVars(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got[1] != "from-env" {
		t.Errorf("argv[1] = %q", got[1])
	}
}

func TestExpandUnknownNameFailsTheLine(t *testing.T) {
	for _, in := range []string{"cat $nope", "cat $9", "cat $env.CDB_TEST_NOT_SET_ANYWHERE"} {
		_, err := expandLine(t, in, session.NewVars(), nil)
		if err == nil || !strings.Contains(err.Error(), "is not set.") {
			t.Errorf("%q error = %v", in, err)
		}
	}
}

func TestBindingsReachAJQStage(t *testing.T) {
	v := session.NewVars()
	v.Set("year", session.Value{JSON: json.RawMessage(`2001`)})
	f, err := compileFilter("select(.year > $year - 1) | .year", v.All())
	if err != nil {
		t.Fatal(err)
	}
	vals, err := f.apply(json.RawMessage(`{"year":2001}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(vals) != 1 || string(vals[0]) != "2001" {
		t.Errorf("values = %v", vals)
	}
}

func TestExpandBackslashProtectsADollar(t *testing.T) {
	v := session.NewVars()
	v.Set("a", session.StringValue("no"))
	for _, in := range []string{`put \$a`, `put "\$a"`} {
		got, err := expandLine(t, in, v, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got[1] != "$a" {
			t.Errorf("%s -> %q; a backslash protects a dollar", in, got[1])
		}
	}
}
