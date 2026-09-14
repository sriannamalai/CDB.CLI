package command

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
)

func TestCompactConfirmsAndPosts(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/movies/_compact", 202, `{"ok":true}`)
	s := connected(t, srv)
	s.Prefs.Interactive = true
	s.SetStdin(strings.NewReader("y\n"))
	res, err := invoke(t, Compact(), s, "/movies")
	if err != nil {
		t.Fatal(err)
	}
	if out := s.Stdout.(*bytes.Buffer).String(); !strings.HasPrefix(out, "Compact movies? [y/N]") {
		t.Errorf("prompt = %q", out)
	}
	if msg := res.(Message).Text; msg != "Compaction of movies started." {
		t.Errorf("message = %q", msg)
	}
}

func TestCompactDeclinedPostsNothing(t *testing.T) {
	srv := couchtest.New(t)
	s := connected(t, srv)
	s.Prefs.Interactive = true
	s.SetStdin(strings.NewReader("n\n"))
	_, err := invoke(t, Compact(), s, "/movies")
	if !errors.Is(err, ErrDeclined) {
		t.Fatalf("error = %v", err)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("requests = %d, want none", len(srv.Requests()))
	}
}

func TestCompactDdocAndCleanup(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/movies/_compact/by_year", 202, `{"ok":true}`)
	srv.JSON("POST", "/movies/_view_cleanup", 202, `{"ok":true}`)
	s := connected(t, srv)
	s.Prefs.Yes = true
	res, err := invoke(t, Compact(), s, "/movies", "--ddoc", "by_year", "--cleanup")
	if err != nil {
		t.Fatal(err)
	}
	if srv.Last("POST", "/movies/_compact") != nil {
		t.Error("--ddoc compacted the whole database as well as the design document")
	}
	if srv.Last("POST", "/movies/_compact/by_year") == nil || srv.Last("POST", "/movies/_view_cleanup") == nil {
		t.Fatal("one of the two calls was not made")
	}
	want := "Compaction of movies/_design/by_year started. Orphaned view indexes are being cleaned up as well."
	if msg := res.(Message).Text; msg != want {
		t.Errorf("message = %q, want %q", msg, want)
	}
}

func TestCompactNeedsADatabasePath(t *testing.T) {
	srv := couchtest.New(t)
	s := connected(t, srv)
	s.Prefs.Yes = true
	_, err := invoke(t, Compact(), s, "/movies/doc1")
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Fatalf("error = %v", err)
	}
}

// The wiring: --watch posts the compaction and hands back a Live stream whose
// first row is available at once. It reads exactly one row, because every row
// after the first costs a real compactPollInterval — the sequence itself is
// driven at millisecond speed by the next test.
func TestCompactWatchPostsAndReturnsALiveStream(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/movies/_compact", 202, `{"ok":true}`)
	srv.JSON("GET", "/_active_tasks", 200,
		`[{"node":"n1","type":"database_compaction","database":"movies","progress":10,"started_on":1757836800,"updated_on":1757836801}]`)
	s := connected(t, srv)
	s.Prefs.Yes = true
	s.Prefs.Interactive = true
	res, err := invoke(t, Compact(), s, "/movies", "--watch")
	if err != nil {
		t.Fatal(err)
	}
	if srv.Last("POST", "/movies/_compact") == nil {
		t.Fatal("--watch did not start the compaction")
	}
	st, ok := res.(Stream)
	if !ok {
		t.Fatalf("result is %T, want Stream", res)
	}
	if !st.Live {
		t.Fatal("the watch stream is not Live")
	}
	row, ok, err := st.Next()
	if err != nil || !ok {
		t.Fatalf("first row: %v, %v", ok, err)
	}
	if !strings.Contains(strings.Join(row.Cells, "|"), "10%") {
		t.Errorf("first row = %v", row.Cells)
	}
}

func TestCompactWatchFollowsTheTaskAndSaysWhenItEnds(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSONSeq("GET", "/_active_tasks", 200,
		`[{"node":"n1","type":"database_compaction","database":"movies","progress":10,"started_on":1757836800,"updated_on":1757836801}]`,
		`[{"node":"n1","type":"database_compaction","database":"movies","progress":90,"started_on":1757836800,"updated_on":1757836802}]`,
		`[]`,
	)
	s := connected(t, srv)
	// Driven directly with a millisecond poll: through the command this
	// sequence would cost two real two-second polls.
	st := watchCompactionWith(context.Background(), s, "movies", "movies", time.Millisecond, 10*time.Second, time.Now)
	var lines []string
	for {
		row, ok, err := st.Next()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		lines = append(lines, strings.Join(row.Cells, "|"))
	}
	if len(lines) != 3 {
		t.Fatalf("rows = %v, want two progress rows and one closing line", lines)
	}
	if !strings.Contains(lines[0], "10%") || !strings.Contains(lines[1], "90%") {
		t.Errorf("progress rows = %v", lines[:2])
	}
	if !strings.Contains(lines[2], "Compaction of movies finished.") {
		t.Errorf("closing line = %q", lines[2])
	}
}

// A database small enough to compact between two polls never produces a task.
// The ten-second window is driven directly here rather than waited out: the
// poll and the window are both one millisecond, so the first poll sees nothing,
// the deadline has already passed, and the watch closes with the finished line.
func TestCompactWatchOnADatabaseThatFinishedInstantly(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_active_tasks", 200, `[]`)
	s := connected(t, srv)
	st := watchCompactionWith(context.Background(), s, "movies", "movies", time.Millisecond, time.Millisecond, time.Now)
	row, ok, err := st.Next()
	if err != nil || !ok {
		t.Fatalf("Next = %v, %v, %v", row, ok, err)
	}
	if !strings.Contains(strings.Join(row.Cells, " "), "Compaction of movies finished.") {
		t.Errorf("row = %v", row.Cells)
	}
	if _, ok, err := st.Next(); ok || err != nil {
		t.Errorf("the stream did not end: ok=%v err=%v", ok, err)
	}
}
