package command

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/couch"
	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// connected returns a session wired to srv with no authentication.
func connected(t *testing.T, srv *couchtest.Server) *session.Session {
	t.Helper()
	c, err := couch.New(couch.Config{URL: srv.URL(), Auth: couch.AuthNone})
	if err != nil {
		t.Fatal(err)
	}
	s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	s.Attach(c, "test")
	t.Cleanup(func() { _ = s.Detach() })
	return s
}

// invoke parses argv for a command and runs it. It mirrors what the two
// front-ends do, including copying the shared --yes and --verbose flags into
// the session preferences; without that, "--yes" would never reach Confirm.
func invoke(t *testing.T, c Command, s *session.Session, argv ...string) (Result, error) {
	t.Helper()
	fs := NewRegistry().NewFlagSet(c)
	if err := fs.Parse(argv); err != nil {
		return nil, err
	}
	if err := c.CheckArgsErr(fs.Args()); err != nil {
		return nil, err
	}
	if v, err := fs.GetBool("yes"); err == nil && v {
		s.Prefs.Yes = true
	}
	if v, err := fs.GetBool("verbose"); err == nil && v {
		s.Prefs.Verbose = true
	}
	return c.Run(context.Background(), s, Invocation{Args: fs.Args(), Flags: fs, Stdin: s.Stdin(), Stdout: s.Stdout, Stderr: s.Stderr})
}

func TestLsAtRootListsDatabases(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_all_dbs", 200, `["mydb","other"]`)
	srv.JSON("POST", "/_dbs_info", 200, `[
		{"key":"mydb","info":{"db_name":"mydb","doc_count":42,"sizes":{"file":16692,"external":100},"cluster":{"q":2,"n":1},"props":{}}},
		{"key":"other","info":{"db_name":"other","doc_count":7,"sizes":{"file":100,"external":10},"cluster":{"q":2,"n":1},"props":{"partitioned":true}}}]`)
	s := connected(t, srv)
	res, err := invoke(t, Ls(), s)
	if err != nil {
		t.Fatal(err)
	}
	rows, ok := res.(Rows)
	if !ok {
		t.Fatalf("result is %T, want Rows", res)
	}
	if len(rows.Items) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows.Items))
	}
	if rows.Items[0].Cells[0] != "mydb" || rows.Items[0].Cells[1] != "42" {
		t.Errorf("row 0 = %v", rows.Items[0].Cells)
	}
	if rows.Columns[1].Align != AlignRight {
		t.Error("the docs column is not right-aligned")
	}
	// The README tells operators to run `cdb ls / --json | jq -r '.name'`, so
	// the JSON side of the row must carry lower-case keys.
	if !strings.Contains(string(rows.Items[0].JSON), `"name":"mydb"`) {
		t.Errorf("row JSON = %s, want a lower-case \"name\" key", rows.Items[0].JSON)
	}
	if !strings.Contains(string(rows.Items[0].JSON), `"doc_count":42`) {
		t.Errorf("row JSON = %s, want a lower-case \"doc_count\" key", rows.Items[0].JSON)
	}
}

func TestLsInDatabaseListsDocumentsAndHints(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb/_all_docs", 200, `{"total_rows":3,"offset":0,"rows":[
		{"id":"a","key":"a","value":{"rev":"1-a"}},
		{"id":"b","key":"b","value":{"rev":"1-b"}},
		{"id":"c","key":"c","value":{"rev":"1-c"}}]}`)
	s := connected(t, srv)
	s.SetPath("/mydb")
	res, err := invoke(t, Ls(), s, "--limit", "2")
	if err != nil {
		t.Fatal(err)
	}
	rows := res.(Rows)
	if len(rows.Items) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows.Items))
	}
	if !strings.Contains(rows.Hint, "--start") || !strings.Contains(rows.Hint, "c") {
		t.Errorf("Hint = %q, want a --start hint naming c", rows.Hint)
	}
}

func TestLsWithFieldsAddsColumns(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb/_all_docs", 200, `{"total_rows":1,"offset":0,"rows":[
		{"id":"a","key":"a","value":{"rev":"1-a"},"doc":{"_id":"a","_rev":"1-a","name":"alice","age":30}}]}`)
	s := connected(t, srv)
	s.SetPath("/mydb")
	res, err := invoke(t, Ls(), s, "--fields", "name,age")
	if err != nil {
		t.Fatal(err)
	}
	rows := res.(Rows)
	if len(rows.Columns) != 4 {
		t.Fatalf("columns = %+v, want id, rev, name, age", rows.Columns)
	}
	if rows.Items[0].Cells[2] != "alice" || rows.Items[0].Cells[3] != "30" {
		t.Errorf("cells = %v", rows.Items[0].Cells)
	}
	if srv.Last("GET", "/mydb/_all_docs").Query("include_docs") != "true" {
		t.Error("--fields did not request include_docs")
	}
}

func TestLsInDesignDocListsViews(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb/_design/app", 200, `{"_id":"_design/app","_rev":"1-a","views":{"by_name":{"map":"function(){}"}},"filters":{"f1":"function(){}"},"updates":{"u1":"function(){}"}}`)
	s := connected(t, srv)
	s.SetPath("/mydb/_design/app")
	res, err := invoke(t, Ls(), s)
	if err != nil {
		t.Fatal(err)
	}
	rows := res.(Rows)
	if len(rows.Items) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows.Items))
	}
	joined := ""
	for _, r := range rows.Items {
		joined += r.Cells[0] + ":" + r.Cells[1] + " "
	}
	for _, want := range []string{"view:by_name", "filter:f1", "update:u1"} {
		if !strings.Contains(joined, want) {
			t.Errorf("rows %q are missing %q", joined, want)
		}
	}
}

func TestCdVerifiesTheTargetExists(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("HEAD", "/mydb", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	s := connected(t, srv)
	if _, err := invoke(t, Cd(), s, "/mydb"); err != nil {
		t.Fatal(err)
	}
	if s.Path() != "/mydb" {
		t.Errorf("Path() = %q, want /mydb", s.Path())
	}
	if _, err := invoke(t, Cd(), s, "/nope"); err == nil {
		t.Fatal("cd to a missing database returned no error")
	}
	if s.Path() != "/mydb" {
		t.Errorf("Path() = %q after a failed cd, want it unchanged", s.Path())
	}
}

func TestCdVerifiesADocumentExists(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb/doc1", 200, `{"_id":"doc1","_rev":"1-a"}`)
	s := connected(t, srv)
	if _, err := invoke(t, Cd(), s, "/mydb/doc1"); err != nil {
		t.Fatal(err)
	}
	if s.Path() != "/mydb/doc1" {
		t.Errorf("Path() = %q, want /mydb/doc1", s.Path())
	}
	// The stub answers 404 for every unregistered route, so this is the real
	// "document does not exist" path and not a database check standing in.
	_, err := invoke(t, Cd(), s, "/mydb/missing-doc")
	if err == nil {
		t.Fatal("cd to a missing document returned no error")
	}
	if e, ok := couch.AsError(err); !ok || e.Status != 404 {
		t.Fatalf("err = %#v, want a 404 couch.Error", err)
	}
	if s.Path() != "/mydb/doc1" {
		t.Errorf("Path() = %q after a failed cd, want it unchanged", s.Path())
	}
}

func TestCdVerifiesAViewExists(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb/_design/app", 200,
		`{"_id":"_design/app","_rev":"1-a","views":{"by_name":{"map":"function(d){}"}}}`)
	s := connected(t, srv)
	if _, err := invoke(t, Cd(), s, "/mydb/_design/app/_view/by_name"); err != nil {
		t.Fatal(err)
	}
	if s.Path() != "/mydb/_design/app/_view/by_name" {
		t.Errorf("Path() = %q", s.Path())
	}
	// The design document exists, so only a check of the views object itself
	// can catch this.
	_, err := invoke(t, Cd(), s, "/mydb/_design/app/_view/nope")
	if err == nil {
		t.Fatal("cd to a missing view returned no error")
	}
	if e, ok := couch.AsError(err); !ok || e.Status != 404 {
		t.Fatalf("err = %#v, want a 404 couch.Error", err)
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("error = %v, want it to name the view", err)
	}
}

func TestCdVerifiesAnAttachmentExists(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb/doc1", 200,
		`{"_id":"doc1","_rev":"1-a","_attachments":{"logo.png":{"content_type":"image/png","stub":true}}}`)
	s := connected(t, srv)
	if _, err := invoke(t, Cd(), s, "/mydb/doc1/logo.png"); err != nil {
		t.Fatal(err)
	}
	if s.Path() != "/mydb/doc1/logo.png" {
		t.Errorf("Path() = %q", s.Path())
	}
	_, err := invoke(t, Cd(), s, "/mydb/doc1/missing.txt")
	if err == nil {
		t.Fatal("cd to a missing attachment returned no error")
	}
	if e, ok := couch.AsError(err); !ok || e.Status != 404 {
		t.Fatalf("err = %#v, want a 404 couch.Error", err)
	}
	if !strings.Contains(err.Error(), "missing.txt") {
		t.Errorf("error = %v, want it to name the attachment", err)
	}
}

func TestCdWithNoArgumentGoesToRoot(t *testing.T) {
	srv := couchtest.New(t)
	s := connected(t, srv)
	s.SetPath("/mydb/doc1")
	if _, err := invoke(t, Cd(), s); err != nil {
		t.Fatal(err)
	}
	if s.Path() != "/" {
		t.Errorf("Path() = %q, want /", s.Path())
	}
}

func TestInfoAtRoot(t *testing.T) {
	srv := couchtest.New(t)
	s := connected(t, srv)
	res, err := invoke(t, Info(), s)
	if err != nil {
		t.Fatal(err)
	}
	rows := res.(Rows)
	joined := ""
	for _, r := range rows.Items {
		joined += r.Cells[0] + "=" + r.Cells[1] + ";"
	}
	if !strings.Contains(joined, "version=3.5.2") {
		t.Errorf("info rows %q are missing the version", joined)
	}
}

func TestInfoForDatabase(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb", 200, `{"db_name":"mydb","doc_count":42,"doc_del_count":1,"update_seq":"9-x","sizes":{"file":16692,"external":100},"cluster":{"q":2,"n":1},"props":{"partitioned":true}}`)
	s := connected(t, srv)
	res, err := invoke(t, Info(), s, "/mydb")
	if err != nil {
		t.Fatal(err)
	}
	rows := res.(Rows)
	joined := ""
	for _, r := range rows.Items {
		joined += r.Cells[0] + "=" + r.Cells[1] + ";"
	}
	for _, want := range []string{"name=mydb", "documents=42", "partitioned=true"} {
		if !strings.Contains(joined, want) {
			t.Errorf("info rows %q are missing %q", joined, want)
		}
	}
}
