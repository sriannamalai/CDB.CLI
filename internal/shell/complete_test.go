package shell

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/command"
	"github.com/sriannamalai/CDB.CLI/internal/couch"
	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// completeShell returns a shell wired to a stub server, plus the server, so a
// test can assert on the requests completion made.
func completeShell(t *testing.T) (*Shell, *couchtest.Server) {
	t.Helper()
	srv := couchtest.New(t)
	srv.JSON("GET", "/_all_dbs", 200, `["mydb","mydata","other"]`)
	srv.JSON("GET", "/mydb/_all_docs", 200, `{"total_rows":5,"offset":0,"rows":[
		{"id":"_design/app","key":"_design/app","value":{"rev":"1-c"}},
		{"id":"caf\u00e9","key":"caf\u00e9","value":{"rev":"1-d"}},
		{"id":"doc1","key":"doc1","value":{"rev":"1-a"}},
		{"id":"doc2","key":"doc2","value":{"rev":"1-b"}},
		{"id":"my doc","key":"my doc","value":{"rev":"1-e"}}]}`)
	srv.JSON("GET", "/mydb/_design/app", 200, `{"_id":"_design/app","views":{"by_name":{"map":"function(){}"},"by_date":{"map":"function(){}"}}}`)
	srv.JSON("POST", "/mydb/_find", 200, `{"docs":[{"_id":"a","name":"alice","age":30}]}`)
	c, err := couch.New(couch.Config{URL: srv.URL(), Auth: couch.AuthNone})
	if err != nil {
		t.Fatal(err)
	}
	s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	s.Attach(c, "test")
	t.Cleanup(func() { _ = s.Detach() })
	sh, err := New(command.Default(), s, Config{})
	if err != nil {
		t.Fatal(err)
	}
	return sh, srv
}

func values(cands []command.Candidate) []string {
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.Value)
	}
	return out
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func TestCompleteCommandNames(t *testing.T) {
	sh, _ := completeShell(t)
	word, cands := sh.CompleteLine(context.Background(), "co", 2)
	if word != "co" {
		t.Errorf("word = %q", word)
	}
	got := values(cands)
	if !contains(got, "connect") || !contains(got, "conflicts") {
		t.Errorf("candidates = %v, want connect and conflicts", got)
	}
	if contains(got, "ls") {
		t.Errorf("candidates = %v, want only names starting with co", got)
	}
}

func TestCompleteMarksDestructiveCommands(t *testing.T) {
	sh, _ := completeShell(t)
	_, cands := sh.CompleteLine(context.Background(), "rm", 2)
	for _, c := range cands {
		switch c.Value {
		case "rm", "rmdir":
			if !strings.HasPrefix(c.Description, command.DestructiveMarker) {
				t.Errorf("%s description = %q, want the %q marker", c.Value, c.Description, command.DestructiveMarker)
			}
		}
	}
}

func TestCompleteFlags(t *testing.T) {
	sh, _ := completeShell(t)
	_, cands := sh.CompleteLine(context.Background(), "ls --li", 7)
	if !contains(values(cands), "--limit") {
		t.Errorf("candidates = %v, want --limit", values(cands))
	}
}

func TestCompleteDatabaseNames(t *testing.T) {
	sh, _ := completeShell(t)
	_, cands := sh.CompleteLine(context.Background(), "cd /myd", 7)
	got := values(cands)
	if !contains(got, "/mydb") || !contains(got, "/mydata") {
		t.Errorf("candidates = %v, want /mydb and /mydata", got)
	}
	if contains(got, "/other") {
		t.Errorf("candidates = %v, want only names starting with myd", got)
	}
}

func TestCompleteDocumentIDs(t *testing.T) {
	sh, _ := completeShell(t)
	_, cands := sh.CompleteLine(context.Background(), "cat /mydb/doc", 13)
	got := values(cands)
	if !contains(got, "/mydb/doc1") || !contains(got, "/mydb/doc2") {
		t.Errorf("candidates = %v", got)
	}
}

func TestCompleteDesignDocumentIDsKeepTheDesignSeparator(t *testing.T) {
	sh, _ := completeShell(t)
	_, cands := sh.CompleteLine(context.Background(), "ls /mydb/_des", 13)
	got := values(cands)
	if !contains(got, "/mydb/_design/app") {
		t.Errorf("candidates = %v, want /mydb/_design/app", got)
	}
}

func TestCompleteDocumentIDsUsesABoundedPrefixQuery(t *testing.T) {
	sh, srv := completeShell(t)
	sh.CompleteLine(context.Background(), "cat /mydb/doc", 13)
	req := srv.Last("GET", "/mydb/_all_docs")
	if req == nil {
		t.Fatal("completion made no _all_docs request")
	}
	if got := req.Query("startkey_docid"); got != "doc" {
		t.Errorf("startkey_docid = %q, want doc", got)
	}
	if got, want := req.Query("endkey_docid"), "doc￰"; got != want {
		t.Errorf("endkey_docid = %q, want %q", got, want)
	}
	if req.Query("limit") == "" {
		t.Errorf("query = %q, want a limit", req.RawQuery)
	}
	if req.Query("skip") != "" {
		t.Errorf("query = %q, want no skip", req.RawQuery)
	}
}

func TestCompleteDecodesTheTypedPrefix(t *testing.T) {
	for _, tc := range []struct {
		name    string
		line    string
		wantKey string
		want    string
	}{
		{"space", "cat /mydb/my%20", "my ", "/mydb/my%20doc"},
		{"partial rune", "cat /mydb/caf%C3", "caf\xc3", "/mydb/caf%C3%A9"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sh, srv := completeShell(t)
			_, cands := sh.CompleteLine(context.Background(), tc.line, len(tc.line))
			if got := values(cands); !contains(got, tc.want) {
				t.Errorf("candidates = %v, want %q", got, tc.want)
			}
			req := srv.Last("GET", "/mydb/_all_docs")
			if req == nil {
				t.Fatal("completion made no _all_docs request")
			}
			if got := req.Query("startkey_docid"); got != tc.wantKey {
				t.Errorf("startkey_docid = %q, want %q", got, tc.wantKey)
			}
			if got, want := req.Query("endkey_docid"), tc.wantKey+"\ufff0"; got != want {
				t.Errorf("endkey_docid = %q, want %q", got, want)
			}
		})
	}
}

func TestCompleteViewNames(t *testing.T) {
	sh, _ := completeShell(t)
	_, cands := sh.CompleteLine(context.Background(), "query /mydb/_design/app/_view/by_", 33)
	got := values(cands)
	if !contains(got, "/mydb/_design/app/_view/by_name") || !contains(got, "/mydb/_design/app/_view/by_date") {
		t.Errorf("candidates = %v", got)
	}
}

func TestCompleteFieldNames(t *testing.T) {
	sh, _ := completeShell(t)
	_, cands := sh.CompleteLine(context.Background(), "find /mydb --fields na", 22)
	if !contains(values(cands), "name") {
		t.Errorf("candidates = %v, want name", values(cands))
	}
}

func TestCompleteFieldNamesSkipsFlagValues(t *testing.T) {
	for _, line := range []string{
		"find --limit 5 /mydb --fields na",
		"find --limit=5 /mydb --fields na",
	} {
		t.Run(line, func(t *testing.T) {
			sh, srv := completeShell(t)
			_, cands := sh.CompleteLine(context.Background(), line, len(line))
			if !contains(values(cands), "name") {
				t.Errorf("candidates = %v, want name", values(cands))
			}
			if srv.Last("POST", "/mydb/_find") == nil {
				t.Error("the field sample did not query /mydb")
			}
			for _, r := range srv.Requests() {
				if r.Method == "POST" && r.Path == "/5/_find" {
					t.Error("the field sample took the --limit value for a database")
				}
			}
		})
	}
}

func TestCompleteAfterAFilterPipeReturnsNothing(t *testing.T) {
	sh, _ := completeShell(t)
	_, cands := sh.CompleteLine(context.Background(), "ls | .na", 8)
	if len(cands) != 0 {
		t.Errorf("candidates after a filter pipe = %v, want none", values(cands))
	}
}

func TestCompleteWhenDisconnectedReturnsCommandsOnly(t *testing.T) {
	s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	sh, err := New(command.Default(), s, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if _, cands := sh.CompleteLine(context.Background(), "c", 1); len(cands) == 0 {
		t.Error("command names should complete without a connection")
	}
	if _, cands := sh.CompleteLine(context.Background(), "cd /my", 6); len(cands) != 0 {
		t.Errorf("path candidates without a connection = %v, want none", values(cands))
	}
}
