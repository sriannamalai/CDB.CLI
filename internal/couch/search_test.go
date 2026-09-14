package couch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
	"github.com/sriannamalai/CDB.CLI/internal/path"
)

func clouseauTarget() path.Target {
	return path.Target{
		Kind: path.KindSearch, Path: "/movies/_design/app/_search/by_title",
		Database: "movies", DocID: "_design/app", Index: "by_title", Backend: path.BackendClouseau,
	}
}

func nouveauTarget() path.Target {
	return path.Target{
		Kind: path.KindSearch, Path: "/movies/_design/app/_nouveau/by_title",
		Database: "movies", DocID: "_design/app", Index: "by_title", Backend: path.BackendNouveau,
	}
}

func TestSearchNormalisesTheClouseauShape(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/movies/_design/app/_search/by_title", 200, `{
		"total_rows": 3,
		"bookmark": "g1AAAA",
		"rows": [
			{"id":"m1","order":[1.25,"m1"],"fields":{"title":"Arrival"}},
			{"id":"m2","order":[0.75,"m2"],"fields":{"title":"Annihilation"}}
		],
		"counts": {"genre": {"scifi": 2}},
		"ranges": {"year": {"old": 1}}}`)
	c, err := New(Config{URL: srv.URL(), Auth: AuthNone})
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Search(context.Background(), clouseauTarget(), SearchOptions{Query: "title:a*", Limit: 25})
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != 3 || got.Bookmark != "g1AAAA" || len(got.Rows) != 2 {
		t.Fatalf("result = %#v", got)
	}
	if got.Rows[0].ID != "m1" || !reflect.DeepEqual(got.Rows[0].Order, []any{1.25, "m1"}) {
		t.Errorf("row 0 = %#v", got.Rows[0])
	}
	if got.Rows[0].Fields["title"] != "Arrival" {
		t.Errorf("fields = %#v", got.Rows[0].Fields)
	}
	if got.Counts == nil || got.Ranges == nil {
		t.Errorf("counts/ranges dropped: %#v %#v", got.Counts, got.Ranges)
	}
	// The query travels as "q" for both backends, and limit is sent as asked
	// for: search pages by bookmark, so there is no "one extra row" trick.
	r := srv.Requests()[0]
	if r.Query("q") != "title:a*" || r.Query("limit") != "25" {
		t.Errorf("query string = %q", r.RawQuery)
	}
}

func TestSearchNormalisesTheNouveauShape(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/movies/_design/app/_nouveau/by_title", 200, `{
		"total_hits": 2,
		"total_hits_relation": "EQUAL_TO",
		"bookmark": "W3sidiI6",
		"hits": [
			{"id":"m1","order":[{"value":1.25,"@type":"float"},{"value":"m1","@type":"string"}],
			 "fields":{"title":"Arrival"},"doc":{"_id":"m1","title":"Arrival"}}
		],
		"counts": {"genre": {"scifi": 1}},
		"ranges": {"year": {"old": 0}},
		"update_latency": 12}`)
	c, err := New(Config{URL: srv.URL(), Auth: AuthNone})
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Search(context.Background(), nouveauTarget(), SearchOptions{Query: "title:a*", IncludeDocs: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != 2 || got.Bookmark != "W3sidiI6" || len(got.Rows) != 1 {
		t.Fatalf("result = %#v", got)
	}
	// The wrapper objects are unwrapped, so a caller sees the same []any it
	// would get from Clouseau and the score column has one implementation.
	if !reflect.DeepEqual(got.Rows[0].Order, []any{1.25, "m1"}) {
		t.Errorf("order = %#v, want the unwrapped values", got.Rows[0].Order)
	}
	if got.Rows[0].Doc["title"] != "Arrival" {
		t.Errorf("doc = %#v", got.Rows[0].Doc)
	}
	if srv.Requests()[0].Query("include_docs") != "true" {
		t.Errorf("include_docs not sent: %q", srv.Requests()[0].RawQuery)
	}
}

func TestSearchSendsTheFacetAndPagingParameters(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/movies/_design/app/_search/by_title", 200, `{"total_rows":0,"rows":[]}`)
	c, err := New(Config{URL: srv.URL(), Auth: AuthNone})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Search(context.Background(), clouseauTarget(), SearchOptions{
		Query:     "*:*",
		Bookmark:  "g1AAAA",
		Sort:      `["-year<number>"]`,
		Counts:    []string{"genre", "decade"},
		Ranges:    json.RawMessage(`{"year":{"old":"[0 TO 1990]"}}`),
		Drilldown: [][]string{{"genre", "scifi"}, {"rating", "PG"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := srv.Requests()[0]
	if r.Query("bookmark") != "g1AAAA" {
		t.Errorf("bookmark = %q", r.Query("bookmark"))
	}
	if r.Query("sort") != `["-year<number>"]` {
		t.Errorf("sort = %q; it is passed through verbatim", r.Query("sort"))
	}
	if r.Query("counts") != `["genre","decade"]` {
		t.Errorf("counts = %q, want a JSON array", r.Query("counts"))
	}
	if r.Query("ranges") != `{"year":{"old":"[0 TO 1990]"}}` {
		t.Errorf("ranges = %q, want the JSON as given", r.Query("ranges"))
	}
	q, err := url.ParseQuery(r.RawQuery)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{`["genre","scifi"]`, `["rating","PG"]`}
	if !reflect.DeepEqual(q["drilldown"], want) {
		t.Errorf("drilldown = %v, want %v: each one is its own parameter", q["drilldown"], want)
	}
}

// A partitioned search goes to the partitioned base, the way a partitioned
// view does.
func TestSearchUsesThePartitionedBase(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/movies/_partition/p1/_design/app/_search/by_title", 200, `{"total_rows":0,"rows":[]}`)
	c, err := New(Config{URL: srv.URL(), Auth: AuthNone})
	if err != nil {
		t.Fatal(err)
	}
	target := clouseauTarget()
	target.Partition = "p1"
	if _, err := c.Search(context.Background(), target, SearchOptions{Query: "*:*"}); err != nil {
		t.Fatal(err)
	}
}

func TestSearchInfoReadsBothBackends(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/movies/_design/app/_search_info/by_title", 200,
		`{"name":"_design/app/by_title","search_index":{"committed_seq":26,"disk_size":193,"doc_count":12,"doc_del_count":0,"pending_seq":26,"signature":"abc"}}`)
	srv.JSON("GET", "/movies/_design/app/_nouveau_info/by_title", 200,
		`{"name":"_design/app/by_title","search_index":{"update_seq":26,"purge_seq":0,"num_docs":12,"disk_size":193,"signature":"abc"}}`)
	c, err := New(Config{URL: srv.URL(), Auth: AuthNone})
	if err != nil {
		t.Fatal(err)
	}
	cl, err := c.SearchInfo(context.Background(), clouseauTarget())
	if err != nil {
		t.Fatal(err)
	}
	if cl.Name != "_design/app/by_title" || cl.Index["doc_count"] != float64(12) {
		t.Errorf("clouseau info = %#v", cl)
	}
	nv, err := c.SearchInfo(context.Background(), nouveauTarget())
	if err != nil {
		t.Fatal(err)
	}
	if nv.Index["num_docs"] != float64(12) || nv.Index["update_seq"] != float64(26) {
		t.Errorf("nouveau info = %#v", nv)
	}
}

// The 503 a server with no Clouseau answers with keeps its status, so the
// command layer can say which of spec §5.3's sentences applies.
func TestSearchPassesTheServiceUnavailableStatusThrough(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/movies/_design/app/_search/by_title", http.StatusServiceUnavailable,
		`{"error":"service unavailable","reason":"Search is not available"}`)
	c, err := New(Config{URL: srv.URL(), Auth: AuthNone})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Search(context.Background(), clouseauTarget(), SearchOptions{Query: "*:*"})
	ce, ok := AsError(err)
	if !ok {
		t.Fatalf("err is %T (%v), want a *couch.Error", err, err)
	}
	if ce.Status != http.StatusServiceUnavailable || ce.Reason != "Search is not available" {
		t.Errorf("error = %#v", ce)
	}
	if ce.Target != `search index "app/by_title" in "movies"` {
		t.Errorf("target = %q", ce.Target)
	}
}
