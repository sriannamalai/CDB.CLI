package couch

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
)

func TestGetAttachmentStreams(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("GET", "/mydb/doc1/photo.jpg", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept-Encoding") != "identity" {
			t.Errorf("Accept-Encoding = %q, want identity", r.Header.Get("Accept-Encoding"))
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "JPEGDATA")
	})
	c := newTestClient(t, srv)
	att, err := c.GetAttachment(context.Background(), "mydb", "doc1", "photo.jpg", "")
	if err != nil {
		t.Fatal(err)
	}
	defer att.Content.Close()
	b, err := io.ReadAll(att.Content)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "JPEGDATA" || att.ContentType != "image/jpeg" {
		t.Errorf("attachment = %q / %q", b, att.ContentType)
	}
}

func TestGetAttachmentNotFound(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb/doc1/nope.jpg", 404, `{"error":"not_found","reason":"Document is missing attachment"}`)
	c := newTestClient(t, srv)
	_, err := c.GetAttachment(context.Background(), "mydb", "doc1", "nope.jpg", "")
	e, ok := AsError(err)
	if !ok || e.Status != 404 {
		t.Fatalf("err = %#v", err)
	}
	if e.Reason != "Document is missing attachment" {
		t.Errorf("reason = %q", e.Reason)
	}
}

func TestGetAttachmentSendsRev(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("GET", "/mydb/doc1/photo.jpg", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "old")
	})
	c := newTestClient(t, srv)
	att, err := c.GetAttachment(context.Background(), "mydb", "doc1", "photo.jpg", "1-a")
	if err != nil {
		t.Fatal(err)
	}
	defer att.Content.Close()
	_, _ = io.Copy(io.Discard, att.Content)
	if got := srv.Last("GET", "/mydb/doc1/photo.jpg").Query("rev"); got != "1-a" {
		t.Errorf("rev query = %q, want 1-a", got)
	}
}

func TestPutAttachmentSendsContentTypeAndRev(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("PUT", "/mydb/doc1/photo.jpg", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "image/jpeg" {
			t.Errorf("Content-Type = %q", r.Header.Get("Content-Type"))
		}
		if r.ContentLength != 8 {
			t.Errorf("Content-Length = %d, want 8", r.ContentLength)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = io.WriteString(w, `{"ok":true,"id":"doc1","rev":"2-b"}`)
	})
	c := newTestClient(t, srv)
	rev, err := c.PutAttachment(context.Background(), "mydb", "doc1", "photo.jpg", "image/jpeg", 8, strings.NewReader("JPEGDATA"), "1-a")
	if err != nil {
		t.Fatal(err)
	}
	if rev != "2-b" {
		t.Errorf("rev = %q", rev)
	}
	req := srv.Last("PUT", "/mydb/doc1/photo.jpg")
	if req.Query("rev") != "1-a" {
		t.Errorf("rev query = %q", req.Query("rev"))
	}
	if string(req.Body) != "JPEGDATA" {
		t.Errorf("body = %q", req.Body)
	}
}

func TestPutAttachmentDefaultsContentType(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("PUT", "/mydb/doc1/blob", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/octet-stream" {
			t.Errorf("Content-Type = %q, want application/octet-stream", r.Header.Get("Content-Type"))
		}
		w.WriteHeader(201)
		_, _ = io.WriteString(w, `{"ok":true,"id":"doc1","rev":"2-b"}`)
	})
	c := newTestClient(t, srv)
	if _, err := c.PutAttachment(context.Background(), "mydb", "doc1", "blob", "", 1, strings.NewReader("x"), "1-a"); err != nil {
		t.Fatal(err)
	}
}

func TestPutAttachmentEscapesNames(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("PUT", "/my db/_design/app/a b.txt", 201, `{"ok":true,"id":"_design/app","rev":"2-b"}`)
	c := newTestClient(t, srv)
	if _, err := c.PutAttachment(context.Background(), "my db", "_design/app", "a b.txt", "text/plain", 1, strings.NewReader("x"), "1-a"); err != nil {
		t.Fatal(err)
	}
	if srv.Last("PUT", "/my db/_design/app/a b.txt") == nil {
		t.Error("the attachment path was not escaped as one segment per name")
	}
}

func TestDeleteAttachment(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("DELETE", "/mydb/doc1/photo.jpg", 200, `{"ok":true,"id":"doc1","rev":"3-c"}`)
	c := newTestClient(t, srv)
	rev, err := c.DeleteAttachment(context.Background(), "mydb", "doc1", "photo.jpg", "2-b")
	if err != nil {
		t.Fatal(err)
	}
	if rev != "3-c" {
		t.Errorf("rev = %q", rev)
	}
	if got := srv.Last("DELETE", "/mydb/doc1/photo.jpg").Query("rev"); got != "2-b" {
		t.Errorf("rev query = %q, want 2-b", got)
	}
}

func TestListAttachments(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb/doc1", 200, `{"_id":"doc1","_rev":"2-b","_attachments":{
		"photo.jpg":{"content_type":"image/jpeg","length":12345,"digest":"md5-x","revpos":2,"stub":true},
		"notes.txt":{"content_type":"text/plain","length":10,"digest":"md5-y","revpos":2,"stub":true}}}`)
	c := newTestClient(t, srv)
	atts, err := c.ListAttachments(context.Background(), "mydb", "doc1")
	if err != nil {
		t.Fatal(err)
	}
	if len(atts) != 2 {
		t.Fatalf("got %d attachments, want 2", len(atts))
	}
	if atts[0].Name != "notes.txt" {
		t.Errorf("attachments are not sorted by name: %+v", atts)
	}
	want := AttachmentMeta{Name: "photo.jpg", ContentType: "image/jpeg", Digest: "md5-x", Length: 12345, RevPos: 2}
	if atts[1] != want {
		t.Errorf("attachment = %+v, want %+v", atts[1], want)
	}
}
