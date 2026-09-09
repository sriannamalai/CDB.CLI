package command

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/couch"
	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

func TestMkdirCreatesADatabase(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("PUT", "/newdb", 201, `{"ok":true}`)
	s := connected(t, srv)
	res, err := invoke(t, Mkdir(), s, "/newdb", "--partitioned", "--q", "2")
	if err != nil {
		t.Fatal(err)
	}
	msg, ok := res.(Message)
	if !ok || !strings.Contains(msg.Text, "newdb") {
		t.Errorf("result = %#v", res)
	}
	req := srv.Last("PUT", "/newdb")
	if req.Query("partitioned") != "true" || req.Query("q") != "2" {
		t.Errorf("query = %q", req.RawQuery)
	}
}

func TestMkdirRejectsANonDatabasePath(t *testing.T) {
	srv := couchtest.New(t)
	s := connected(t, srv)
	_, err := invoke(t, Mkdir(), s, "/mydb/doc1")
	if _, ok := err.(*UsageError); !ok {
		t.Fatalf("err = %#v, want *UsageError", err)
	}
}

func TestRmdirRequiresTheDatabaseNameOnATerminal(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb", 200, `{"db_name":"mydb","doc_count":3,"sizes":{"file":1,"external":1},"cluster":{"q":1,"n":1},"props":{}}`)
	srv.JSON("DELETE", "/mydb", 200, `{"ok":true}`)
	s := connected(t, srv)
	s.Prefs.Interactive = true
	s.SetStdin(strings.NewReader("wrong\n"))
	if _, err := invoke(t, Rmdir(), s, "/mydb"); err == nil {
		t.Fatal("rmdir deleted after the wrong name was typed")
	}
	if srv.Last("DELETE", "/mydb") != nil {
		t.Fatal("rmdir sent DELETE after a failed confirmation")
	}
	s.SetStdin(strings.NewReader("mydb\n"))
	if _, err := invoke(t, Rmdir(), s, "/mydb"); err != nil {
		t.Fatal(err)
	}
	if srv.Last("DELETE", "/mydb") == nil {
		t.Fatal("rmdir did not delete after the name was typed correctly")
	}
}

func TestRmdirIsDestructive(t *testing.T) {
	if !Rmdir().Destructive {
		t.Error("rmdir is not marked Destructive")
	}
}

func TestRmdirLeavesTheCwdWhenOnlyTheNamePrefixMatches(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb", 200, `{"db_name":"mydb","doc_count":0,"sizes":{"file":1,"external":1},"cluster":{"q":1,"n":1},"props":{}}`)
	srv.JSON("DELETE", "/mydb", 200, `{"ok":true}`)
	s := connected(t, srv)
	s.Prefs.Yes = true
	s.SetPath("/mydb2")
	if _, err := invoke(t, Rmdir(), s, "/mydb"); err != nil {
		t.Fatal(err)
	}
	if s.Path() != "/mydb2" {
		t.Errorf("Path() = %q after deleting /mydb, want %q", s.Path(), "/mydb2")
	}
	s.SetPath("/mydb/doc1")
	if _, err := invoke(t, Rmdir(), s, "/mydb"); err != nil {
		t.Fatal(err)
	}
	if s.Path() != "/" {
		t.Errorf("Path() = %q after deleting the database you were in, want %q", s.Path(), "/")
	}
}

func TestCpCopiesADocument(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("COPY", "/mydb/doc1", 201, `{"ok":true,"id":"doc2","rev":"1-x"}`)
	s := connected(t, srv)
	s.SetPath("/mydb")
	res, err := invoke(t, Cp(), s, "doc1", "doc2")
	if err != nil {
		t.Fatal(err)
	}
	msg, ok := res.(Message)
	if !ok || !strings.Contains(msg.Text, "1-x") {
		t.Errorf("result = %#v", res)
	}
	if got := srv.Last("COPY", "/mydb/doc1").Header.Get("Destination"); got != "doc2" {
		t.Errorf("Destination = %q", got)
	}
}

func TestCpOnAFreshDestinationSendsNoRev(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("HEAD", "/mydb/doc2", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(404) })
	srv.JSON("COPY", "/mydb/doc1", 201, `{"ok":true,"id":"doc2","rev":"1-x"}`)
	s := connected(t, srv)
	s.SetPath("/mydb")
	if _, err := invoke(t, Cp(), s, "doc1", "doc2"); err != nil {
		t.Fatal(err)
	}
	if got := srv.Last("COPY", "/mydb/doc1").Header.Get("Destination"); got != "doc2" {
		t.Errorf("Destination = %q, want no rev for a fresh destination", got)
	}
}

func TestCpOverwritesAnExistingDestination(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("HEAD", "/mydb/doc2", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `"3-old"`)
		w.WriteHeader(200)
	})
	srv.JSON("COPY", "/mydb/doc1", 201, `{"ok":true,"id":"doc2","rev":"4-new"}`)
	s := connected(t, srv)
	s.SetPath("/mydb")
	res, err := invoke(t, Cp(), s, "doc1", "doc2")
	if err != nil {
		t.Fatal(err)
	}
	msg, ok := res.(Message)
	if !ok || !strings.Contains(msg.Text, "4-new") {
		t.Errorf("result = %#v", res)
	}
	if got := srv.Last("COPY", "/mydb/doc1").Header.Get("Destination"); got != "doc2?rev=3-old" {
		t.Errorf("Destination = %q, want doc2?rev=3-old (overwrite the current revision)", got)
	}
}

func TestCpPreservesADestinationIDWithASpace(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("COPY", "/mydb/doc1", 201, `{"ok":true,"id":"doc 2","rev":"1-x"}`)
	s := connected(t, srv)
	s.SetPath("/mydb")
	if _, err := invoke(t, Cp(), s, "doc1", "doc 2"); err != nil {
		t.Fatal(err)
	}
	if got := srv.Last("COPY", "/mydb/doc1").Header.Get("Destination"); got != "doc 2" {
		t.Errorf("Destination = %q, want %q (raw, not URL-encoded)", got, "doc 2")
	}
}

func TestCpBetweenDatabasesStartsAReplication(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("HEAD", "/src", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	srv.On("HEAD", "/dst", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	srv.JSON("POST", "/_replicator", 201, `{"ok":true,"id":"cdb-cp-src-dst","rev":"1-a"}`)
	s := connected(t, srv)
	res, err := invoke(t, Cp(), s, "/src", "/dst")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := res.(Message); !ok {
		t.Fatalf("result is %T, want Message", res)
	}
	body := string(srv.Last("POST", "/_replicator").Body)
	// CouchDB 3.x rejects a bare database name in "source"/"target" outright
	// (403 local_endpoints_not_supported), so the document must carry each
	// endpoint's full URL.
	for _, want := range []string{`"url":"` + srv.URL() + `/src"`, `"url":"` + srv.URL() + `/dst"`} {
		if !strings.Contains(body, want) {
			t.Errorf("_replicator body %s is missing %s", body, want)
		}
	}
}

// TestCpBetweenDatabasesSendsPerEndpointCredentials uses a session-authenticated
// client (the credentials are only known here in the test, standing in for
// what a real profile's stored password would be) to verify that "cp" between
// two databases carries them in a per-endpoint Authorization header, never as
// a bare URL, and never anywhere the operator sees: not in the returned Message, and
// not printed to stdout.
func TestCpBetweenDatabasesSendsPerEndpointCredentials(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("HEAD", "/src", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	srv.On("HEAD", "/dst", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	srv.JSON("POST", "/_replicator", 201, `{"ok":true,"id":"cdb-cp-src-dst","rev":"1-a"}`)
	c, err := couch.New(couch.Config{URL: srv.URL(), Auth: couch.AuthSession, Username: "admin", Secret: "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	var out bytes.Buffer
	s := session.New(strings.NewReader(""), &out, &out)
	s.Attach(c, "test")
	t.Cleanup(func() { _ = s.Detach() })

	res, err := invoke(t, Cp(), s, "/src", "/dst")
	if err != nil {
		t.Fatal(err)
	}
	body := string(srv.Last("POST", "/_replicator").Body)
	// base64("admin:s3cret"); CouchDB 3.0 and 3.1 ignore the "auth" object.
	if !strings.Contains(body, `"headers":{"Authorization":"Basic YWRtaW46czNjcmV0"}`) {
		t.Errorf("_replicator body %s is missing the Basic header for the session profile", body)
	}
	msg, ok := res.(Message)
	if !ok {
		t.Fatalf("result is %T, want Message", res)
	}
	if strings.Contains(msg.Text, "s3cret") {
		t.Errorf("cp result leaks the password: %q", msg.Text)
	}
	if strings.Contains(out.String(), "s3cret") {
		t.Errorf("cp printed the password to stdout: %q", out.String())
	}
}
