package replicate

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/couch"
	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
)

func testClient(t *testing.T, srv *couchtest.Server) *couch.Client {
	t.Helper()
	c, err := couch.New(couch.Config{URL: srv.URL(), Auth: couch.AuthNone})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// mustEndpoint resolves s against cl, failing the test if it cannot.
func mustEndpoint(t *testing.T, cl *couch.Client, s string) Endpoint {
	t.Helper()
	e, err := ResolveEndpoint(cl, "/", s)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestCreateWritesAReplicatorDocument(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/_replicator", 201, `{"ok":true,"id":"job1","rev":"1-a"}`)
	c := testClient(t, srv)
	id, err := Create(context.Background(), c, Request{
		ID:           "job1",
		Source:       mustEndpoint(t, c, "http://a.example.com/src"),
		Target:       mustEndpoint(t, c, "http://b.example.com/dst"),
		Continuous:   true,
		CreateTarget: true,
		Filter:       "app/mine",
	})
	if err != nil {
		t.Fatal(err)
	}
	if id != "job1" {
		t.Errorf("id = %q", id)
	}
	body := string(srv.Last("POST", "/_replicator").Body)
	for _, want := range []string{
		`"_id":"job1"`,
		`"source":{"url":"http://a.example.com/src"}`,
		`"target":{"url":"http://b.example.com/dst"}`,
		`"continuous":true`,
		`"create_target":true`,
		`"filter":"app/mine"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body %s is missing %s", body, want)
		}
	}
}

func TestCreateOmitsOptionalFields(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/_replicator", 201, `{"ok":true,"id":"auto","rev":"1-a"}`)
	c := testClient(t, srv)
	req := Request{
		Source: mustEndpoint(t, c, "http://a.example.com/src"),
		Target: mustEndpoint(t, c, "http://b.example.com/dst"),
	}
	if _, err := Create(context.Background(), c, req); err != nil {
		t.Fatal(err)
	}
	body := string(srv.Last("POST", "/_replicator").Body)
	for _, unwanted := range []string{`"filter"`, `"continuous"`, `"create_target"`, `"_id"`} {
		if strings.Contains(body, unwanted) {
			t.Errorf("body %s should not contain %s", body, unwanted)
		}
	}
}

// TestCreateSendsPerEndpointCredentials pins the reason Create takes an
// Endpoint rather than a URL string: CouchDB 3.x refuses a bare database name
// (403 local_endpoints_not_supported), so a same-server replication needs a
// full URL, and the credentials for it travel in CouchDB's per-endpoint auth
// object inside the request body, never in the URL.
func TestCreateSendsPerEndpointCredentials(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/_replicator", 201, `{"ok":true,"id":"job1","rev":"1-a"}`)
	c, err := couch.New(couch.Config{URL: srv.URL(), Auth: couch.AuthSession, Username: "admin", Secret: "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	src := mustEndpoint(t, c, "/mydb")
	if strings.Contains(src.URL, "s3cret") {
		t.Errorf("Endpoint.URL = %q, which carries the password", src.URL)
	}
	if _, err := Create(context.Background(), c, Request{Source: src, Target: mustEndpoint(t, c, "/other")}); err != nil {
		t.Fatal(err)
	}
	body := string(srv.Last("POST", "/_replicator").Body)
	if !strings.Contains(body, `"basic":{"password":"s3cret","username":"admin"}`) {
		t.Errorf("body %s is missing the per-endpoint credentials", body)
	}
	if !strings.Contains(body, `"url":"`+srv.URL()+`/mydb"`) {
		t.Errorf("body %s is missing the full source URL", body)
	}
}

func TestList(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_scheduler/docs", 200, `{"total_rows":2,"offset":0,"docs":[
		{"database":"_replicator","doc_id":"job1","id":"abc+continuous","source":"http://a/src/","target":"http://b/dst/","state":"running","node":"nonode@nohost","error_count":0,"last_updated":"2026-09-08T00:00:00Z","info":{"docs_written":5}},
		{"database":"_replicator","doc_id":"job2","id":null,"source":"http://a/s2/","target":"http://b/t2/","state":"crashing","node":"nonode@nohost","error_count":3,"last_updated":"2026-09-08T00:01:00Z","info":{"error":"unauthorized"}}]}`)
	c := testClient(t, srv)
	list, err := List(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("list = %d entries, want 2", len(list))
	}
	if list[0].DocID != "job1" || list[0].State != "running" || list[0].ID != "abc+continuous" {
		t.Errorf("entry 0 = %+v", list[0])
	}
	if list[0].Source != "http://a/src/" || list[0].Target != "http://b/dst/" {
		t.Errorf("entry 0 endpoints = %q, %q", list[0].Source, list[0].Target)
	}
	if list[1].ErrorCount != 3 || list[1].Error == "" {
		t.Errorf("entry 1 = %+v", list[1])
	}
}

// TestListRedactsEndpointCredentials covers both shapes a _scheduler/docs
// endpoint can take: the object form a cdb-written document produces, and a
// URL with userinfo. Neither may reach a Status, because Status is marshalled
// straight into the --json output.
func TestListRedactsEndpointCredentials(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_scheduler/docs", 200, `{"docs":[
		{"database":"_replicator","doc_id":"job1","id":"abc",
		 "source":{"url":"http://a.example.com/src","auth":{"basic":{"username":"admin","password":"s3cret"}}},
		 "target":"http://admin:s3cret@b.example.com/dst/",
		 "state":"running","node":"n1","error_count":0,"last_updated":"2026-09-08T00:00:00Z"}]}`)
	c := testClient(t, srv)
	list, err := List(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("list = %d entries, want 1", len(list))
	}
	if list[0].Source != "http://a.example.com/src" {
		t.Errorf("source = %q, want the bare URL", list[0].Source)
	}
	if list[0].Target != "http://b.example.com/dst/" {
		t.Errorf("target = %q, want the URL without userinfo", list[0].Target)
	}
	b, err := json.Marshal(list[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"s3cret", "auth", "admin"} {
		if strings.Contains(string(b), leak) {
			t.Errorf("status JSON %s leaks %q", b, leak)
		}
	}
}

func TestShow(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_scheduler/docs/_replicator/job1", 200, `{"database":"_replicator","doc_id":"job1","id":"abc","source":"http://a/","target":"http://b/","state":"running","node":"n1","error_count":0,"last_updated":"2026-09-08T00:00:00Z","info":{"docs_written":5}}`)
	c := testClient(t, srv)
	st, err := Show(context.Background(), c, "job1")
	if err != nil {
		t.Fatal(err)
	}
	if st.DocID != "job1" || st.State != "running" {
		t.Errorf("status = %+v", st)
	}
}

func TestShowRedactsEndpointCredentials(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_scheduler/docs/_replicator/job1", 200, `{"database":"_replicator","doc_id":"job1","id":"abc",
		"source":{"url":"http://a.example.com/src","headers":{"Authorization":"Bearer tok.en.sig"}},
		"target":"http://admin:s3cret@b.example.com/dst/","state":"running","node":"n1","error_count":0,"last_updated":"2026-09-08T00:00:00Z"}`)
	c := testClient(t, srv)
	st, err := Show(context.Background(), c, "job1")
	if err != nil {
		t.Fatal(err)
	}
	if st.Source != "http://a.example.com/src" || st.Target != "http://b.example.com/dst/" {
		t.Errorf("endpoints = %q, %q", st.Source, st.Target)
	}
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"tok.en.sig", "s3cret", "Bearer"} {
		if strings.Contains(string(b), leak) {
			t.Errorf("status JSON %s leaks %q", b, leak)
		}
	}
}

func TestCancelDeletesTheDocument(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("HEAD", "/_replicator/job1", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `"1-a"`)
		w.WriteHeader(200)
	})
	srv.JSON("DELETE", "/_replicator/job1", 200, `{"ok":true,"id":"job1","rev":"2-b"}`)
	c := testClient(t, srv)
	if err := Cancel(context.Background(), c, "job1"); err != nil {
		t.Fatal(err)
	}
	if srv.Last("DELETE", "/_replicator/job1").Query("rev") != "1-a" {
		t.Error("cancel did not send the current revision")
	}
}

func TestResolveEndpoint(t *testing.T) {
	srv := couchtest.New(t)
	c := testClient(t, srv)

	got := mustEndpoint(t, c, "/mydb")
	if got.URL != c.URL()+"/mydb" {
		t.Errorf("ResolveEndpoint(/mydb).URL = %q, want %q", got.URL, c.URL()+"/mydb")
	}
	// CouchDB 3.x rejects a bare database name with 403
	// local_endpoints_not_supported, so a local path has to become a full URL.
	if got.wire()["url"] != c.URL()+"/mydb" {
		t.Errorf("ResolveEndpoint(/mydb) wire form = %v", got.wire())
	}

	got = mustEndpoint(t, c, "https://other.example.com/remote")
	if got.URL != "https://other.example.com/remote" {
		t.Errorf("ResolveEndpoint(url) = %q", got.URL)
	}

	if _, err := ResolveEndpoint(c, "/", "/mydb/doc1"); err == nil {
		t.Error("ResolveEndpoint accepted a document path")
	}
	if _, err := ResolveEndpoint(c, "/", "/"); err == nil {
		t.Error("ResolveEndpoint accepted the server root")
	}
}

// TestResolveEndpointMovesURLUserinfoIntoAuth keeps a password out of the URL
// CouchDB stores and out of anything cdb prints, while still authenticating.
func TestResolveEndpointMovesURLUserinfoIntoAuth(t *testing.T) {
	srv := couchtest.New(t)
	c := testClient(t, srv)
	got := mustEndpoint(t, c, "https://admin:s3cret@other.example.com/remote")
	if got.URL != "https://other.example.com/remote" {
		t.Errorf("Endpoint.URL = %q, want the URL without userinfo", got.URL)
	}
	if got.String() != got.URL {
		t.Errorf("Endpoint.String() = %q, want %q", got.String(), got.URL)
	}
	b, err := json.Marshal(got.wire())
	if err != nil {
		t.Fatal(err)
	}
	want := `{"auth":{"basic":{"password":"s3cret","username":"admin"}},"url":"https://other.example.com/remote"}`
	if string(b) != want {
		t.Errorf("wire = %s, want %s", b, want)
	}
}

func TestResolveEndpointRejectsAnUnsupportedScheme(t *testing.T) {
	srv := couchtest.New(t)
	c := testClient(t, srv)
	if _, err := ResolveEndpoint(c, "/", "ftp://other.example.com/remote"); err == nil {
		t.Error("ResolveEndpoint accepted an ftp URL")
	}
}
