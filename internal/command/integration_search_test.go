package command

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// nouveauTestEnv skips unless the server behind CDB_TEST_URL is wired to
// Nouveau. CDB_TEST_NOUVEAU is set only on the CI leg that runs the nouveau
// service container and on the local cdb-test-35nv container: a plain
// couchdb:3.5 answers 404 for every _nouveau path, so without it the test
// would assert nothing. integrationSession applies the CDB_TEST_URL gate.
func nouveauTestEnv(t *testing.T) {
	t.Helper()
	if os.Getenv("CDB_TEST_NOUVEAU") == "" {
		t.Skip("set CDB_TEST_NOUVEAU=1 against a server wired to Nouveau to run the search tests; see the Development section of the README")
	}
}

// The whole search path end to end: create a database, define a nouveau index,
// write documents, query them, page by bookmark, and read the index's info.
func TestIntegrationNouveauSearch(t *testing.T) {
	nouveauTestEnv(t)
	s := integrationSession(t)
	ctx := context.Background()
	db := "rv9search" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if err := s.Client.CreateDatabase(ctx, db, false, 0); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Client.DestroyDatabase(context.Background(), db) }()

	// genre is indexed as "string" rather than "text" because Nouveau counts
	// facets off the doc values a string field carries; a text field has none
	// and answers a --counts query with a 400.
	ddoc := `{"nouveau":{"by_title":{"index":"function(doc){ if (doc.title) { index(\"text\", \"title\", doc.title, {\"store\": true}); index(\"string\", \"genre\", doc.genre || \"none\", {\"store\": true}); } }"}}}`
	if _, err := s.Client.PutDocument(ctx, db, "_design/app", json.RawMessage(ddoc), ""); err != nil {
		t.Fatalf("put the design document: %v", err)
	}
	for i, title := range []string{"Arrival", "Annihilation", "Alien", "Interstellar"} {
		if _, err := s.Client.PutDocument(ctx, db, "m"+strconv.Itoa(i), json.RawMessage(`{"title":"`+title+`","genre":"scifi"}`), ""); err != nil {
			t.Fatalf("put m%d: %v", i, err)
		}
	}

	idx := "/" + db + "/_design/app/_nouveau/by_title"
	// The first query builds the index, which can take a moment on a cold
	// Nouveau; retry rather than sleeping a fixed amount.
	var rows Rows
	deadline := time.Now().Add(90 * time.Second)
	for {
		res, err := invoke(t, Search(), s, idx, "title:A*", "--limit", "2")
		if err == nil {
			rows = res.(Rows)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("search never succeeded: %v", err)
		}
		time.Sleep(time.Second)
	}
	if len(rows.Items) != 2 {
		t.Fatalf("got %d rows, want the 2 the limit asked for: %#v", len(rows.Items), rows.Items)
	}
	if !strings.Contains(rows.Hint, "--bookmark") {
		t.Fatalf("no paging hint on a full page: %q", rows.Hint)
	}
	// The bookmark from the hint must fetch the next match and no repeat.
	first := rows.Items[0].Cells[0]
	mark := strings.TrimSuffix(strings.SplitN(rows.Hint, `--bookmark "`, 2)[1], `"`)
	res2, err := invoke(t, Search(), s, idx, "title:A*", "--limit", "2", "--bookmark", mark)
	if err != nil {
		t.Fatalf("the bookmarked page failed: %v", err)
	}
	for _, r := range res2.(Rows).Items {
		if r.Cells[0] == first {
			t.Errorf("the bookmarked page repeated %q", first)
		}
	}

	// Counts come back when asked for.
	res3, err := invoke(t, Search(), s, idx, "title:A*", "--counts", "genre")
	if err != nil {
		t.Fatalf("counts query: %v", err)
	}
	if len(res3.(Rows).Extra) == 0 {
		t.Error("--counts produced no counts member")
	}

	info, err := invoke(t, Info(), s, idx)
	if err != nil {
		t.Fatalf("info on a nouveau index: %v", err)
	}
	got := map[string]string{}
	for _, r := range info.(Rows).Items {
		got[r.Cells[0]] = r.Cells[1]
	}
	if got["backend"] != "nouveau" {
		t.Errorf("info rows = %v", got)
	}
}

// On a plain server the two "no backend here" sentences are what an operator
// sees, and they are the point of spec section 5.3.
func TestIntegrationSearchSentencesOnAPlainServer(t *testing.T) {
	if os.Getenv("CDB_TEST_URL") == "" || os.Getenv("CDB_TEST_NOUVEAU") != "" {
		t.Skip("set CDB_TEST_URL, and leave CDB_TEST_NOUVEAU unset, to check the no-backend sentences")
	}
	s := integrationSession(t)
	ctx := context.Background()
	db := "rv9nosearch" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if err := s.Client.CreateDatabase(ctx, db, false, 0); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Client.DestroyDatabase(context.Background(), db) }()
	if _, err := s.Client.PutDocument(ctx, db, "_design/app",
		json.RawMessage(`{"indexes":{"by_title":{"index":"function(doc){}"}},"nouveau":{"by_body":{"index":"function(doc){}"}}}`), ""); err != nil {
		t.Fatal(err)
	}

	_, err := invoke(t, Search(), s, "/"+db+"/_design/app/_search/by_title", "*:*")
	if err == nil || !strings.Contains(err.Error(), "Clouseau must be installed") {
		t.Errorf("_search on a plain server = %v", err)
	}
	_, err = invoke(t, Search(), s, "/"+db+"/_design/app/_nouveau/by_body", "*:*")
	if err == nil || !strings.Contains(err.Error(), "Nouveau is not enabled") {
		t.Errorf("_nouveau on a plain server = %v", err)
	}
}
