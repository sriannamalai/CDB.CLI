package shell

import (
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantArgv   []string
		wantFilter string
	}{
		{"empty", "", nil, ""},
		{"simple", "ls", []string{"ls"}, ""},
		{"args", "ls /mydb --limit 5", []string{"ls", "/mydb", "--limit", "5"}, ""},
		{"double quotes", `put "my doc.json"`, []string{"put", "my doc.json"}, ""},
		{"single quotes", `find '{"selector":{"a":1}}'`, []string{"find", `{"selector":{"a":1}}`}, ""},
		{"escaped space", `cat my\ doc`, []string{"cat", "my doc"}, ""},
		{"escape in double quotes", `cat "a\"b"`, []string{"cat", `a"b`}, ""},
		{"empty quoted arg", `put ""`, []string{"put", ""}, ""},
		{"filter", "cat doc1 | .name", []string{"cat", "doc1"}, ".name"},
		{"filter with pipe inside", "ls | .[] | .id", []string{"ls"}, ".[] | .id"},
		{"pipe in quotes is literal", `find '{"a":"b|c"}'`, []string{"find", `{"a":"b|c"}`}, ""},
		{"extra whitespace", "  ls   /mydb  ", []string{"ls", "/mydb"}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse(tc.input)
			if err != nil {
				t.Fatalf("Parse(%q) returned error: %v", tc.input, err)
			}
			if !reflect.DeepEqual(got.Argv, tc.wantArgv) {
				t.Errorf("Parse(%q).Argv = %#v, want %#v", tc.input, got.Argv, tc.wantArgv)
			}
			if got.Filter != tc.wantFilter {
				t.Errorf("Parse(%q).Filter = %q, want %q", tc.input, got.Filter, tc.wantFilter)
			}
		})
	}
}

// A quoted selector typed over several lines — what NeedsMore keeps reading
// for — reaches the command as one argument with the newline intact.
func TestParseKeepsAQuotedArgumentSplitOverLines(t *testing.T) {
	got, err := Parse("find '{\"selector\":\n{\"kind\":\"holder\"}}'")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"find", "{\"selector\":\n{\"kind\":\"holder\"}}"}
	if !reflect.DeepEqual(got.Argv, want) {
		t.Errorf("Argv = %#v, want %#v", got.Argv, want)
	}
}

func TestParseUnterminatedQuote(t *testing.T) {
	if _, err := Parse(`cat "oops`); err != ErrUnterminatedQuote {
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
