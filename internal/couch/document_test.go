package couch

import (
	"context"
	"net/http"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
)

func TestGetDocumentReturnsBodyAndRev(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb/doc1", 200, `{"_id":"doc1","_rev":"1-a","n":1}`)
	c := newTestClient(t, srv)
	body, rev, err := c.GetDocument(context.Background(), "mydb", "doc1", GetOptions{Revs: true, Conflicts: true})
	if err != nil {
		t.Fatal(err)
	}
	if rev != "1-a" {
		t.Errorf("rev = %q", rev)
	}
	if string(body) != `{"_id":"doc1","_rev":"1-a","n":1}` {
		t.Errorf("body = %s", body)
	}
	req := srv.Last("GET", "/mydb/doc1")
	if req.Query("revs") != "true" || req.Query("conflicts") != "true" {
		t.Errorf("query = %q", req.RawQuery)
	}
}

func TestGetDocumentWithRev(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb/doc1", 200, `{"_id":"doc1","_rev":"1-a"}`)
	c := newTestClient(t, srv)
	if _, _, err := c.GetDocument(context.Background(), "mydb", "doc1", GetOptions{Rev: "1-a"}); err != nil {
		t.Fatal(err)
	}
	if got := srv.Last("GET", "/mydb/doc1").Query("rev"); got != "1-a" {
		t.Errorf("rev query = %q", got)
	}
}

func TestGetDesignDocumentPathIsNotEscaped(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb/_design/app", 200, `{"_id":"_design/app","_rev":"1-a"}`)
	c := newTestClient(t, srv)
	if _, _, err := c.GetDocument(context.Background(), "mydb", "_design/app", GetOptions{}); err != nil {
		t.Fatal(err)
	}
	if srv.Last("GET", "/mydb/_design/app") == nil {
		t.Error("design document request did not reach /mydb/_design/app")
	}
}

func TestPutDocumentSendsRev(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("PUT", "/mydb/doc1", 201, `{"ok":true,"id":"doc1","rev":"2-b"}`)
	c := newTestClient(t, srv)
	rev, err := c.PutDocument(context.Background(), "mydb", "doc1", []byte(`{"n":2}`), "1-a")
	if err != nil {
		t.Fatal(err)
	}
	if rev != "2-b" {
		t.Errorf("rev = %q", rev)
	}
	req := srv.Last("PUT", "/mydb/doc1")
	if req.Query("rev") != "1-a" {
		t.Errorf("rev query = %q", req.Query("rev"))
	}
	if string(req.Body) != `{"n":2}` {
		t.Errorf("body = %s", req.Body)
	}
	if got := req.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
}

func TestPutDocumentConflictIsAnError(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("PUT", "/mydb/doc1", 409, `{"error":"conflict","reason":"Document update conflict."}`)
	c := newTestClient(t, srv)
	_, err := c.PutDocument(context.Background(), "mydb", "doc1", []byte(`{}`), "1-a")
	e, ok := AsError(err)
	if !ok || e.Status != 409 || e.Name != "conflict" {
		t.Fatalf("err = %#v", err)
	}
}

func TestDeleteDocument(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("DELETE", "/mydb/doc1", 200, `{"ok":true,"id":"doc1","rev":"3-c"}`)
	c := newTestClient(t, srv)
	rev, err := c.DeleteDocument(context.Background(), "mydb", "doc1", "2-b")
	if err != nil {
		t.Fatal(err)
	}
	if rev != "3-c" {
		t.Errorf("rev = %q", rev)
	}
	if srv.Last("DELETE", "/mydb/doc1").Query("rev") != "2-b" {
		t.Error("DELETE did not send the rev")
	}
}

func TestGetRevUsesHead(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("HEAD", "/mydb/doc1", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `"4-d"`)
		w.WriteHeader(200)
	})
	c := newTestClient(t, srv)
	rev, err := c.GetRev(context.Background(), "mydb", "doc1")
	if err != nil {
		t.Fatal(err)
	}
	if rev != "4-d" {
		t.Errorf("GetRev = %q, want 4-d", rev)
	}
}

func TestGetRevOnAMissingDocumentIsA404(t *testing.T) {
	srv := couchtest.New(t)
	c := newTestClient(t, srv)
	_, err := c.GetRev(context.Background(), "mydb", "gone")
	e, ok := AsError(err)
	if !ok || e.Status != 404 || e.Name != "not_found" {
		t.Fatalf("err = %#v", err)
	}
}

func TestCopyDocument(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("COPY", "/mydb/doc1", 201, `{"ok":true,"id":"doc2","rev":"1-x"}`)
	c := newTestClient(t, srv)
	rev, err := c.CopyDocument(context.Background(), "mydb", "doc1", "doc2", "")
	if err != nil {
		t.Fatal(err)
	}
	if rev != "1-x" {
		t.Errorf("rev = %q", rev)
	}
	if got := srv.Last("COPY", "/mydb/doc1").Header.Get("Destination"); got != "doc2" {
		t.Errorf("Destination = %q", got)
	}
}

func TestCopyDocumentOverwriteSendsTheDestinationRev(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("COPY", "/mydb/doc1", 201, `{"ok":true,"id":"doc2","rev":"2-y"}`)
	c := newTestClient(t, srv)
	if _, err := c.CopyDocument(context.Background(), "mydb", "doc1", "doc2", "1-x"); err != nil {
		t.Fatal(err)
	}
	if got := srv.Last("COPY", "/mydb/doc1").Header.Get("Destination"); got != "doc2?rev=1-x" {
		t.Errorf("Destination = %q, want doc2?rev=1-x", got)
	}
}
