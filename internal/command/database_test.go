package command

import (
	"net/http"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
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
	for _, want := range []string{`"source"`, `"target"`, "/src", "/dst"} {
		if !strings.Contains(body, want) {
			t.Errorf("_replicator body %s is missing %s", body, want)
		}
	}
}
