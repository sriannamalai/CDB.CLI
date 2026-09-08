package couch

import (
	"context"
	"net/http"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
)

func newTestClient(t *testing.T, srv *couchtest.Server) *Client {
	t.Helper()
	c, err := New(Config{URL: srv.URL(), Auth: AuthNone})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestListDatabases(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_all_dbs", 200, `["_replicator","_users","mydb"]`)
	c := newTestClient(t, srv)
	got, err := c.ListDatabases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[2] != "mydb" {
		t.Errorf("ListDatabases = %v", got)
	}
}

func TestDatabasesInfoPostsKeys(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/_dbs_info", 200, `[{"key":"mydb","info":{"db_name":"mydb","doc_count":42,"doc_del_count":1,"update_seq":"9-x","sizes":{"file":16692,"external":123},"cluster":{"q":2,"n":1},"props":{"partitioned":true}}}]`)
	c := newTestClient(t, srv)
	got, err := c.DatabasesInfo(context.Background(), []string{"mydb"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d infos, want 1", len(got))
	}
	i := got[0]
	if i.Name != "mydb" || i.DocCount != 42 || i.DiskSize != 16692 || i.ExternalSize != 123 || !i.Partitioned || i.Q != 2 || i.N != 1 {
		t.Errorf("info = %+v", i)
	}
	body := string(srv.Last("POST", "/_dbs_info").Body)
	if body != `{"keys":["mydb"]}` {
		t.Errorf("_dbs_info body = %s", body)
	}
}

func TestAllDocsPagesWithStartKeyDocIDAndNeverUsesSkip(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("GET", "/mydb/_all_docs", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"total_rows":5,"offset":0,"rows":[
			{"id":"a","key":"a","value":{"rev":"1-a"}},
			{"id":"b","key":"b","value":{"rev":"1-b"}},
			{"id":"c","key":"c","value":{"rev":"1-c"}}]}`))
	})
	c := newTestClient(t, srv)
	page, err := c.AllDocs(context.Background(), "mydb", AllDocsOptions{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 2 {
		t.Fatalf("page has %d rows, want 2", len(page.Rows))
	}
	if page.NextStartKeyDocID != "c" {
		t.Errorf("NextStartKeyDocID = %q, want %q", page.NextStartKeyDocID, "c")
	}
	req := srv.Last("GET", "/mydb/_all_docs")
	if req.Query("limit") != "3" {
		t.Errorf("limit = %q, want 3 (limit+1 for lookahead)", req.Query("limit"))
	}
	if req.Query("skip") != "" {
		t.Error("_all_docs request used skip")
	}
}

func TestAllDocsSendsStartKeyDocID(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb/_all_docs", 200, `{"total_rows":1,"offset":0,"rows":[{"id":"c","key":"c","value":{"rev":"1-c"}}]}`)
	c := newTestClient(t, srv)
	if _, err := c.AllDocs(context.Background(), "mydb", AllDocsOptions{Limit: 10, StartKeyDocID: "c", IncludeDocs: true}); err != nil {
		t.Fatal(err)
	}
	req := srv.Last("GET", "/mydb/_all_docs")
	if req.Query("startkey_docid") != "c" {
		t.Errorf("startkey_docid = %q", req.Query("startkey_docid"))
	}
	if req.Query("start_key") != `"c"` {
		t.Errorf("start_key = %q, want %q", req.Query("start_key"), `"c"`)
	}
	if req.Query("include_docs") != "true" {
		t.Errorf("include_docs = %q", req.Query("include_docs"))
	}
}

func TestAllDocsPartitionUsesPartitionPath(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb/_partition/p1/_all_docs", 200, `{"total_rows":1,"offset":0,"rows":[{"id":"p1:doc1","key":"p1:doc1","value":{"rev":"1-a"}}]}`)
	c := newTestClient(t, srv)
	page, err := c.AllDocs(context.Background(), "mydb", AllDocsOptions{Partition: "p1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 1 || page.Rows[0].ID != "p1:doc1" {
		t.Errorf("rows = %+v", page.Rows)
	}
}

func TestDatabaseExists(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("HEAD", "/mydb", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	c := newTestClient(t, srv)
	ok, err := c.DatabaseExists(context.Background(), "mydb")
	if err != nil || !ok {
		t.Errorf("DatabaseExists(mydb) = %v, %v", ok, err)
	}
	ok, err = c.DatabaseExists(context.Background(), "nope")
	if err != nil || ok {
		t.Errorf("DatabaseExists(nope) = %v, %v", ok, err)
	}
}
