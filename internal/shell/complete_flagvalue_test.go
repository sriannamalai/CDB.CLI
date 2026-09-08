package shell

import (
	"testing"
)

func TestCompleteAttachedFlagValue(t *testing.T) {
	sh, _ := completeShell(t)
	sh.sess.SetPath("/mydb")

	line := "find --fields=na"
	word, cands := sh.CompleteLine(t.Context(), line, len(line))
	if word != "--fields=na" {
		t.Fatalf("word = %q", word)
	}
	if len(cands) != 1 {
		t.Fatalf("candidates = %+v", cands)
	}
	if cands[0].Value != "--fields=name" {
		t.Errorf("value = %q, want --fields=name", cands[0].Value)
	}
	if cands[0].Display != "name" {
		t.Errorf("display = %q, want the bare field name", cands[0].Display)
	}
}

// A bool flag has no value worth completing, and an undeclared flag is still a
// flag name rather than a value.
func TestCompleteAttachedValueOnlyForValueFlags(t *testing.T) {
	sh, _ := completeShell(t)
	sh.sess.SetPath("/mydb")

	for _, line := range []string{"find --explain=tr", "find --nosuch=x"} {
		_, cands := sh.CompleteLine(t.Context(), line, len(line))
		if len(cands) != 0 {
			t.Errorf("%q produced %+v, want nothing", line, cands)
		}
	}
}

func TestCompleteCommaSeparatedFieldList(t *testing.T) {
	sh, _ := completeShell(t)
	sh.sess.SetPath("/mydb")

	for _, tc := range []struct{ line, want string }{
		{"find --fields name,ag", "name,age"},
		{"find --fields=name,ag", "--fields=name,age"},
	} {
		_, cands := sh.CompleteLine(t.Context(), tc.line, len(tc.line))
		if len(cands) != 1 || cands[0].Value != tc.want {
			t.Errorf("%q produced %+v, want one candidate %q", tc.line, cands, tc.want)
		}
		if len(cands) == 1 && cands[0].Display != "age" {
			t.Errorf("%q display = %q, want the bare field name", tc.line, cands[0].Display)
		}
	}
}
