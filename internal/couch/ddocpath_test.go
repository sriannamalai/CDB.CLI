package couch

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
)

// TestDesignDocPathsEscapeTheDocumentID pins that every design-document URL is
// built the way GetDocument builds it. A "#" in an unescaped id truncates the
// URL at the fragment and a "?" at the query string, so "cat" would find the
// document while "ls", "query" and view completion all 404 on it.
func TestDesignDocPathsEscapeTheDocumentID(t *testing.T) {
	for _, ddoc := range []string{"_design/a#b", "_design/a b", "_design/a?b"} {
		t.Run(ddoc, func(t *testing.T) {
			srv := couchtest.New(t)
			docPath := "/mydb/" + ddoc
			srv.JSON("GET", docPath, 200, `{"_id":"`+ddoc+`","_rev":"1-abc","views":{"v":{"map":"function(){}"}}}`)
			srv.JSON("GET", docPath+"/_view/v", 200,
				`{"total_rows":1,"offset":0,"rows":[{"id":"doc1","key":"k","value":1}]}`)
			c := newTestClient(t, srv)

			// cat: already escaped before this fix, and the behaviour every
			// other caller has to match.
			raw, _, err := c.GetDocument(context.Background(), "mydb", ddoc, GetOptions{})
			if err != nil {
				t.Fatalf("GetDocument: %v", err)
			}
			var doc struct {
				ID string `json:"_id"`
			}
			if err := json.Unmarshal(raw, &doc); err != nil || doc.ID != ddoc {
				t.Fatalf("GetDocument returned %s (err %v)", raw, err)
			}

			// ls of a design document.
			if _, err := c.DesignDoc(context.Background(), "mydb", ddoc); err != nil {
				t.Errorf("DesignDoc: %v", err)
			}

			// query of one of its views.
			page, err := c.Query(context.Background(), "mydb", ddoc, "v", ViewOptions{})
			if err != nil {
				t.Errorf("Query: %v", err)
			} else if len(page.Rows) != 1 || page.Rows[0].ID != "doc1" {
				t.Errorf("Query returned %+v", page.Rows)
			}

			for _, want := range []string{docPath, docPath + "/_view/v"} {
				if srv.Last("GET", want) == nil {
					t.Errorf("no request for %q; got %v", want, requestPaths(srv))
				}
			}
		})
	}
}

// TestQueryEscapesTheDesignDocInAPartitionedView covers the third place the
// design-document path is built: a partitioned view query.
func TestQueryEscapesTheDesignDocInAPartitionedView(t *testing.T) {
	srv := couchtest.New(t)
	const want = "/mydb/_partition/p 1/_design/a#b/_view/v"
	srv.JSON("GET", want, 200, `{"total_rows":0,"offset":0,"rows":[]}`)
	c := newTestClient(t, srv)
	if _, err := c.Query(context.Background(), "mydb", "_design/a#b", "v", ViewOptions{Partition: "p 1"}); err != nil {
		t.Fatalf("Query: %v", err)
	}
	if srv.Last("GET", want) == nil {
		t.Errorf("no request for %q; got %v", want, requestPaths(srv))
	}
}

func requestPaths(srv *couchtest.Server) []string {
	var paths []string
	for _, r := range srv.Requests() {
		paths = append(paths, r.Method+" "+r.Path)
	}
	return paths
}
