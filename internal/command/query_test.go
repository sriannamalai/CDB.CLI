package command

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
)

func TestFindWithAnExplicitSelector(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/mydb/_find", 200, `{"docs":[{"_id":"a","name":"alice"}],"bookmark":"BM"}`)
	s := connected(t, srv)
	s.SetPath("/mydb")
	res, err := invoke(t, Find(), s, `{"name":"alice"}`)
	if err != nil {
		t.Fatal(err)
	}
	rows, ok := res.(Rows)
	if !ok {
		t.Fatalf("result is %T, want Rows", res)
	}
	if len(rows.Items) != 1 || rows.Items[0].Cells[0] != "a" {
		t.Errorf("rows = %+v", rows.Items)
	}
	body := string(srv.Last("POST", "/mydb/_find").Body)
	if !strings.Contains(body, `"selector":{"name":"alice"}`) {
		t.Errorf("body = %s", body)
	}
}

func TestFindWrapsABareSelector(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/mydb/_find", 200, `{"docs":[]}`)
	s := connected(t, srv)
	s.SetPath("/mydb")
	if _, err := invoke(t, Find(), s, `{"selector":{"name":"alice"}}`); err != nil {
		t.Fatal(err)
	}
	body := string(srv.Last("POST", "/mydb/_find").Body)
	if strings.Contains(body, `"selector":{"selector"`) {
		t.Errorf("find double-wrapped the selector: %s", body)
	}
}

func TestFindHintsAtTheBookmark(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/mydb/_find", 200, `{"docs":[{"_id":"a"},{"_id":"b"}],"bookmark":"BM"}`)
	s := connected(t, srv)
	s.SetPath("/mydb")
	res, err := invoke(t, Find(), s, "--limit", "2", `{"a":1}`)
	if err != nil {
		t.Fatal(err)
	}
	rows := res.(Rows)
	if !strings.Contains(rows.Hint, "--bookmark") || !strings.Contains(rows.Hint, "BM") {
		t.Errorf("Hint = %q", rows.Hint)
	}
}

func TestFindCarriesTheMangoWarningInTheHint(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/mydb/_find", 200, `{"docs":[{"_id":"a"}],"warning":"no matching index found, create an index to optimize query time"}`)
	s := connected(t, srv)
	s.SetPath("/mydb")
	var errBuf bytes.Buffer
	s.Stderr = &errBuf
	res, err := invoke(t, Find(), s, `{"a":1}`)
	if err != nil {
		t.Fatal(err)
	}
	rows := res.(Rows)
	if !strings.Contains(rows.Hint, "no matching index") {
		t.Errorf("Hint = %q, want the Mango warning", rows.Hint)
	}
	// Commands never print: the warning must not reach stderr directly.
	if errBuf.Len() != 0 {
		t.Errorf("find wrote %q to stderr", errBuf.String())
	}
}

func TestFindExplain(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/mydb/_explain", 200, `{"dbname":"mydb"}`)
	s := connected(t, srv)
	s.SetPath("/mydb")
	res, err := invoke(t, Find(), s, "--explain", `{"a":1}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := res.(Document); !ok {
		t.Fatalf("result is %T, want Document", res)
	}
}

func TestFindGuidedBuilder(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/mydb/_find", 200, `{"docs":[{"_id":"a"}]}`)
	s := connected(t, srv)
	s.SetPath("/mydb")
	s.Prefs.Interactive = true
	// field, operator, value, then "n" to stop adding conditions.
	s.SetStdin(strings.NewReader("name\n=\nalice\nn\n"))
	if _, err := invoke(t, Find(), s); err != nil {
		t.Fatal(err)
	}
	body := string(srv.Last("POST", "/mydb/_find").Body)
	if !strings.Contains(body, `"name":"alice"`) {
		t.Errorf("guided builder produced %s", body)
	}
}

// TestFindGuidedBuilderUnquotesAJSONString covers the operator who types a
// JSON-quoted value. Sending {"name":"\"bob\""} to CouchDB is not an error: it
// silently matches nothing, which is the worst possible failure mode for a
// guided prompt.
func TestFindGuidedBuilderUnquotesAJSONString(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/mydb/_find", 200, `{"docs":[]}`)
	s := connected(t, srv)
	s.SetPath("/mydb")
	s.Prefs.Interactive = true
	s.SetStdin(strings.NewReader("name\n=\n\"bob\"\nn\n"))
	if _, err := invoke(t, Find(), s); err != nil {
		t.Fatal(err)
	}
	body := string(srv.Last("POST", "/mydb/_find").Body)
	if !strings.Contains(body, `"selector":{"name":"bob"}`) {
		t.Errorf("guided builder produced %s, want the value unquoted", body)
	}
	if strings.Contains(body, `\"bob\"`) {
		t.Errorf("guided builder kept the JSON quotes: %s", body)
	}
}

// TestFindGuidedBuilderKeepsNumbersTyped guards the non-"=" operator path: a
// numeric value must reach Mango as a number, not as a string, or a $gt
// comparison silently compares strings.
func TestFindGuidedBuilderKeepsNumbersTyped(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/mydb/_find", 200, `{"docs":[]}`)
	s := connected(t, srv)
	s.SetPath("/mydb")
	s.Prefs.Interactive = true
	s.SetStdin(strings.NewReader("n\n>\n3\nn\n"))
	if _, err := invoke(t, Find(), s); err != nil {
		t.Fatal(err)
	}
	body := string(srv.Last("POST", "/mydb/_find").Body)
	if !strings.Contains(body, `"selector":{"n":{"$gt":3}}`) {
		t.Errorf("guided builder produced %s, want a numeric $gt", body)
	}
}

func TestFindWithoutASelectorOnANonTerminalIsAUsageError(t *testing.T) {
	srv := couchtest.New(t)
	s := connected(t, srv)
	s.SetPath("/mydb")
	s.Prefs.Interactive = false
	_, err := invoke(t, Find(), s)
	if _, ok := err.(*UsageError); !ok {
		t.Fatalf("err = %#v, want *UsageError", err)
	}
}

func TestQueryCommand(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb/_design/app/_view/by_name", 200, `{"total_rows":2,"offset":0,"rows":[
		{"id":"a","key":"alice","value":1},
		{"id":"b","key":"bob","value":1}]}`)
	s := connected(t, srv)
	res, err := invoke(t, Query(), s, "/mydb/_design/app/_view/by_name", "--limit", "10")
	if err != nil {
		t.Fatal(err)
	}
	rows, ok := res.(Rows)
	if !ok {
		t.Fatalf("result is %T, want Rows", res)
	}
	if len(rows.Items) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows.Items))
	}
	if rows.Columns[0].Title != "key" || rows.Columns[1].Title != "id" || rows.Columns[2].Title != "value" {
		t.Errorf("columns = %+v", rows.Columns)
	}
}

func TestQueryRejectsANonViewPath(t *testing.T) {
	srv := couchtest.New(t)
	s := connected(t, srv)
	_, err := invoke(t, Query(), s, "/mydb")
	if _, ok := err.(*UsageError); !ok {
		t.Fatalf("err = %#v, want *UsageError", err)
	}
}
