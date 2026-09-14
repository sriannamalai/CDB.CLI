package couch

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
)

func testClient(t *testing.T, srv *couchtest.Server) *Client {
	t.Helper()
	c, err := New(Config{URL: srv.URL(), Auth: AuthNone})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestBulkWriteReportsOneResultPerDocument(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/movies/_bulk_docs", 201, `[
		{"ok":true,"id":"a","rev":"1-x"},
		{"id":"b","error":"conflict","reason":"Document update conflict."}]`)
	c := testClient(t, srv)
	got, err := c.BulkWrite(context.Background(), "movies", []json.RawMessage{
		json.RawMessage(`{"_id":"a"}`),
		json.RawMessage(`{"_id":"b","_rev":"1-old"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d results, want 2", len(got))
	}
	if got[0] != (BulkResult{ID: "a", Rev: "1-x", Status: "ok"}) {
		t.Errorf("result 0 = %#v", got[0])
	}
	if got[1].Status != "conflict" || got[1].ID != "b" {
		t.Errorf("result 1 = %#v; a per-document error is a row, not a failure", got[1])
	}
	body := srv.Last("POST", "/movies/_bulk_docs").Body
	if !strings.Contains(string(body), `"docs"`) || strings.Contains(string(body), "new_edits") {
		t.Errorf("request body = %s; BulkWrite writes with the server's default new_edits", body)
	}
}

func TestBulkWriteWithNoDocumentsAsksNothing(t *testing.T) {
	srv := couchtest.New(t)
	c := testClient(t, srv)
	got, err := c.BulkWrite(context.Background(), "movies", nil)
	if err != nil || got != nil {
		t.Fatalf("BulkWrite(nil) = %v, %v", got, err)
	}
	if srv.Last("POST", "/movies/_bulk_docs") != nil {
		t.Error("an empty batch reached the server")
	}
}

func TestAllDocsByKeysReturnsARowPerKey(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/movies/_all_docs", 200, `{"total_rows":2,"offset":0,"rows":[
		{"id":"a","key":"a","value":{"rev":"1-x"},"doc":{"_id":"a","_rev":"1-x","title":"Amelie"}},
		{"key":"gone","error":"not_found"}]}`)
	c := testClient(t, srv)
	rows, err := c.AllDocsByKeys(context.Background(), "movies", []string{"a", "gone"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if rows[0].ID != "a" || rows[0].Rev != "1-x" || !strings.Contains(string(rows[0].Doc), "Amelie") {
		t.Errorf("row 0 = %#v", rows[0])
	}
	if rows[1].ID != "gone" || rows[1].Error != "not_found" {
		t.Errorf("row 1 = %#v; a missing key keeps the id that was asked for", rows[1])
	}
	req := srv.Last("POST", "/movies/_all_docs")
	if req.Query("include_docs") != "true" {
		t.Errorf("query = %q", req.RawQuery)
	}
	if !strings.Contains(string(req.Body), `"keys"`) {
		t.Errorf("body = %s", req.Body)
	}
}

func TestAllDocsByKeysWithNoKeysAsksNothing(t *testing.T) {
	srv := couchtest.New(t)
	c := testClient(t, srv)
	rows, err := c.AllDocsByKeys(context.Background(), "movies", nil, false)
	if err != nil || rows != nil {
		t.Fatalf("AllDocsByKeys(nil) = %v, %v", rows, err)
	}
	if srv.Last("POST", "/movies/_all_docs") != nil {
		t.Error("an empty key list reached the server")
	}
}
