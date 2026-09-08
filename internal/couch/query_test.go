package couch

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
)

func TestFindSendsSelectorAndReturnsBookmark(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/mydb/_find", 200, `{"docs":[{"_id":"a"},{"_id":"b"}],"bookmark":"g1AAAA","warning":"no matching index found"}`)
	c := newTestClient(t, srv)
	page, err := c.Find(context.Background(), "mydb", FindOptions{
		Selector: json.RawMessage(`{"n":{"$gt":0}}`),
		Fields:   []string{"_id", "n"},
		Limit:    2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Docs) != 2 || page.Bookmark != "g1AAAA" || page.Warning == "" {
		t.Errorf("page = %+v", page)
	}
	body := string(srv.Last("POST", "/mydb/_find").Body)
	for _, want := range []string{`"selector":{"n":{"$gt":0}}`, `"limit":2`, `"fields":["_id","n"]`} {
		if !strings.Contains(body, want) {
			t.Errorf("_find body %s is missing %s", body, want)
		}
	}
	if strings.Contains(body, `"skip"`) {
		t.Error("_find body used skip")
	}
}

func TestFindSendsBookmark(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/mydb/_find", 200, `{"docs":[],"bookmark":"nil"}`)
	c := newTestClient(t, srv)
	if _, err := c.Find(context.Background(), "mydb", FindOptions{
		Selector: json.RawMessage(`{}`),
		Bookmark: "g1AAAA",
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(srv.Last("POST", "/mydb/_find").Body), `"bookmark":"g1AAAA"`) {
		t.Error("_find did not send the bookmark")
	}
}

func TestFindPartitionUsesPartitionPath(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/mydb/_partition/p1/_find", 200, `{"docs":[{"_id":"p1:a"}]}`)
	c := newTestClient(t, srv)
	page, err := c.Find(context.Background(), "mydb", FindOptions{Selector: json.RawMessage(`{}`), Partition: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Docs) != 1 {
		t.Errorf("docs = %v", page.Docs)
	}
}

func TestExplain(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/mydb/_explain", 200, `{"dbname":"mydb","index":{"type":"special"}}`)
	c := newTestClient(t, srv)
	raw, err := c.Explain(context.Background(), "mydb", FindOptions{Selector: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "dbname") {
		t.Errorf("explain = %s", raw)
	}
}

func TestQueryBuildsViewPathAndPagesWithStartKeyDocID(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb/_design/app/_view/by_name", 200, `{"total_rows":9,"offset":0,"rows":[
		{"id":"a","key":"alice","value":1},
		{"id":"b","key":"bob","value":1},
		{"id":"c","key":"carol","value":1}]}`)
	c := newTestClient(t, srv)
	page, err := c.Query(context.Background(), "mydb", "_design/app", "by_name", ViewOptions{Limit: 2, IncludeDocs: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(page.Rows))
	}
	if page.NextStartKeyDocID != "c" || string(page.NextStartKey) != `"carol"` {
		t.Errorf("next = %q / %s", page.NextStartKeyDocID, page.NextStartKey)
	}
	req := srv.Last("GET", "/mydb/_design/app/_view/by_name")
	if req.Query("limit") != "3" {
		t.Errorf("limit = %q, want 3", req.Query("limit"))
	}
	if req.Query("include_docs") != "true" {
		t.Errorf("include_docs = %q", req.Query("include_docs"))
	}
	if req.Query("skip") != "" {
		t.Error("view query used skip")
	}
}

func TestQueryReduceAndGroupLevel(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb/_design/app/_view/by_name", 200, `{"rows":[{"key":null,"value":9}]}`)
	c := newTestClient(t, srv)
	reduce := true
	level := 2
	if _, err := c.Query(context.Background(), "mydb", "_design/app", "by_name", ViewOptions{Reduce: &reduce, GroupLevel: &level}); err != nil {
		t.Fatal(err)
	}
	req := srv.Last("GET", "/mydb/_design/app/_view/by_name")
	if req.Query("reduce") != "true" || req.Query("group_level") != "2" {
		t.Errorf("query = %q", req.RawQuery)
	}
}
