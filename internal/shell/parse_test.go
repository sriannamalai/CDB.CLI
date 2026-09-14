package shell

import (
	"errors"
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	isCommand := func(name string) bool {
		switch name {
		case "ls", "cat", "put", "find", "rm":
			return true
		}
		return false
	}
	tests := []struct {
		name  string
		input string
		want  []Stage
	}{
		{"empty", "", nil},
		{"simple", "ls", []Stage{{Argv: []string{"ls"}}}},
		{"args", "ls /mydb --limit 5", []Stage{{Argv: []string{"ls", "/mydb", "--limit", "5"}}}},
		{"double quotes", `put "my doc.json"`, []Stage{{Argv: []string{"put", "my doc.json"}}}},
		{"single quotes", `find '{"selector":{"a":1}}'`, []Stage{{Argv: []string{"find", `{"selector":{"a":1}}`}}}},
		{"escaped space", `cat my\ doc`, []Stage{{Argv: []string{"cat", "my doc"}}}},
		{"escape in double quotes", `cat "a\"b"`, []Stage{{Argv: []string{"cat", `a"b`}}}},
		{"empty quoted arg", `put ""`, []Stage{{Argv: []string{"put", ""}}}},
		{"one jq stage", "cat doc1 | .name", []Stage{{Argv: []string{"cat", "doc1"}}, {Expr: ".name"}}},
		{"two jq stages", "ls | .[] | .id", []Stage{{Argv: []string{"ls"}}, {Expr: ".[]"}, {Expr: ".id"}}},
		{"command stage", "ls /mydb | cat", []Stage{{Argv: []string{"ls", "/mydb"}}, {Argv: []string{"cat"}}}},
		{"jq then command", `find --field year | select(.year > 2000) | put /old`, []Stage{
			{Argv: []string{"find", "--field", "year"}},
			{Expr: "select(.year > 2000)"},
			{Argv: []string{"put", "/old"}},
		}},
		{"pipe in quotes is literal", `find '{"a":"b|c"}'`, []Stage{{Argv: []string{"find", `{"a":"b|c"}`}}}},
		{"pipe inside a jq string stays in the stage", `ls | select(.id == "a|b")`, []Stage{
			{Argv: []string{"ls"}},
			{Expr: `select(.id == "a|b")`},
		}},
		{"extra whitespace", "  ls   /mydb  ", []Stage{{Argv: []string{"ls", "/mydb"}}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse(tc.input, isCommand)
			if err != nil {
				t.Fatalf("Parse(%q) returned error: %v", tc.input, err)
			}
			if !reflect.DeepEqual(got.Stages, tc.want) {
				t.Errorf("Parse(%q).Stages = %#v, want %#v", tc.input, got.Stages, tc.want)
			}
		})
	}
}

// A nil classifier is what the history filter and completion pass: they only
// need to know the line parses, and must not depend on a registry.
func TestParseWithoutAClassifierMakesEveryLaterStageJQ(t *testing.T) {
	got, err := Parse("ls | cat", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []Stage{{Argv: []string{"ls"}}, {Expr: "cat"}}
	if !reflect.DeepEqual(got.Stages, want) {
		t.Errorf("Stages = %#v, want %#v", got.Stages, want)
	}
}

func TestParseRejectsALeadingPipe(t *testing.T) {
	for _, in := range []string{"| .name", "  |  ls"} {
		if _, err := Parse(in, nil); !errors.Is(err, ErrLeadingPipe) {
			t.Errorf("Parse(%q) error = %v, want ErrLeadingPipe", in, err)
		}
	}
}

func TestParseRejectsATrailingPipe(t *testing.T) {
	for _, in := range []string{"ls |", "ls | .id |", "ls |   "} {
		if _, err := Parse(in, nil); !errors.Is(err, ErrTrailingPipe) {
			t.Errorf("Parse(%q) error = %v, want ErrTrailingPipe", in, err)
		}
	}
}

func TestLineIsCommand(t *testing.T) {
	line, err := Parse("ls | .id", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !line.IsCommand(0) || line.IsCommand(1) {
		t.Errorf("IsCommand = %v,%v; want true,false", line.IsCommand(0), line.IsCommand(1))
	}
}

// "set <name> = <pipeline>" is the one line the parser does not split: the
// operator must not have to quote a pipeline in order to capture it. The
// classifier is irrelevant here and is passed as nil: the capture path never
// reaches the stage splitter, which is exactly what these cases assert.
func TestParseKeepsACaptureAsOneRawString(t *testing.T) {
	line, err := Parse(`set rev = cat /movies/a | ._rev`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(line.Stages) != 1 {
		t.Fatalf("stages = %#v; a capture is one stage", line.Stages)
	}
	st := line.Stages[0]
	if !reflect.DeepEqual(st.Argv, []string{"set", "rev", "="}) {
		t.Errorf("Argv = %#v", st.Argv)
	}
	if st.Capture != "cat /movies/a | ._rev" {
		t.Errorf("Capture = %q", st.Capture)
	}
}

func TestParseCaptureKeepsQuotesAndSpacingVerbatim(t *testing.T) {
	line, err := Parse(`set sel =   find '{"a":1}' | .id  `, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := line.Stages[0].Capture; got != `find '{"a":1}' | .id` {
		t.Errorf("Capture = %q", got)
	}
}

func TestParseOnlyTheThreeWordShapeIsACapture(t *testing.T) {
	for _, in := range []string{
		"set year 2001",     // no "="
		"set",               // nothing to capture
		"set year =",        // nothing after the "="
		"set 9bad = ls",     // not a variable name
		"settle rev = ls",   // not the "set" command
		"ls | set rev = ls", // "set" is not the line's first word
	} {
		line, err := Parse(in, nil)
		if err != nil {
			continue // a leading/trailing pipe error is fine here; a capture is not
		}
		for i, st := range line.Stages {
			if st.Capture != "" {
				t.Errorf("Parse(%q).Stages[%d].Capture = %q, want none", in, i, st.Capture)
			}
		}
	}
}

// A quoted selector typed over several lines — what NeedsMore keeps reading
// for — reaches the command as one argument with the newline intact.
func TestParseKeepsAQuotedArgumentSplitOverLines(t *testing.T) {
	got, err := Parse("find '{\"selector\":\n{\"kind\":\"holder\"}}'", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"find", "{\"selector\":\n{\"kind\":\"holder\"}}"}
	if !reflect.DeepEqual(got.Stages[0].Argv, want) {
		t.Errorf("Argv = %#v, want %#v", got.Stages[0].Argv, want)
	}
}

func TestParseUnterminatedQuote(t *testing.T) {
	if _, err := Parse(`cat "oops`, nil); err != ErrUnterminatedQuote {
		t.Fatalf("Parse returned %v, want ErrUnterminatedQuote", err)
	}
}

func TestNeedsMore(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  bool
	}{
		{"ls", false},
		{`ls \`, true},
		{`find {"selector":`, true},
		{`find {"selector":{"a":1}}`, false},
		{`cat "unclosed`, true},
		{`find '{"a":"{"}'`, false},
		{"ls [", true},
	} {
		if got := NeedsMore(tc.input); got != tc.want {
			t.Errorf("NeedsMore(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}
