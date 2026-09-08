package couch

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
)

func TestChangesReadsIDsAndLeafRevs(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb/_changes", 200, `{"results":[
		{"seq":"1-x","id":"a","changes":[{"rev":"1-aa"}]},
		{"seq":"2-y","id":"b","deleted":true,"changes":[{"rev":"2-bb"},{"rev":"2-cc"}]}],
		"last_seq":"2-y","pending":7}`)
	c := newTestClient(t, srv)
	// all_docs is no longer the hard-coded default, so backup's style is now
	// asked for explicitly — which is the point of the options struct.
	page, err := c.Changes(context.Background(), "mydb", ChangesOptions{
		Since: "0", Limit: 100, Style: StyleAllDocs,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 2 || page.LastSeq != "2-y" || page.Pending != 7 {
		t.Fatalf("page = %+v", page)
	}
	if !page.Rows[1].Deleted || len(page.Rows[1].Revs) != 2 {
		t.Errorf("row 1 = %+v", page.Rows[1])
	}
	req := srv.Last("GET", "/mydb/_changes")
	if req.Query("style") != "all_docs" || req.Query("since") != "0" || req.Query("limit") != "100" {
		t.Errorf("query = %q", req.RawQuery)
	}
	if req.Query("feed") != "normal" {
		t.Errorf("feed = %q, want normal", req.Query("feed"))
	}
}

func TestChangesSendsEveryOption(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb/_changes", 200, `{"results":[
		{"seq":"1-x","id":"a","changes":[{"rev":"1-aa"}],"doc":{"_id":"a","n":1}},
		{"seq":"2-y","id":"b","deleted":true,"changes":[{"rev":"2-bb"}]}],
		"last_seq":"2-y","pending":7}`)
	c := newTestClient(t, srv)

	page, err := c.Changes(context.Background(), "mydb", ChangesOptions{
		Since:       "1-x",
		Limit:       50,
		Style:       StyleAllDocs,
		IncludeDocs: true,
		Filter:      "app/by_type",
	})
	if err != nil {
		t.Fatal(err)
	}
	req := srv.Last("GET", "/mydb/_changes")
	if req == nil {
		t.Fatal("no _changes request reached the server")
	}
	for _, tc := range []struct{ key, want string }{
		{"feed", "normal"},
		{"since", "1-x"},
		{"limit", "50"},
		{"style", "all_docs"},
		{"include_docs", "true"},
		{"filter", "app/by_type"},
	} {
		if got := req.Query(tc.key); got != tc.want {
			t.Errorf("%s = %q, want %q", tc.key, got, tc.want)
		}
	}
	if req.Query("heartbeat") != "" {
		t.Errorf("heartbeat = %q, want empty on the normal feed", req.Query("heartbeat"))
	}
	if len(page.Rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(page.Rows))
	}
	if page.Rows[0].Seq != "1-x" || page.Rows[0].Revs[0] != "1-aa" {
		t.Errorf("row 0 = %+v", page.Rows[0])
	}
	if string(page.Rows[0].Doc) != `{"_id":"a","n":1}` {
		t.Errorf("row 0 doc = %s", page.Rows[0].Doc)
	}
	if !page.Rows[1].Deleted {
		t.Error("row 1 is not marked deleted")
	}
	if page.LastSeq != "2-y" || page.Pending != 7 {
		t.Errorf("page = %+v", page)
	}
}

func TestChangesDefaultsStyleAndSince(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb/_changes", 200, `{"results":[],"last_seq":"0","pending":0}`)
	c := newTestClient(t, srv)

	if _, err := c.Changes(context.Background(), "mydb", ChangesOptions{}); err != nil {
		t.Fatal(err)
	}
	req := srv.Last("GET", "/mydb/_changes")
	if got := req.Query("style"); got != "main_only" {
		t.Errorf("style = %q, want main_only", got)
	}
	if got := req.Query("since"); got != "0" {
		t.Errorf("since = %q, want 0", got)
	}
	if got := req.Query("limit"); got != "" {
		t.Errorf("limit = %q, want empty when Limit is 0", got)
	}
}

func TestBulkGetRequestsRevisions(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/mydb/_bulk_get", 200, `{"results":[
		{"id":"a","docs":[{"ok":{"_id":"a","_rev":"1-aa","_revisions":{"start":1,"ids":["aa"]}}}]},
		{"id":"b","docs":[{"error":{"id":"b","rev":"9-z","error":"not_found","reason":"missing"}}]}]}`)
	c := newTestClient(t, srv)
	docs, failed, err := c.BulkGet(context.Background(), "mydb", []BulkRef{{ID: "a", Rev: "1-aa"}, {ID: "b", Rev: "9-z"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 {
		t.Fatalf("docs = %d, want 1", len(docs))
	}
	if !strings.Contains(string(docs[0]), "_revisions") {
		t.Errorf("doc = %s, want _revisions", docs[0])
	}
	// The error entry is reported, never dropped: a revision the server cannot
	// hand back is missing data, not an empty result.
	if len(failed) != 1 {
		t.Fatalf("failed = %+v, want the one error entry", failed)
	}
	if failed[0] != (BulkError{ID: "b", Rev: "9-z", Error: "not_found", Reason: "missing"}) {
		t.Errorf("failed[0] = %+v", failed[0])
	}
	req := srv.Last("POST", "/mydb/_bulk_get")
	if req.Query("revs") != "true" {
		t.Errorf("revs = %q", req.Query("revs"))
	}
	if !strings.Contains(string(req.Body), `"rev":"1-aa"`) {
		t.Errorf("body = %s", req.Body)
	}
}

func TestBulkDocsNewEditsFalse(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/mydb/_bulk_docs", 201, `[]`)
	c := newTestClient(t, srv)
	errs, err := c.BulkDocs(context.Background(), "mydb", []json.RawMessage{json.RawMessage(`{"_id":"a","_rev":"1-a"}`)}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) != 0 {
		t.Errorf("errs = %+v", errs)
	}
	body := string(srv.Last("POST", "/mydb/_bulk_docs").Body)
	if !strings.Contains(body, `"new_edits":false`) {
		t.Errorf("body = %s, want new_edits false", body)
	}
}

func TestBulkDocsReportsPerDocumentErrors(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/mydb/_bulk_docs", 201, `[{"id":"a","error":"conflict","reason":"Document update conflict."}]`)
	c := newTestClient(t, srv)
	errs, err := c.BulkDocs(context.Background(), "mydb", []json.RawMessage{json.RawMessage(`{"_id":"a"}`)}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) != 1 || errs[0].Error != "conflict" {
		t.Errorf("errs = %+v", errs)
	}
}
