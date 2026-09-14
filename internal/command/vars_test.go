package command

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

func TestSetDefinesAVariableFromTheRestOfTheLine(t *testing.T) {
	s := connected(t, couchtest.New(t))
	if _, err := invoke(t, Set(), s, "title", "two", "words"); err != nil {
		t.Fatal(err)
	}
	v, ok := s.Vars.Lookup("title")
	if !ok || v.Text() != "two words" {
		t.Errorf("title = %v, %v", v, ok)
	}
}

func TestSetListsSortedAndMasksACredential(t *testing.T) {
	s := connected(t, couchtest.New(t))
	s.Vars.Set("api_token", session.StringValue("s3cret"))
	s.Vars.Set("year", session.Value{JSON: json.RawMessage(`2001`)})
	res, err := invoke(t, Set(), s)
	if err != nil {
		t.Fatal(err)
	}
	rows, ok := res.(Rows)
	if !ok {
		t.Fatalf("result is %T, want Rows", res)
	}
	if len(rows.Items) != 2 || rows.Items[0].Cells[0] != "api_token" {
		t.Fatalf("rows = %#v", rows.Items)
	}
	if rows.Items[0].Cells[1] != maskedValue {
		t.Errorf("token value = %q, want %q", rows.Items[0].Cells[1], maskedValue)
	}
	if strings.Contains(string(rows.Items[0].JSON), "s3cret") {
		t.Fatal("the secret reached the JSON output")
	}
	if rows.Items[1].Cells[1] != "2001" {
		t.Errorf("year = %q", rows.Items[1].Cells[1])
	}
}

func TestSetRejectsABadName(t *testing.T) {
	s := connected(t, couchtest.New(t))
	_, err := invoke(t, Set(), s, "a-b", "x")
	var ue *UsageError
	if !errorsAs(err, &ue) {
		t.Fatalf("error = %v (%T)", err, err)
	}
}

func TestSetCapturesOneValueAndSeveral(t *testing.T) {
	s := connected(t, couchtest.New(t))
	one := SetFrom(func(context.Context, string) ([]json.RawMessage, error) {
		return []json.RawMessage{json.RawMessage(`"1-abc"`)}, nil
	})
	if _, err := invoke(t, one, s, "rev", "=", "cat", "/movies/a"); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.Vars.Lookup("rev"); v.Text() != "1-abc" {
		t.Errorf("rev = %q; a captured JSON string is stored as the string", v.Text())
	}
	many := SetFrom(func(context.Context, string) ([]json.RawMessage, error) {
		return []json.RawMessage{json.RawMessage(`"a"`), json.RawMessage(`"b"`)}, nil
	})
	if _, err := invoke(t, many, s, "ids", "=", "ls", "/movies"); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.Vars.Lookup("ids"); v.Text() != `["a","b"]` {
		t.Errorf("ids = %q; several values are stored as an array", v.Text())
	}
}

// The shell's parser hands the whole pipeline over on Invocation.Capture, so a
// capture containing "|" needs no quoting from the operator.
func TestSetCaptureTakesTheWholePipelineFromTheStage(t *testing.T) {
	s := connected(t, couchtest.New(t))
	var got string
	c := SetFrom(func(_ context.Context, line string) ([]json.RawMessage, error) {
		got = line
		return []json.RawMessage{json.RawMessage(`"1-abc"`)}, nil
	})
	fs := NewRegistry().NewFlagSet(c)
	if err := fs.Parse([]string{"rev", "="}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Run(context.Background(), s, Invocation{
		Args: fs.Args(), Flags: fs, Stdin: s.Stdin(), Stdout: s.Stdout, Stderr: s.Stderr,
		Shell: true, Capture: "cat /movies/a | ._rev",
	}); err != nil {
		t.Fatal(err)
	}
	if got != "cat /movies/a | ._rev" {
		t.Errorf("captured line = %q", got)
	}
	if v, _ := s.Vars.Lookup("rev"); v.Text() != "1-abc" {
		t.Errorf("rev = %q", v.Text())
	}
}

func TestSetCaptureNeedsARunner(t *testing.T) {
	s := connected(t, couchtest.New(t))
	_, err := invoke(t, Set(), s, "rev", "=", "cat", "/movies/a")
	var ue *UsageError
	if !errorsAs(err, &ue) {
		t.Fatalf("error = %v (%T)", err, err)
	}
}

func TestUnsetRemovesAndIsQuietAboutAnUnknownName(t *testing.T) {
	s := connected(t, couchtest.New(t))
	s.Vars.Set("year", session.StringValue("2001"))
	if _, err := invoke(t, Unset(), s, "year"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Vars.Lookup("year"); ok {
		t.Error("year survived unset")
	}
	if _, err := invoke(t, Unset(), s, "neverset"); err != nil {
		t.Errorf("unsetting an unknown name = %v, want nil", err)
	}
}
