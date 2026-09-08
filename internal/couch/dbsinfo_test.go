package couch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
)

// CouchDB's chttpd caps POST /_dbs_info at max_db_number_for_dbs_info_req,
// which defaults to 100, and answers 400 "too_many_keys" beyond it (verified
// against CouchDB 3.5.2). "ls /" is the first command the README tells an
// operator to type, so it has to work on a server with a database per tenant.
func TestDatabasesInfoChunksAtTheServerKeyLimit(t *testing.T) {
	const total = 250
	names := make([]string, total)
	for i := range names {
		names[i] = fmt.Sprintf("db%03d", i)
	}

	srv := couchtest.New(t)
	srv.On("POST", "/_dbs_info", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Keys []string `json:"keys"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decoding the _dbs_info body: %v", err)
		}
		if len(req.Keys) > 100 {
			// The real server answers 400; do the same so a regression fails
			// with the error an operator would actually see.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"bad_request","reason":"too_many_keys"}`))
			return
		}
		rows := make([]map[string]any, 0, len(req.Keys))
		for _, k := range req.Keys {
			rows = append(rows, map[string]any{
				"key":  k,
				"info": map[string]any{"db_name": k, "doc_count": 1},
			})
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(couchtest.Encode(rows)))
	})
	c := newTestClient(t, srv)

	got, err := c.DatabasesInfo(context.Background(), names)
	if err != nil {
		t.Fatalf("DatabasesInfo over %d names: %v", total, err)
	}
	if len(got) != total {
		t.Fatalf("got %d infos, want %d", len(got), total)
	}
	for i, info := range got {
		if info.Name != names[i] {
			t.Fatalf("info[%d].Name = %q, want %q — the chunks were not merged in order", i, info.Name, names[i])
		}
		if info.DocCount != 1 {
			t.Errorf("info[%d].DocCount = %d, want the value from the response", i, info.DocCount)
		}
	}

	var posts int
	for _, req := range srv.Requests() {
		if req.Method != "POST" || req.Path != "/_dbs_info" {
			continue
		}
		posts++
		var body struct {
			Keys []string `json:"keys"`
		}
		if err := json.Unmarshal(req.Body, &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Keys) == 0 || len(body.Keys) > 100 {
			t.Errorf("POST %d carried %d keys, want between 1 and 100", posts, len(body.Keys))
		}
	}
	if posts != 3 {
		t.Errorf("made %d POSTs to /_dbs_info, want 3 for %d names", posts, total)
	}
}

// One request is still one request: chunking must not add a round trip for the
// ordinary small server.
func TestDatabasesInfoSendsOneRequestUnderTheLimit(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/_dbs_info", 200, `[{"key":"mydb","info":{"db_name":"mydb"}}]`)
	c := newTestClient(t, srv)
	if _, err := c.DatabasesInfo(context.Background(), []string{"mydb"}); err != nil {
		t.Fatal(err)
	}
	var posts int
	for _, req := range srv.Requests() {
		if req.Method == "POST" && req.Path == "/_dbs_info" {
			posts++
		}
	}
	if posts != 1 {
		t.Errorf("made %d POSTs for one name, want 1", posts)
	}
}
