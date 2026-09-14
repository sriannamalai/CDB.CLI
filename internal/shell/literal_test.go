package shell

import (
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// Issue #53.1: a backslash protects the rune after it, not the word it is in.
func TestABackslashProtectsOnlyTheNextRune(t *testing.T) {
	v := session.NewVars()
	v.Set("var", session.StringValue("value"))
	got, err := expandLine(t, `put "\$literal and $var"`, v, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := "$literal and value"; got[1] != want {
		t.Errorf("argv[1] = %q, want %q", got[1], want)
	}
}

// The same outside quotes: only the escaped "$" survives.
func TestABackslashOutsideQuotesProtectsOnlyTheNextRune(t *testing.T) {
	v := session.NewVars()
	v.Set("var", session.StringValue("value"))
	got, err := expandLine(t, `put \$literal-$var`, v, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := "$literal-value"; got[1] != want {
		t.Errorf("argv[1] = %q, want %q", got[1], want)
	}
}

// And single quotes protect only what is inside them.
func TestSingleQuotesProtectOnlyTheRunesInsideThem(t *testing.T) {
	v := session.NewVars()
	v.Set("b", session.StringValue("value"))
	got, err := expandLine(t, `put '$a'$b`, v, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := "$avalue"; got[1] != want {
		t.Errorf("argv[1] = %q, want %q", got[1], want)
	}
}

// What the marks are there for in the first place still holds: a Mango
// selector's own "$" needs no escaping, quoted or bare.
func TestAMangoOperatorIsStillLeftAlone(t *testing.T) {
	v := session.NewVars()
	v.Set("gt", session.StringValue("no"))
	for _, tc := range []struct{ in, want string }{
		// A bare word loses its quotes the way every shell word does; the
		// operator's "$" is what must survive.
		{`find {"year":{"$gt":2000}}`, `{year:{$gt:2000}}`},
		{`find "{\"year\":{\"$gt\":2000}}"`, `{"year":{"$gt":2000}}`},
		{`find '{"year":{"$gt":2000}}'`, `{"year":{"$gt":2000}}`},
	} {
		got, err := expandLine(t, tc.in, v, nil)
		if err != nil {
			t.Fatalf("%s: %v", tc.in, err)
		}
		if got[1] != tc.want {
			t.Errorf("%s: argv[1] = %q, want %q", tc.in, got[1], tc.want)
		}
	}
}
