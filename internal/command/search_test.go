package command

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/couch"
	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
)

const ddocWithIndexes = `{"_id":"_design/app","indexes":{"by_title":{"index":"function(doc){}"}},"nouveau":{"by_body":{"index":"function(doc){}"}}}`

func TestSearchRendersRowsAndTheScore(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/movies/_design/app/_search/by_title", 200, `{
		"total_rows":2,"bookmark":"g1AAAA",
		"rows":[{"id":"m1","order":[1.25,"m1"],"fields":{"title":"Arrival"}},
		        {"id":"m2","order":[0.75,"m2"],"fields":{"title":"Annihilation"}}]}`)
	s := connected(t, srv)
	res, err := invoke(t, Search(), s, "/movies/_design/app/_search/by_title", "title:a*")
	if err != nil {
		t.Fatal(err)
	}
	rows, ok := res.(Rows)
	if !ok {
		t.Fatalf("result is %T, want Rows", res)
	}
	if len(rows.Columns) != 3 || rows.Columns[0].Title != "id" || rows.Columns[1].Title != "score" || rows.Columns[2].Title != "fields" {
		t.Fatalf("columns = %#v", rows.Columns)
	}
	if len(rows.Items) != 2 || rows.Items[0].Cells[0] != "m1" || rows.Items[0].Cells[1] != "1.25" {
		t.Errorf("row 0 = %v", rows.Items[0].Cells)
	}
	if !strings.Contains(rows.Items[0].Cells[2], `"title":"Arrival"`) {
		t.Errorf("fields cell = %q", rows.Items[0].Cells[2])
	}
	// Row.JSON carries the normalised row, not the cells.
	var got struct {
		ID     string         `json:"id"`
		Order  []any          `json:"order"`
		Fields map[string]any `json:"fields"`
	}
	if err := json.Unmarshal(rows.Items[0].JSON, &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != "m1" || len(got.Order) != 2 || got.Fields["title"] != "Arrival" {
		t.Errorf("row JSON = %#v", got)
	}
}

// The paging hint is find's, in search's words: it appears only when the page
// is full and a bookmark came back, because a bookmark alone means nothing.
func TestSearchPagingHint(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/movies/_design/app/_search/by_title", 200,
		`{"total_rows":9,"bookmark":"g1AAAA","rows":[{"id":"m1","order":[1.0,"m1"],"fields":{}},{"id":"m2","order":[0.9,"m2"],"fields":{}}]}`)
	s := connected(t, srv)
	res, err := invoke(t, Search(), s, "/movies/_design/app/_search/by_title", "title:a*", "--limit", "2")
	if err != nil {
		t.Fatal(err)
	}
	want := `more results: search /movies/_design/app/_search/by_title "title:a*" --bookmark "g1AAAA"`
	if got := res.(Rows).Hint; got != want {
		t.Errorf("hint  = %q\nwant %q", got, want)
	}
}

func TestSearchNoHintWhenThePageIsNotFull(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/movies/_design/app/_search/by_title", 200,
		`{"total_rows":1,"bookmark":"g1AAAA","rows":[{"id":"m1","order":[1.0,"m1"],"fields":{}}]}`)
	s := connected(t, srv)
	res, err := invoke(t, Search(), s, "/movies/_design/app/_search/by_title", "title:a*", "--limit", "25")
	if err != nil {
		t.Fatal(err)
	}
	if got := res.(Rows).Hint; got != "" {
		t.Errorf("hint = %q, want none", got)
	}
}

func TestSearchIncludeDocsAddsAColumn(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/movies/_design/app/_nouveau/by_body", 200, `{
		"total_hits":1,"bookmark":"W3s",
		"hits":[{"id":"m1","order":[{"value":1.25,"@type":"float"}],"fields":{"title":"Arrival"},"doc":{"_id":"m1","title":"Arrival"}}]}`)
	s := connected(t, srv)
	res, err := invoke(t, Search(), s, "/movies/_design/app/_nouveau/by_body", "arrival", "--include-docs")
	if err != nil {
		t.Fatal(err)
	}
	rows := res.(Rows)
	if len(rows.Columns) != 4 || rows.Columns[3].Title != "document" {
		t.Fatalf("columns = %#v", rows.Columns)
	}
	if !strings.Contains(rows.Items[0].Cells[3], `"title":"Arrival"`) {
		t.Errorf("document cell = %q", rows.Items[0].Cells[3])
	}
	if rows.Items[0].Cells[1] != "1.25" {
		t.Errorf("score = %q; Nouveau's wrapped order must be unwrapped", rows.Items[0].Cells[1])
	}
}

func TestSearchCountsAndRangesRideInTheHintAndTheJSONExtra(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/movies/_design/app/_search/by_title", 200, `{
		"total_rows":1,"rows":[{"id":"m1","order":[1.0,"m1"],"fields":{}}],
		"counts":{"genre":{"scifi":1}},"ranges":{"year":{"old":0}}}`)
	s := connected(t, srv)
	res, err := invoke(t, Search(), s, "/movies/_design/app/_search/by_title", "*:*", "--counts", "genre")
	if err != nil {
		t.Fatal(err)
	}
	rows := res.(Rows)
	if !strings.Contains(rows.Hint, `counts: {"genre":{"scifi":1}}`) {
		t.Errorf("hint = %q", rows.Hint)
	}
	if !strings.Contains(rows.Hint, `ranges: {"year":{"old":0}}`) {
		t.Errorf("hint = %q", rows.Hint)
	}
	if string(rows.Extra["counts"]) != `{"genre":{"scifi":1}}` {
		t.Errorf("Extra[counts] = %s", rows.Extra["counts"])
	}
	if string(rows.Extra["ranges"]) != `{"year":{"old":0}}` {
		t.Errorf("Extra[ranges] = %s", rows.Extra["ranges"])
	}
	// The counts parameter reached the server as a JSON array.
	if got := srv.Requests()[0].Query("counts"); got != `["genre"]` {
		t.Errorf("counts parameter = %q", got)
	}
}

// Nouveau answers with "counts":{} and "ranges":{} on every response, asked
// for or not, so an empty facet object must produce no facet output at all —
// no hint line, and no member in the JSON tail.
func TestSearchEmptyNouveauFacetsProduceNoOutput(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/movies/_design/app/_nouveau/by_body", 200, `{
		"total_hits":1,"bookmark":"W3s","counts":{},"ranges":{},
		"hits":[{"id":"m1","order":[{"value":1.25,"@type":"float"}],"fields":{}}]}`)
	s := connected(t, srv)
	res, err := invoke(t, Search(), s, "/movies/_design/app/_nouveau/by_body", "*:*")
	if err != nil {
		t.Fatal(err)
	}
	rows := res.(Rows)
	if strings.Contains(rows.Hint, "counts:") || strings.Contains(rows.Hint, "ranges:") {
		t.Errorf("empty facet objects reached the hint: %q", rows.Hint)
	}
	if len(rows.Extra) != 0 {
		t.Errorf("empty facet objects reached the JSON tail: %v", rows.Extra)
	}
}

func TestSearchRejectsANonSearchPath(t *testing.T) {
	srv := couchtest.New(t)
	s := connected(t, srv)
	_, err := invoke(t, Search(), s, "/movies", "title:a*")
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Fatalf("err is %T (%v), want a *UsageError so the exit code is 2", err, err)
	}
	if !strings.Contains(err.Error(), "_search") || !strings.Contains(err.Error(), "_nouveau") {
		t.Errorf("the message does not say what a search path looks like: %v", err)
	}
}

// The client sends q= verbatim, so an empty query would ask the server a
// question it cannot answer. It is the caller's mistake, hence exit 2.
func TestSearchRejectsAnEmptyQuery(t *testing.T) {
	srv := couchtest.New(t)
	s := connected(t, srv)
	_, err := invoke(t, Search(), s, "/movies/_design/app/_search/by_title", "")
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Fatalf("err is %T (%v), want a *UsageError", err, err)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("an empty query reached the server: %v", srv.Requests())
	}
}

func TestSearchRejectsBadRangesJSON(t *testing.T) {
	srv := couchtest.New(t)
	s := connected(t, srv)
	_, err := invoke(t, Search(), s, "/movies/_design/app/_search/by_title", "*:*", "--ranges", "{nope")
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Fatalf("err is %T (%v), want a *UsageError", err, err)
	}
	if !strings.Contains(err.Error(), "--ranges") {
		t.Errorf("the message does not name the flag: %v", err)
	}
}

func TestSearchDrilldownSplitsOnTheFirstColon(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/movies/_design/app/_search/by_title", 200, `{"total_rows":0,"rows":[]}`)
	s := connected(t, srv)
	if _, err := invoke(t, Search(), s, "/movies/_design/app/_search/by_title", "*:*",
		"--drilldown", "genre:sci:fi", "--drilldown", "rating:PG"); err != nil {
		t.Fatal(err)
	}
	q, err := url.ParseQuery(srv.Requests()[0].RawQuery)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{`["genre","sci:fi"]`, `["rating","PG"]`}
	if !reflect.DeepEqual(q["drilldown"], want) {
		t.Errorf("drilldown = %v, want %v", q["drilldown"], want)
	}
}

func TestSearchClouseauUnavailableSentence(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/movies/_design/app/_search/by_title", http.StatusServiceUnavailable,
		`{"error":"service unavailable","reason":"Search is not available"}`)
	s := connected(t, srv)
	_, err := invoke(t, Search(), s, "/movies/_design/app/_search/by_title", "*:*")
	if err == nil {
		t.Fatal("a 503 succeeded")
	}
	want := "This server has no search service running; Clouseau must be installed and started for _search indexes."
	if got := err.Error(); !strings.Contains(got, want) {
		t.Errorf("got  %q\nwant it to contain %q", got, want)
	}
}

func TestSearchNouveauNotEnabledSentence(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/movies/_design/app", 200, ddocWithIndexes)
	srv.JSON("GET", "/movies/_design/app/_nouveau/by_body", http.StatusNotFound, `{"error":"not_found","reason":"missing"}`)
	s := connected(t, srv)
	_, err := invoke(t, Search(), s, "/movies/_design/app/_nouveau/by_body", "*:*")
	if err == nil {
		t.Fatal("a 404 succeeded")
	}
	want := `Nouveau is not enabled on this server, or "app/by_body" is not a nouveau index.`
	if got := err.Error(); !strings.Contains(got, want) {
		t.Errorf("got  %q\nwant it to contain %q", got, want)
	}
}

// The same 404, but the design document is genuinely missing: the existing
// design-document sentence has to win, because that is the actionable one.
func TestSearchMissingDesignDocKeepsItsOwnSentence(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/movies/_design/app", http.StatusNotFound, `{"error":"not_found","reason":"missing"}`)
	srv.JSON("GET", "/movies/_design/app/_nouveau/by_body", http.StatusNotFound, `{"error":"not_found","reason":"missing"}`)
	s := connected(t, srv)
	_, err := invoke(t, Search(), s, "/movies/_design/app/_nouveau/by_body", "*:*")
	if err == nil {
		t.Fatal("a missing design document succeeded")
	}
	if strings.Contains(err.Error(), "Nouveau is not enabled") {
		t.Errorf("a missing design document was blamed on Nouveau: %v", err)
	}
	ce, ok := couch.AsError(err)
	if !ok || !strings.Contains(ce.Target, "design document") {
		t.Errorf("error = %#v; want the design document's own 404", err)
	}
}

func TestSearchBadQuerySentence(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/movies/_design/app/_search/by_title", http.StatusBadRequest,
		`{"error":"bad_request","reason":"Cannot parse 'title:[' : Encountered \"<EOF>\""}`)
	s := connected(t, srv)
	_, err := invoke(t, Search(), s, "/movies/_design/app/_search/by_title", "title:[")
	if err == nil {
		t.Fatal("a 400 succeeded")
	}
	if !strings.Contains(err.Error(), "The search query was rejected: ") {
		t.Errorf("got %q", err)
	}
}

func TestInfoOnASearchPath(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/movies/_design/app/_nouveau_info/by_body", 200,
		`{"name":"_design/app/by_body","search_index":{"update_seq":26,"purge_seq":0,"num_docs":12,"disk_size":193,"signature":"abc"}}`)
	s := connected(t, srv)
	res, err := invoke(t, Info(), s, "/movies/_design/app/_nouveau/by_body")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range res.(Rows).Items {
		got[r.Cells[0]] = r.Cells[1]
	}
	if got["name"] != "_design/app/by_body" || got["backend"] != "nouveau" {
		t.Errorf("rows = %v", got)
	}
	if got["num_docs"] != "12" || got["signature"] != "abc" {
		t.Errorf("rows = %v", got)
	}
}

// A 503 from _search_info is the same missing Clouseau the query path reports,
// so info borrows the sentence rather than printing "service unavailable".
func TestInfoOnASearchPathUsesTheSearchSentence(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/movies/_design/app/_search_info/by_title", http.StatusServiceUnavailable,
		`{"error":"service unavailable","reason":"Search is not available"}`)
	s := connected(t, srv)
	_, err := invoke(t, Info(), s, "/movies/_design/app/_search/by_title")
	if err == nil || !strings.Contains(err.Error(), "Clouseau must be installed") {
		t.Errorf("info on a Clouseau index without Clouseau = %v", err)
	}
}

// ls and cat both used to send an operator standing on a search index to the
// other one. Neither reads an index; search does.
func TestLsAndCatOnASearchIndexPointAtSearch(t *testing.T) {
	srv := couchtest.New(t)
	s := connected(t, srv)
	for _, c := range []Command{Ls(), Cat()} {
		_, err := invoke(t, c, s, "/movies/_design/app/_search/by_title")
		if err == nil {
			t.Fatalf("%s on a search index succeeded", c.Name)
		}
		if !strings.Contains(err.Error(), `"search"`) {
			t.Errorf("%s says %q; it should point at search", c.Name, err)
		}
		if strings.Contains(err.Error(), `"cat"`) || strings.Contains(err.Error(), `"ls"`) {
			t.Errorf("%s still points at the other reader: %q", c.Name, err)
		}
	}
}

// --verbose appends the raw status and reason to every other failure, and the
// sentences cdb composes itself are no exception: the sentence explains what
// the operator can do, the bracket says what the server actually answered.
// Composing it is internal/render's job, so what is pinned here is that the
// error carries the three fields the suffix is built from.
func TestSearchSentenceCarriesTheServersOwnWords(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/movies/_design/app/_search/by_title", http.StatusServiceUnavailable,
		`{"error":"service unavailable","reason":"Search is not available"}`)
	s := connected(t, srv)
	_, err := invoke(t, Search(), s, "/movies/_design/app/_search/by_title", "*:*")
	var se *SentenceError
	if !errors.As(err, &se) {
		t.Fatalf("err is %T (%v), want a *SentenceError", err, err)
	}
	if se.Status != http.StatusServiceUnavailable || se.Name != "service unavailable" || se.Reason != "Search is not available" {
		t.Errorf("the server's own words were dropped: %#v", se)
	}
	// The whole point of not wrapping with %w: the *couch.Error must not be
	// reachable, or internal/render would print its sentence instead of this
	// one.
	if _, ok := couch.AsError(err); ok {
		t.Error("the couch.Error is still reachable through errors.As")
	}
}

// CouchDB 3.0 does not answer a Clouseau-less _search with 503 and a sentence;
// it lets the gen_server call to a process that is not there fail, and the
// operator gets a 500 carrying an Erlang badarg tuple. The name of the missing
// process is in it, which is the one thing in that blob worth reading.
func TestSearchClouseauMissingOnAnOlderServerSentence(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/movies/_design/app/_search/by_title", http.StatusInternalServerError,
		`{"error":"{badarg,[{erlang,monitor,[process,{main,'clouseau@127.0.0.1'}],[]}]}",
		  "reason":"{gen_server,call,[ioq,{request,{main,'clouseau@127.0.0.1'}}]}"}`)
	s := connected(t, srv)
	_, err := invoke(t, Search(), s, "/movies/_design/app/_search/by_title", "*:*")
	if err == nil {
		t.Fatal("a 500 succeeded")
	}
	want := "This server has no search service running; Clouseau must be installed and started for _search indexes."
	if got := err.Error(); !strings.Contains(got, want) {
		t.Errorf("got  %q\nwant it to contain %q", got, want)
	}
}

// A 500 that says nothing about Clouseau is a server fault, not a missing
// backend, and must keep the server's own error.
func TestSearchUnrelated500IsNotBlamedOnClouseau(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/movies/_design/app/_search/by_title", http.StatusInternalServerError,
		`{"error":"badmatch","reason":"something else entirely"}`)
	s := connected(t, srv)
	_, err := invoke(t, Search(), s, "/movies/_design/app/_search/by_title", "*:*")
	if err == nil {
		t.Fatal("a 500 succeeded")
	}
	if strings.Contains(err.Error(), "Clouseau") {
		t.Errorf("an unrelated 500 was blamed on Clouseau: %v", err)
	}
}

// Before 3.4 there is no _nouveau endpoint at all, so the 404 says "Document
// is missing attachment" — the router's answer, not the index's. The sentence
// is the same one, with the version clause that says why.
func TestSearchNouveauBefore34SaysWhichVersionItNeeds(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/", 200, `{"couchdb":"Welcome","version":"3.0.1","vendor":{"name":"The Apache Software Foundation"}}`)
	srv.JSON("GET", "/movies/_design/app", 200, ddocWithIndexes)
	srv.JSON("GET", "/movies/_design/app/_nouveau/by_body", http.StatusNotFound,
		`{"error":"not_found","reason":"Document is missing attachment"}`)
	s := connected(t, srv)
	_, err := invoke(t, Search(), s, "/movies/_design/app/_nouveau/by_body", "*:*")
	if err == nil {
		t.Fatal("a 404 succeeded")
	}
	want := `Nouveau is not enabled on this server, or "app/by_body" is not a nouveau index. Nouveau needs CouchDB 3.4 or later; this server is 3.0.1.`
	if got := err.Error(); !strings.Contains(got, want) {
		t.Errorf("got  %q\nwant it to contain %q", got, want)
	}
}

// On a server new enough to have the endpoint the clause would be wrong, so it
// is not printed.
func TestSearchNouveauOn35SaysNothingAboutTheVersion(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/", 200, `{"couchdb":"Welcome","version":"3.5.2","vendor":{"name":"The Apache Software Foundation"}}`)
	srv.JSON("GET", "/movies/_design/app", 200, ddocWithIndexes)
	srv.JSON("GET", "/movies/_design/app/_nouveau/by_body", http.StatusNotFound, `{"error":"not_found","reason":"missing"}`)
	s := connected(t, srv)
	_, err := invoke(t, Search(), s, "/movies/_design/app/_nouveau/by_body", "*:*")
	if err == nil {
		t.Fatal("a 404 succeeded")
	}
	if strings.Contains(err.Error(), "3.4 or later") {
		t.Errorf("a 3.5 server was told it needs 3.4: %v", err)
	}
}
