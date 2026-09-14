package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// piped runs a command with a pipeline made of vals, and drains whatever
// result it returns into rows of cells.
func piped(t *testing.T, c Command, s *session.Session, argv []string, vals ...string) ([][]string, error) {
	t.Helper()
	src := make(chan json.RawMessage, len(vals))
	for _, v := range vals {
		src <- json.RawMessage(v)
	}
	close(src)
	fs := NewRegistry().NewFlagSet(c)
	if err := fs.Parse(argv); err != nil {
		return nil, err
	}
	if err := c.CheckArgsErr(fs.Args()); err != nil {
		return nil, err
	}
	res, err := c.Run(context.Background(), s, Invocation{
		Args: fs.Args(), Flags: fs, Stdin: s.Stdin(), Stdout: s.Stdout, Stderr: s.Stderr,
		Shell: true, Pipe: NewPipe(src),
	})
	if err != nil {
		return nil, err
	}
	st, ok := res.(Stream)
	if !ok {
		t.Fatalf("result is %T, want Stream", res)
	}
	var out [][]string
	for {
		row, ok, err := st.Next()
		if err != nil {
			return out, err
		}
		if !ok {
			return out, nil
		}
		out = append(out, row.Cells)
	}
}

func TestPutPipelineWritesAndReportsEveryDocument(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/movies/_all_docs", 200, `{"rows":[
		{"key":"a","error":"not_found"},
		{"key":"b","error":"not_found"}]}`)
	srv.JSON("POST", "/movies/_bulk_docs", 201, `[
		{"ok":true,"id":"a","rev":"1-x"},
		{"id":"b","error":"conflict","reason":"Document update conflict."}]`)
	s := connected(t, srv)
	s.Prefs.Yes = true
	rows, err := piped(t, Put(), s, []string{"/movies"}, `{"_id":"a"}`, `{"_id":"b","_rev":"1-old"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %v", rows)
	}
	if rows[0][0] != "a" || rows[0][1] != "1-x" || rows[0][2] != "ok" {
		t.Errorf("row 0 = %v", rows[0])
	}
	if rows[1][2] != "conflict" {
		t.Errorf("row 1 = %v; a per-document conflict is a row, not a line failure", rows[1])
	}
}

func TestPutPipelineTakesTheDatabaseFromTheCurrentDirectory(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/movies/_all_docs", 200, `{"rows":[{"key":"a","error":"not_found"}]}`)
	srv.JSON("POST", "/movies/_bulk_docs", 201, `[{"ok":true,"id":"a","rev":"1-x"}]`)
	s := connected(t, srv)
	s.SetPath("/movies")
	rows, err := piped(t, Put(), s, nil, `{"_id":"a"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0][0] != "a" {
		t.Errorf("rows = %v", rows)
	}
}

func TestPutPipelineDropsAnIncomingRevision(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/movies/_all_docs", 200, `{"rows":[{"key":"a","error":"not_found"}]}`)
	srv.JSON("POST", "/movies/_bulk_docs", 201, `[{"ok":true,"id":"a","rev":"1-x"}]`)
	s := connected(t, srv)
	s.Prefs.Yes = true
	rows, err := piped(t, Put(), s, []string{"/movies"}, `{"_id":"a","_rev":"9-stale"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0][2] != "ok" {
		t.Fatalf("rows = %v", rows)
	}
	body := srv.Last("POST", "/movies/_bulk_docs").Body
	if strings.Contains(string(body), "9-stale") {
		t.Errorf("request body = %s; an incoming _rev must not reach the target", body)
	}
}

func TestPutPipelineOverwritesTheTargetsCurrentRevision(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/movies/_all_docs", 200, `{"rows":[{"id":"a","key":"a","value":{"rev":"5-existing"}}]}`)
	srv.JSON("POST", "/movies/_bulk_docs", 201, `[{"ok":true,"id":"a","rev":"6-new"}]`)
	s := connected(t, srv)
	rows, err := piped(t, Put(), s, []string{"/movies"}, `{"_id":"a","name":"alpha"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0][1] != "6-new" || rows[0][2] != "ok" {
		t.Fatalf("rows = %v", rows)
	}
	body := srv.Last("POST", "/movies/_bulk_docs").Body
	if !strings.Contains(string(body), "5-existing") {
		t.Errorf("request body = %s; put must write over the target's current revision", body)
	}
}

func TestPutPipelineStillReportsAGenuineConflict(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/movies/_all_docs", 200, `{"rows":[{"id":"a","key":"a","value":{"rev":"5-existing"}}]}`)
	srv.JSON("POST", "/movies/_bulk_docs", 201, `[{"id":"a","error":"conflict","reason":"Document update conflict."}]`)
	s := connected(t, srv)
	rows, err := piped(t, Put(), s, []string{"/movies"}, `{"_id":"a"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0][2] != "conflict" {
		t.Fatalf("rows = %v; a concurrent write racing put is still a conflict row", rows)
	}
}

func TestPutPipelineAtTheRootIsAUsageError(t *testing.T) {
	srv := couchtest.New(t)
	s := connected(t, srv)
	_, err := piped(t, Put(), s, nil, `{"_id":"a"}`)
	if err == nil || err.Error() != "put needs a database path when the current directory is /" {
		t.Fatalf("error = %v", err)
	}
	var ue *UsageError
	if !errorsAs(err, &ue) {
		t.Errorf("error is %T, want *UsageError so the exit code is 2", err)
	}
}

func TestPutPipelineRejectsAValueThatIsNotAnObject(t *testing.T) {
	srv := couchtest.New(t)
	s := connected(t, srv)
	s.SetPath("/movies")
	_, err := piped(t, Put(), s, nil, `{"_id":"a"}`, `7`)
	if err == nil || !strings.Contains(err.Error(), "value 2 is not a JSON object") {
		t.Fatalf("error = %v", err)
	}
}

// errorsAs is errors.As, named here so the tests above read as prose.
func errorsAs(err error, target any) bool { return errors.As(err, target) }

func TestRmPipelineLooksUpRevisionsAndDeletes(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/movies/_all_docs", 200, `{"rows":[
		{"id":"a","key":"a","value":{"rev":"1-x"}},
		{"key":"gone","error":"not_found"}]}`)
	srv.JSON("POST", "/movies/_bulk_docs", 201, `[{"ok":true,"id":"a","rev":"2-y"}]`)
	s := connected(t, srv)
	s.Prefs.Yes = true
	rows, err := piped(t, Rm(), s, []string{"/movies"}, `"a"`, `"gone"`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %v", rows)
	}
	if rows[0][0] != "a" || rows[0][1] != "2-y" || rows[0][2] != "ok" {
		t.Errorf("row 0 = %v", rows[0])
	}
	if rows[1][0] != "gone" || rows[1][2] != "not_found" {
		t.Errorf("row 1 = %v; a missing document is a row, not a failure", rows[1])
	}
	body := string(srv.Last("POST", "/movies/_bulk_docs").Body)
	if !strings.Contains(body, `"_deleted":true`) || !strings.Contains(body, `"1-x"`) {
		t.Errorf("delete body = %s", body)
	}
}

// A reference that already carries its revision needs no lookup: piping "cat"
// or "ls --json" into "rm" must not double the requests.
func TestRmPipelineSkipsTheLookupWhenTheRevisionIsThere(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/movies/_bulk_docs", 201, `[{"ok":true,"id":"a","rev":"2-y"}]`)
	s := connected(t, srv)
	s.Prefs.Yes = true
	rows, err := piped(t, Rm(), s, []string{"/movies"}, `{"_id":"a","_rev":"1-x"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0][2] != "ok" {
		t.Fatalf("rows = %v", rows)
	}
	if srv.Last("POST", "/movies/_all_docs") != nil {
		t.Error("rm looked a revision up that the reference already carried")
	}
}

func TestRmPipelineConfirmsOnce(t *testing.T) {
	srv := couchtest.New(t)
	s := connected(t, srv)
	s.Prefs.Interactive = false
	s.Prefs.Yes = false
	_, err := piped(t, Rm(), s, []string{"/movies"}, `"a"`)
	var ue *UsageError
	if !errorsAs(err, &ue) {
		t.Fatalf("error = %v (%T); a script without --yes must be refused, not left waiting", err, err)
	}
	if srv.Last("POST", "/movies/_bulk_docs") != nil {
		t.Error("rm deleted without a confirmation")
	}
}

// The prompt is built once, before anything is read, so one --yes covers the
// whole pipeline however many documents it names.
func TestRmPipelineConfirmsBeforeReadingThePipe(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/movies/_all_docs", 200, `{"rows":[{"id":"a","key":"a","value":{"rev":"1-x"}},{"id":"b","key":"b","value":{"rev":"1-y"}}]}`)
	srv.JSON("POST", "/movies/_bulk_docs", 201, `[{"ok":true,"id":"a","rev":"2-x"},{"ok":true,"id":"b","rev":"2-y"}]`)
	s := connected(t, srv)
	s.Prefs.Yes = true
	rows, err := piped(t, Rm(), s, []string{"/movies"}, `"a"`, `"b"`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Errorf("rows = %v", rows)
	}
}

func TestCatPipelineStreamsTheDocuments(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/movies/_all_docs", 200, `{"rows":[
		{"id":"a","key":"a","value":{"rev":"1-x"},"doc":{"_id":"a","_rev":"1-x","title":"Amelie"}}]}`)
	s := connected(t, srv)
	rows, err := piped(t, Cat(), s, []string{"/movies"}, `"a"`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || !strings.Contains(rows[0][0], "Amelie") {
		t.Fatalf("rows = %v", rows)
	}
}

func TestCatPipelineNamesAMissingDocument(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/movies/_all_docs", 200, `{"rows":[{"key":"gone","error":"not_found"}]}`)
	s := connected(t, srv)
	_, err := piped(t, Cat(), s, []string{"/movies"}, `"gone"`)
	if err == nil || err.Error() != `"gone" is not in movies` {
		t.Fatalf("error = %v", err)
	}
}

func TestCatPipelineRejectsAValueThatNamesNoDocument(t *testing.T) {
	srv := couchtest.New(t)
	s := connected(t, srv)
	_, err := piped(t, Cat(), s, []string{"/movies"}, `"a"`, `3`)
	if err == nil || !strings.Contains(err.Error(), "value 2 is not a document reference") {
		t.Fatalf("error = %v", err)
	}
}

// nextBatch caps a batch at pipeBatch (100): 101 references piped into rm
// must split into two keyed _all_docs lookups and two _bulk_docs deletes,
// not one of each.
func TestRmPipelineBatchesAtTheBoundary(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("POST", "/movies/_all_docs", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Keys []string `json:"keys"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		rows := make([]map[string]any, len(body.Keys))
		for i, k := range body.Keys {
			rows[i] = map[string]any{"id": k, "key": k, "value": map[string]string{"rev": "1-x"}}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_ = json.NewEncoder(w).Encode(map[string]any{"rows": rows})
	})
	srv.On("POST", "/movies/_bulk_docs", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Docs []struct {
				ID string `json:"_id"`
			} `json:"docs"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		rows := make([]map[string]any, len(body.Docs))
		for i, d := range body.Docs {
			rows[i] = map[string]any{"ok": true, "id": d.ID, "rev": "2-x"}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(rows)
	})
	s := connected(t, srv)
	s.Prefs.Yes = true

	vals := make([]string, 101)
	for i := range vals {
		vals[i] = fmt.Sprintf(`"d%d"`, i)
	}
	rows, err := piped(t, Rm(), s, []string{"/movies"}, vals...)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 101 {
		t.Fatalf("rows = %d, want 101", len(rows))
	}
	var allDocs, bulkDocs int
	for _, r := range srv.Requests() {
		if r.Method != "POST" {
			continue
		}
		switch r.Path {
		case "/movies/_all_docs":
			allDocs++
		case "/movies/_bulk_docs":
			bulkDocs++
		}
	}
	if allDocs != 2 {
		t.Errorf("_all_docs requests = %d, want 2", allDocs)
	}
	if bulkDocs != 2 {
		t.Errorf("_bulk_docs requests = %d, want 2", bulkDocs)
	}
}

// A view can emit several keys per document, so one batch can carry the same
// id twice. The second tombstone conflicts, and the row must say so: the
// server's results are matched by position, not by id.
func TestRmPipelineReportsEachReferenceByPosition(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/movies/_bulk_docs", 201, `[
		{"ok":true,"id":"a","rev":"2-dead"},
		{"id":"a","error":"conflict","reason":"Document update conflict."},
		{"id":"a","error":"conflict","reason":"Document update conflict."}]`)
	s := connected(t, srv)
	s.Prefs.Yes = true
	row := `{"id":"a","value":{"rev":"1-x"}}`
	rows, err := piped(t, Rm(), s, []string{"/movies"}, row, row, row)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %v", rows)
	}
	if rows[0][2] != "ok" {
		t.Errorf("row 0 = %v, want ok", rows[0])
	}
	for i := 1; i < 3; i++ {
		if rows[i][2] != "conflict" {
			t.Errorf("row %d = %v; a repeated id must carry the server's own status", i, rows[i])
		}
	}
}

// An "ls" or "tail" row carries {"id","rev"}, so the revision is already known
// and the _all_docs round trip is waste.
func TestRmPipelineTakesAFlatRevFromTheRow(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/movies/_bulk_docs", 201, `[{"ok":true,"id":"a","rev":"2-dead"}]`)
	s := connected(t, srv)
	s.Prefs.Yes = true
	rows, err := piped(t, Rm(), s, []string{"/movies"}, `{"id":"a","rev":"1-x"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0][2] != "ok" {
		t.Fatalf("rows = %v", rows)
	}
	if req := srv.Last("POST", "/movies/_all_docs"); req != nil {
		t.Errorf("a keyed read was sent: %s; the row already carried its rev", req.Body)
	}
	if body := srv.Last("POST", "/movies/_bulk_docs").Body; !strings.Contains(string(body), "1-x") {
		t.Errorf("request body = %s; the row's own rev must be the one deleted", body)
	}
}
