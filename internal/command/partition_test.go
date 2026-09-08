package command

import (
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
)

func partitionServer(t *testing.T) *couchtest.Server {
	t.Helper()
	srv := couchtest.New(t)
	srv.JSON("HEAD", "/pdb", 200, ``)
	srv.JSON("GET", "/pdb/_partition/p1/_all_docs", 200, `{"total_rows":2,"offset":0,"rows":[
		{"id":"p1:doc1","key":"p1:doc1","value":{"rev":"1-a"}},
		{"id":"p1:doc2","key":"p1:doc2","value":{"rev":"1-b"}}]}`)
	srv.JSON("GET", "/pdb/p1:doc1", 200, `{"_id":"p1:doc1","_rev":"1-a","type":"x"}`)
	srv.JSON("POST", "/pdb/_partition/p1/_find", 200, `{"docs":[{"_id":"p1:doc1","_rev":"1-a"}],"bookmark":"bk"}`)
	srv.JSON("GET", "/pdb/_partition/p1/_design/app/_view/by_type", 200, `{"total_rows":1,"offset":0,"rows":[
		{"id":"p1:doc1","key":"x","value":1}]}`)
	srv.JSON("GET", "/pdb/_design/app", 200, `{"_id":"_design/app","_rev":"1-d","views":{"by_type":{"map":"function(doc){}"}},"options":{"partitioned":true}}`)
	return srv
}

func TestLsListsAPartition(t *testing.T) {
	srv := partitionServer(t)
	s := connected(t, srv)
	res, err := invoke(t, Ls(), s, "/pdb/_partition/p1")
	if err != nil {
		t.Fatal(err)
	}
	rows := res.(Rows)
	if len(rows.Items) != 2 || rows.Items[0].Cells[0] != "p1:doc1" {
		t.Fatalf("rows = %+v", rows.Items)
	}
	req := srv.Last("GET", "/pdb/_partition/p1/_all_docs")
	if req == nil {
		t.Fatal("ls did not use the partitioned _all_docs")
	}
	// Paging inside a partition follows the same rule as everywhere else.
	if req.Query("skip") != "" {
		t.Errorf("ls sent skip=%q; paging is startkey_docid only", req.Query("skip"))
	}
	// Every row carries its own JSON so --json and the jq pipe have something
	// to render.
	for i, row := range rows.Items {
		if len(row.JSON) == 0 {
			t.Errorf("row %d has no JSON payload", i)
		}
	}
}

func TestCatReadsAPartitionedDocument(t *testing.T) {
	srv := partitionServer(t)
	s := connected(t, srv)
	res, err := invoke(t, Cat(), s, "/pdb/_partition/p1/doc1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res.(Document).JSON), `"_id":"p1:doc1"`) {
		t.Errorf("document = %s", res.(Document).JSON)
	}
	if srv.Last("GET", "/pdb/p1:doc1") == nil {
		t.Error("cat did not read /pdb/p1:doc1; the partition belongs in the id, not the URL")
	}
}

func TestFindQueriesAPartition(t *testing.T) {
	srv := partitionServer(t)
	s := connected(t, srv)
	if _, err := invoke(t, Find(), s, "/pdb/_partition/p1", `{"type":"x"}`); err != nil {
		t.Fatal(err)
	}
	req := srv.Last("POST", "/pdb/_partition/p1/_find")
	if req == nil {
		t.Fatal("find did not use the partitioned _find")
	}
	if !strings.Contains(string(req.Body), `"selector":{"type":"x"}`) {
		t.Errorf("body = %s", req.Body)
	}
}

func TestQueryRunsAViewAgainstAPartition(t *testing.T) {
	srv := partitionServer(t)
	s := connected(t, srv)
	if _, err := invoke(t, Query(), s, "/pdb/_partition/p1/_design/app/_view/by_type"); err != nil {
		t.Fatal(err)
	}
	if srv.Last("GET", "/pdb/_partition/p1/_design/app/_view/by_type") == nil {
		t.Fatal("query did not use the partitioned view URL")
	}
}

func TestCdVerifiesAPartitionedView(t *testing.T) {
	srv := partitionServer(t)
	s := connected(t, srv)
	if _, err := invoke(t, Cd(), s, "/pdb/_partition/p1/_design/app/_view/by_type"); err != nil {
		t.Fatal(err)
	}
	if s.Path() != "/pdb/_partition/p1/_design/app/_view/by_type" {
		t.Errorf("path = %q", s.Path())
	}
	// The design document is read unpartitioned: it is not partition-scoped.
	if srv.Last("GET", "/pdb/_design/app") == nil {
		t.Error("cd did not read /pdb/_design/app")
	}
}

// "/pdb/_partition" is not a path anything can stand on, so ".." out of a
// partition has to skip over it and land on the database.
func TestCdUpFromAPartitionLandsOnTheDatabase(t *testing.T) {
	srv := partitionServer(t)
	s := connected(t, srv)
	s.SetPath("/pdb/_partition/p1")
	if _, err := invoke(t, Cd(), s, ".."); err != nil {
		t.Fatal(err)
	}
	if s.Path() != "/pdb" {
		t.Errorf("path = %q, want /pdb", s.Path())
	}
}

func TestCdUpFromAPartitionedDocumentLandsOnThePartition(t *testing.T) {
	srv := partitionServer(t)
	s := connected(t, srv)
	s.SetPath("/pdb/_partition/p1/doc1")
	if _, err := invoke(t, Cd(), s, ".."); err != nil {
		t.Fatal(err)
	}
	if s.Path() != "/pdb/_partition/p1" {
		t.Errorf("path = %q, want /pdb/_partition/p1", s.Path())
	}
	if _, err := invoke(t, Cd(), s, ".."); err != nil {
		t.Fatal(err)
	}
	if s.Path() != "/pdb" {
		t.Errorf("path after the second .. = %q, want /pdb", s.Path())
	}
}

func TestCompletePathInsideAPartitionStripsTheKey(t *testing.T) {
	srv := partitionServer(t)
	s := connected(t, srv)
	got := CompletePath(t.Context(), s, nil, "/pdb/_partition/p1/doc")
	var values, displays []string
	for _, c := range got {
		values = append(values, c.Value)
		displays = append(displays, c.Display)
	}
	if len(values) != 2 || values[0] != "/pdb/_partition/p1/doc1" {
		t.Fatalf("values = %v", values)
	}
	if displays[0] != "doc1" {
		t.Errorf("display = %q, want the id without the p1: prefix", displays[0])
	}
	// The lookup asks for the fully qualified key range.
	req := srv.Last("GET", "/pdb/_partition/p1/_all_docs")
	if got := req.Query("startkey_docid"); got != "p1:doc" {
		t.Errorf("startkey_docid = %q, want p1:doc", got)
	}
	if got := req.Query("endkey_docid"); got != "p1:doc"+idSentinel {
		t.Errorf("endkey_docid = %q, want the qualified prefix range", got)
	}
}

// A partitioned id pasted out of "ls" already carries its "p1:" prefix, and
// the path grammar accepts it as the same document the short form names. The
// key range completion asks for has to follow that rule too, or the one form
// an operator is most likely to paste completes to nothing.
func TestCompletePathInsideAPartitionAcceptsAQualifiedID(t *testing.T) {
	srv := partitionServer(t)
	s := connected(t, srv)
	got := CompletePath(t.Context(), s, nil, "/pdb/_partition/p1/p1:doc")
	if len(got) != 2 || got[0].Value != "/pdb/_partition/p1/doc1" || got[0].Display != "doc1" {
		t.Fatalf("candidates = %+v", got)
	}
	if q := srv.Last("GET", "/pdb/_partition/p1/_all_docs").Query("startkey_docid"); q != "p1:doc" {
		t.Errorf("startkey_docid = %q, want p1:doc rather than a doubled prefix", q)
	}
}

func TestCompletePathOffersDesignInsideAPartition(t *testing.T) {
	srv := partitionServer(t)
	s := connected(t, srv)
	got := CompletePath(t.Context(), s, nil, "/pdb/_partition/p1/_de")
	if len(got) == 0 || got[0].Value != "/pdb/_partition/p1/_design" {
		t.Fatalf("candidates = %+v; a partitioned _all_docs never returns a design document, so the segment has to be offered", got)
	}
}

func TestCompletePathOffersPartitionedViewNames(t *testing.T) {
	srv := partitionServer(t)
	s := connected(t, srv)
	got := CompletePath(t.Context(), s, nil, "/pdb/_partition/p1/_design/app/_view/by")
	if len(got) != 1 || got[0].Value != "/pdb/_partition/p1/_design/app/_view/by_type" {
		t.Fatalf("candidates = %+v", got)
	}
	got = CompletePath(t.Context(), s, nil, "/pdb/_partition/p1/_design/app/_vi")
	if len(got) != 1 || got[0].Value != "/pdb/_partition/p1/_design/app/_view" {
		t.Fatalf("candidates for the _view segment = %+v", got)
	}
}
