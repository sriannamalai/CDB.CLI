package session

import (
	"encoding/json"
	"testing"
)

func TestVarsTextUnquotesAStringAndKeepsEverythingElseAsJSON(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`"1-abc"`, "1-abc"},
		{`2001`, "2001"},
		{`{"a":1}`, `{"a":1}`},
		{`null`, "null"},
	} {
		if got := (Value{JSON: json.RawMessage(tc.in)}).Text(); got != tc.want {
			t.Errorf("Value(%s).Text() = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestVarsAnyKeepsTheType(t *testing.T) {
	if v, ok := (Value{JSON: json.RawMessage(`2001`)}).Any().(float64); !ok || v != 2001 {
		t.Errorf("Any() = %#v; a number must reach jq as a number", v)
	}
	if _, ok := (Value{JSON: json.RawMessage(`{"a":1}`)}).Any().(map[string]any); !ok {
		t.Error("an object must reach jq as an object")
	}
}

func TestVarsChildReadsThroughAndWritesLocally(t *testing.T) {
	parent := NewVars()
	parent.Set("year", Value{JSON: json.RawMessage(`2001`)})
	child := parent.Child()
	if v, ok := child.Lookup("year"); !ok || v.Text() != "2001" {
		t.Fatalf("child lookup = %v, %v", v, ok)
	}
	child.Set("year", Value{JSON: json.RawMessage(`1999`)})
	child.Set("only", StringValue("here"))
	if v, _ := parent.Lookup("year"); v.Text() != "2001" {
		t.Errorf("the child wrote through to its parent: %q", v.Text())
	}
	if _, ok := parent.Lookup("only"); ok {
		t.Error("the child defined a name in its parent")
	}
	child.Unset("year")
	if v, ok := child.Lookup("year"); !ok || v.Text() != "2001" {
		t.Errorf("unsetting a name locally must reveal the parent's again: %v, %v", v, ok)
	}
}

func TestVarsListIsSortedAndNearestWins(t *testing.T) {
	parent := NewVars()
	parent.Set("b", StringValue("parent"))
	parent.Set("a", StringValue("a"))
	child := parent.Child()
	child.Set("b", StringValue("child"))
	got := child.List()
	if len(got) != 2 || got[0].Name != "a" || got[1].Name != "b" || got[1].Value.Text() != "child" {
		t.Errorf("List() = %#v", got)
	}
}

func TestVarsAllExcludesEnv(t *testing.T) {
	v := NewVars()
	v.Set("env", StringValue("no"))
	v.Set("year", Value{JSON: json.RawMessage(`2001`)})
	all := v.All()
	if _, ok := all["env"]; ok {
		t.Error("binding $env into jq would shadow what jq means by it")
	}
	if all["year"] != float64(2001) {
		t.Errorf("All()[year] = %#v", all["year"])
	}
}

func TestMaskedNames(t *testing.T) {
	for _, name := range []string{"password", "db_password", "API_TOKEN", "clientSecret"} {
		if !Masked(name) {
			t.Errorf("Masked(%q) = false", name)
		}
	}
	for _, name := range []string{"year", "rev", "tokens", "secretive"} {
		if Masked(name) {
			t.Errorf("Masked(%q) = true", name)
		}
	}
}

func TestValidName(t *testing.T) {
	for _, name := range []string{"a", "_x", "db_password", "x9"} {
		if !ValidName(name) {
			t.Errorf("ValidName(%q) = false", name)
		}
	}
	for _, name := range []string{"", "9x", "a-b", "a.b", "$a"} {
		if ValidName(name) {
			t.Errorf("ValidName(%q) = true", name)
		}
	}
}
