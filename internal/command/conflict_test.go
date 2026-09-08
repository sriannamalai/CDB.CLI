package command

import (
	"net/http"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
)

func TestConflictsInADatabaseListsOnlyConflictedDocuments(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb/_all_docs", 200, `{"total_rows":2,"offset":0,"rows":[
		{"id":"clean","key":"clean","value":{"rev":"1-a"},"doc":{"_id":"clean","_rev":"1-a"}},
		{"id":"messy","key":"messy","value":{"rev":"2-b"},"doc":{"_id":"messy","_rev":"2-b","_conflicts":["2-c","2-d"]}}]}`)
	s := connected(t, srv)
	s.SetPath("/mydb")
	res, err := invoke(t, Conflicts(), s)
	if err != nil {
		t.Fatal(err)
	}
	rows, ok := res.(Rows)
	if !ok {
		t.Fatalf("result is %T, want Rows", res)
	}
	if len(rows.Items) != 1 || rows.Items[0].Cells[0] != "messy" {
		t.Fatalf("rows = %+v", rows.Items)
	}
	if rows.Items[0].Cells[2] != "2" {
		t.Errorf("conflict count = %q, want 2", rows.Items[0].Cells[2])
	}
	req := srv.Last("GET", "/mydb/_all_docs")
	if req.Query("conflicts") != "true" || req.Query("include_docs") != "true" {
		t.Errorf("query = %q", req.RawQuery)
	}
}

func TestConflictsForOneDocumentListsRevisions(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("GET", "/mydb/messy", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("rev") {
		case "2-c":
			_, _ = w.Write([]byte(`{"_id":"messy","_rev":"2-c","v":3}`))
		case "":
			_, _ = w.Write([]byte(`{"_id":"messy","_rev":"2-b","v":2,"_conflicts":["2-c"]}`))
		default:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"not_found","reason":"missing"}`))
		}
	})
	s := connected(t, srv)
	res, err := invoke(t, Conflicts(), s, "/mydb/messy")
	if err != nil {
		t.Fatal(err)
	}
	rows, ok := res.(Rows)
	if !ok {
		t.Fatalf("result is %T, want Rows", res)
	}
	if len(rows.Items) != 2 {
		t.Fatalf("rows = %+v, want the winner and one conflict", rows.Items)
	}
	if rows.Items[0].Cells[1] != "winner" || rows.Items[1].Cells[1] != "conflict" {
		t.Errorf("roles = %q / %q", rows.Items[0].Cells[1], rows.Items[1].Cells[1])
	}
}

func TestConflictsOnACleanDocument(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb/clean", 200, `{"_id":"clean","_rev":"1-a"}`)
	s := connected(t, srv)
	res, err := invoke(t, Conflicts(), s, "/mydb/clean")
	if err != nil {
		t.Fatal(err)
	}
	msg, ok := res.(Message)
	if !ok || !strings.Contains(msg.Text, "no conflict") {
		t.Errorf("result = %#v", res)
	}
}

func TestResolveKeepsTheChosenRevision(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("GET", "/mydb/messy", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("rev") {
		case "2-c":
			_, _ = w.Write([]byte(`{"_id":"messy","_rev":"2-c","v":3}`))
		default:
			_, _ = w.Write([]byte(`{"_id":"messy","_rev":"2-b","v":2,"_conflicts":["2-c"]}`))
		}
	})
	srv.JSON("DELETE", "/mydb/messy", 200, `{"ok":true,"id":"messy","rev":"3-z"}`)
	s := connected(t, srv)
	s.Prefs.Yes = true
	res, err := invoke(t, Resolve(), s, "/mydb/messy", "--keep", "2-c")
	if err != nil {
		t.Fatal(err)
	}
	// Read the deletions back from the stub's mutex-protected request log; a
	// slice appended from the handler goroutine would race under -race.
	var deleted []string
	for _, r := range srv.Requests() {
		if r.Method == "DELETE" && r.Path == "/mydb/messy" {
			deleted = append(deleted, r.Query("rev"))
		}
	}
	if len(deleted) != 1 || deleted[0] != "2-b" {
		t.Fatalf("deleted revisions = %v, want [2-b]", deleted)
	}
	msg, ok := res.(Message)
	if !ok || !strings.Contains(msg.Text, "2-c") {
		t.Errorf("result = %#v", res)
	}
}

func TestResolveAsksWhenNoKeepIsGiven(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("GET", "/mydb/messy", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("rev") {
		case "2-c":
			_, _ = w.Write([]byte(`{"_id":"messy","_rev":"2-c","v":3}`))
		default:
			_, _ = w.Write([]byte(`{"_id":"messy","_rev":"2-b","v":2,"_conflicts":["2-c"]}`))
		}
	})
	srv.JSON("DELETE", "/mydb/messy", 200, `{"ok":true,"id":"messy","rev":"3-z"}`)
	s := connected(t, srv)
	s.Prefs.Interactive = true
	// Choose revision 2, then confirm.
	s.SetStdin(strings.NewReader("2\ny\n"))
	if _, err := invoke(t, Resolve(), s, "/mydb/messy"); err != nil {
		t.Fatal(err)
	}
	if srv.Last("DELETE", "/mydb/messy") == nil {
		t.Fatal("resolve deleted nothing")
	}
}

func TestResolveIsDestructive(t *testing.T) {
	if !Resolve().Destructive {
		t.Error("resolve is not marked Destructive")
	}
}
