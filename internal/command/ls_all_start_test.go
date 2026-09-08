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
