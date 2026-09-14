package command

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
)

const threeTasks = `[
	{"node":"nonode@nohost","pid":"<0.1.0>","type":"replication","continuous":true,
	 "doc_id":"movies-job","source":"http://alice:s3cret@remote:5984/movies/",
	 "target":"http://localhost:5984/movies-backup/","started_on":1757836800,
	 "updated_on":1757836860},
	{"node":"nonode@nohost","pid":"<0.2.0>","type":"database_compaction",
	 "database":"movies","progress":84,"started_on":1757836800,"updated_on":1757836870},
	{"node":"nonode@nohost","pid":"<0.3.0>","type":"indexer","database":"other",
	 "design_document":"_design/by_year","progress":0,"started_on":1757836800,
	 "updated_on":1757836880}]`

func TestTasksRendersOneRowPerTask(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_active_tasks", 200, threeTasks)
	s := connected(t, srv)
	res, err := invoke(t, Tasks(), s)
	if err != nil {
		t.Fatal(err)
	}
	rows, ok := res.(Rows)
	if !ok {
		t.Fatalf("result is %T, want Rows", res)
	}
	if len(rows.Items) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows.Items))
	}
	titles := []string{"type", "progress", "target", "started", "updated", "node"}
	for i, want := range titles {
		if rows.Columns[i].Title != want {
			t.Fatalf("column %d = %q, want %q", i, rows.Columns[i].Title, want)
		}
	}

	rep := rows.Items[0].Cells
	if rep[0] != "replication" || rep[1] != "" {
		t.Errorf("replication row = %v; a task with no progress must leave the cell blank", rep)
	}
	if rep[2] != "http://remote:5984/movies/ → http://localhost:5984/movies-backup/" {
		t.Errorf("replication target = %q", rep[2])
	}
	if strings.Contains(strings.Join(rep, " "), "s3cret") {
		t.Fatal("the password reached a rendered cell")
	}
	if rows.Items[1].Cells[1] != "84%" {
		t.Errorf("compaction progress = %q", rows.Items[1].Cells[1])
	}
	if rows.Items[1].Cells[2] != "movies" {
		t.Errorf("compaction target = %q", rows.Items[1].Cells[2])
	}
	if rows.Items[2].Cells[1] != "0%" {
		t.Errorf("indexer progress = %q; zero is a progress, not an absence", rows.Items[2].Cells[1])
	}
	if rows.Items[2].Cells[2] != "other/_design/by_year" {
		t.Errorf("indexer target = %q", rows.Items[2].Cells[2])
	}

	started := time.Unix(1757836800, 0).Format("15:04:05")
	if rep[3] != started {
		t.Errorf("started = %q, want the local time %q", rep[3], started)
	}
	if rep[5] != "nonode@nohost" {
		t.Errorf("node = %q", rep[5])
	}

	var payload map[string]any
	if err := json.Unmarshal(rows.Items[0].JSON, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["pid"] != "<0.1.0>" {
		t.Errorf("Row.JSON is not the task object: %s", rows.Items[0].JSON)
	}
	if payload["source"] != "http://remote:5984/movies/" {
		t.Errorf("Row.JSON source = %v", payload["source"])
	}
}

func TestTasksWithNoTasksSaysSo(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_active_tasks", 200, `[]`)
	s := connected(t, srv)
	res, err := invoke(t, Tasks(), s)
	if err != nil {
		t.Fatal(err)
	}
	msg, ok := res.(Message)
	if !ok || msg.Text != "No active tasks." {
		t.Errorf("result = %#v", res)
	}
}

func TestTasksFiltersByTypeAndDatabase(t *testing.T) {
	// --db /movies matches two of the three fixtures: the database_compaction
	// whose "database" is movies, and the replication whose source endpoint
	// ends in /movies/. --db /other matches only the indexer.
	for _, tc := range []struct {
		argv []string
		want int
	}{
		{[]string{"--type", "indexer"}, 1},
		{[]string{"--type", "database_compaction"}, 1},
		{[]string{"--db", "/movies"}, 2},
		{[]string{"--db", "/other"}, 1},
	} {
		srv := couchtest.New(t)
		srv.JSON("GET", "/_active_tasks", 200, threeTasks)
		s := connected(t, srv)
		res, err := invoke(t, Tasks(), s, tc.argv...)
		if err != nil {
			t.Fatalf("%v: %v", tc.argv, err)
		}
		rows, ok := res.(Rows)
		if !ok {
			t.Fatalf("%v: result is %T", tc.argv, res)
		}
		if len(rows.Items) != tc.want {
			t.Errorf("%v: got %d rows, want %d", tc.argv, len(rows.Items), tc.want)
		}
	}
}

func TestTasksRejectsAnUnknownType(t *testing.T) {
	srv := couchtest.New(t)
	s := connected(t, srv)
	_, err := invoke(t, Tasks(), s, "--type", "bogus")
	var ue *UsageError
	if !errors.As(err, &ue) || !strings.Contains(ue.Reason, "search_indexer") {
		t.Fatalf("error = %v", err)
	}
}

func TestTasksWatchReprintsOnlyWhenTheSetChanges(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSONSeq("GET", "/_active_tasks", 200,
		threeTasks, // first poll: three rows
		threeTasks, // unchanged: nothing new
		`[]`,       // the tasks ended: one "no active tasks" line
	)
	s := connected(t, srv)
	s.Prefs.Interactive = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	res, err := invokeContext(ctx, Tasks(), s, "--watch", "--interval", "10ms")
	if err != nil {
		t.Fatal(err)
	}
	st, ok := res.(Stream)
	if !ok {
		t.Fatalf("result is %T, want Stream", res)
	}
	if !st.Live {
		t.Fatal("the watch stream is not Live; the rows would be held back until it ended")
	}

	var cells []string
	for i := 0; i < 4; i++ {
		row, ok, err := st.Next()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Fatal("the stream ended while the watch was still running")
		}
		cells = append(cells, strings.Join(row.Cells, "|"))
	}
	if len(cells) != 4 {
		t.Fatalf("got %d rows", len(cells))
	}
	for i := 0; i < 3; i++ {
		if cells[i] == "" {
			t.Fatalf("row %d is empty", i)
		}
	}
	if !strings.Contains(cells[3], "no active tasks") {
		t.Errorf("the fourth row is %q; the emptied list must say so once", cells[3])
	}

	cancel()
	if _, _, err := st.Next(); !errors.Is(err, context.Canceled) {
		t.Errorf("after Ctrl-C, Next returned %v, want context.Canceled", err)
	}
}
