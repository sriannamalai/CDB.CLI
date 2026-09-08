package command

import (
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
)

// "ls --all" built its own AllDocsOptions and dropped --start, so resuming a
// long listing from where an earlier one stopped silently started again at the
// beginning. The two flags are documented on the same command and have to
// compose.
func TestLsAllHonoursStart(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb/_all_docs", 200, `{"total_rows":2,"offset":0,"rows":[
		{"id":"m","key":"m","value":{"rev":"1-m"}}]}`)
	s := connected(t, srv)
	s.SetPath("/mydb")

	res, err := invoke(t, Ls(), s, "--all", "--start", "m")
	if err != nil {
		t.Fatal(err)
	}
	st, ok := res.(Stream)
	if !ok {
		t.Fatalf("ls --all returned %#v, want a Stream", res)
	}
	// Drain it so the producer goroutine actually issues the request.
	for {
		_, more, err := st.Next()
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
	}
	req := srv.Last("GET", "/mydb/_all_docs")
	if req == nil {
		t.Fatal("no _all_docs request reached the server")
	}
	if got := req.Query("startkey_docid"); got != "m" {
		t.Errorf("startkey_docid = %q, want m — --start was dropped", got)
	}
}

// The same rule inside a partition: --start takes a raw document id, which in
// a partitioned database is the fully qualified "p1:m".
func TestLsAllHonoursStartInsideAPartition(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb/_partition/p1/_all_docs", 200, `{"total_rows":2,"offset":0,"rows":[
		{"id":"p1:m","key":"p1:m","value":{"rev":"1-m"}}]}`)
	s := connected(t, srv)

	res, err := invoke(t, Ls(), s, "/mydb/_partition/p1", "--all", "--start", "p1:m")
	if err != nil {
		t.Fatal(err)
	}
	st, ok := res.(Stream)
	if !ok {
		t.Fatalf("ls --all returned %#v, want a Stream", res)
	}
	rows := 0
	for {
		row, more, err := st.Next()
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
		if len(row.JSON) == 0 {
			t.Errorf("streamed row %d has no JSON payload", rows)
		}
		rows++
	}
	req := srv.Last("GET", "/mydb/_partition/p1/_all_docs")
	if req == nil {
		t.Fatal("no partitioned _all_docs request reached the server")
	}
	if got := req.Query("startkey_docid"); got != "p1:m" {
		t.Errorf("startkey_docid = %q, want p1:m — --start was dropped", got)
	}
	if got := req.Query("skip"); got != "" {
		t.Errorf("skip = %q; a partitioned listing pages with startkey_docid only", got)
	}
}
