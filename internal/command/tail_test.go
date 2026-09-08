package command

import (
	"errors"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
)

// drain reads a whole stream into its rows.
func drain(t *testing.T, st Stream) []Row {
	t.Helper()
	var out []Row
	for {
		row, ok, err := st.Next()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			return out
		}
		out = append(out, row)
	}
}

func tailServer(t *testing.T) *couchtest.Server {
	t.Helper()
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb/_changes", 200, `{"results":[
		{"seq":"1-x","id":"a","changes":[{"rev":"1-aa"}],"doc":{"_id":"a","name":"alice"}},
		{"seq":"2-y","id":"b","deleted":true,"changes":[{"rev":"2-bb"}]}],
		"last_seq":"2-y","pending":0}`)
	return srv
}

func TestTailReadsTheNormalFeed(t *testing.T) {
	srv := tailServer(t)
	s := connected(t, srv)

	res, err := invoke(t, Tail(), s, "/mydb", "--since", "0", "--limit", "2")
	if err != nil {
		t.Fatal(err)
	}
	st, ok := res.(Stream)
	if !ok {
		t.Fatalf("tail returned %#v, want a Stream", res)
	}
	if st.Live {
		t.Error("the normal feed produced a Live stream; only --follow does")
	}
	rows := drain(t, st)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	// The cell is the sequence's numeric prefix, marked as truncated; the
	// JSON keeps it whole.
	if got := rows[0].Cells; got[0] != "1-…" || got[1] != "a" || got[2] != "1-aa" || got[3] != "false" {
		t.Errorf("row 0 cells = %v", got)
	}
	if got := string(rows[0].JSON); !strings.Contains(got, `"seq":"1-x"`) {
		t.Errorf("row 0 JSON = %s, want the full sequence", got)
	}
	if got := rows[1].Cells[3]; got != "true" {
		t.Errorf("row 1 deleted cell = %q, want true", got)
	}
	if got := string(rows[1].JSON); got != `{"deleted":true,"id":"b","rev":"2-bb","seq":"2-y"}` {
		t.Errorf("row 1 JSON = %s", got)
	}
	// The hint is offered because the page came back exactly full, and it
	// carries the full sequence plus the one note about the shortened column.
	want := "more changes: tail /mydb --since \"2-y\"\n" + tailSeqHint
	if st.Hint != want {
		t.Errorf("hint = %q, want %q", st.Hint, want)
	}

	req := srv.Last("GET", "/mydb/_changes")
	for _, tc := range []struct{ key, want string }{
		{"feed", "normal"},
		{"style", "main_only"},
		{"since", "0"},
		{"limit", "2"},
	} {
		if got := req.Query(tc.key); got != tc.want {
			t.Errorf("%s = %q, want %q", tc.key, got, tc.want)
		}
	}
}

func TestTailWithIncludeDocsAddsTheDocColumn(t *testing.T) {
	srv := tailServer(t)
	s := connected(t, srv)

	res, err := invoke(t, Tail(), s, "/mydb", "--include-docs")
	if err != nil {
		t.Fatal(err)
	}
	st := res.(Stream)
	if n := len(st.Columns); n != 5 || st.Columns[4].Title != "doc" {
		t.Fatalf("columns = %+v", st.Columns)
	}
	rows := drain(t, st)
	if got := rows[0].Cells[4]; got != `{"_id":"a","name":"alice"}` {
		t.Errorf("doc cell = %q", got)
	}
	if !strings.Contains(string(rows[0].JSON), `"doc":{"_id":"a","name":"alice"}`) {
		t.Errorf("row JSON = %s", rows[0].JSON)
	}
	if got := srv.Last("GET", "/mydb/_changes").Query("include_docs"); got != "true" {
		t.Errorf("include_docs = %q, want true", got)
	}
}

func TestTailNoHintOnAShortPage(t *testing.T) {
	srv := tailServer(t)
	s := connected(t, srv)

	res, err := invoke(t, Tail(), s, "/mydb", "--limit", "25")
	if err != nil {
		t.Fatal(err)
	}
	if st := res.(Stream); st.Hint != "" {
		t.Errorf("hint = %q, want none for a page that was not full", st.Hint)
	}
}

func TestTailUsageErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		argv []string
		want string
	}{
		{"document path", []string{"/mydb/doc1"}, "/mydb/doc1 is a document; tail takes a database path."},
		{"partition path", []string{"/mydb/_partition/p1"}, "CouchDB has no partition-scoped changes feed; tail /mydb reads the whole database."},
		{"bad filter", []string{"/mydb", "--filter", "byname"}, `--filter takes a design document and a filter name, as "app/by_type".`},
		{"heartbeat without follow", []string{"/mydb", "--heartbeat", "1000"}, "--heartbeat only applies to --follow; the normal feed has no idle period to keep alive."},
		{"limit with follow", []string{"/mydb", "--follow", "--limit", "5"}, "--limit does not apply to --follow, which reads until you stop it."},
		{"negative limit", []string{"/mydb", "--limit", "-1"}, "--limit cannot be negative."},
		{"negative heartbeat", []string{"/mydb", "--follow", "--heartbeat", "-1"}, "--heartbeat cannot be negative."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := tailServer(t)
			s := connected(t, srv)
			_, err := invoke(t, Tail(), s, tc.argv...)
			var ue *UsageError
			if err == nil || !asUsageError(err, &ue) {
				t.Fatalf("err = %v, want a UsageError", err)
			}
			if !strings.Contains(ue.Error(), tc.want) {
				t.Errorf("message = %q, want it to contain %q", ue.Error(), tc.want)
			}
		})
	}
}

func TestTailShortensTheSequenceInTheTable(t *testing.T) {
	const long = "25-g1AAAACLeJzLYWBgYMpgTmHgzcvPy09JdcjLz8gvLskBCScyJNX___8_K4M5kTEXKMBukGhiYZpkia4Yh_Y8FiDJ0ACk_qOYYmKRmpKWYoKuJwsASGwqvA"
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb/_changes", 200, `{"results":[
		{"seq":"`+long+`","id":"a","changes":[{"rev":"1-aa"}]},
		{"seq":"7","id":"b","changes":[{"rev":"1-bb"}]}],
		"last_seq":"`+long+`","pending":0}`)
	s := connected(t, srv)

	res, err := invoke(t, Tail(), s, "/mydb", "--limit", "2")
	if err != nil {
		t.Fatal(err)
	}
	st := res.(Stream)
	rows := drain(t, st)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	// The marker matters: CouchDB accepts a bare integer in --since and
	// answers it by replaying the whole feed, so a cell that looked like a
	// usable sequence would be a trap.
	if got := rows[0].Cells[0]; got != "25-…" {
		t.Errorf("seq cell = %q, want the marked prefix %q", got, "25-…")
	}
	// A sequence with no "-" has no prefix to take, so it is shown whole and
	// carries no marker, because nothing was dropped.
	if got := rows[1].Cells[0]; got != "7" {
		t.Errorf("seq cell = %q, want %q", got, "7")
	}
	if got := string(rows[0].JSON); !strings.Contains(got, `"seq":"`+long+`"`) {
		t.Errorf("row JSON = %s, want the full sequence", got)
	}
	// --since is fed from the hint, so the hint must carry the whole thing.
	want := "more changes: tail /mydb --since \"" + long + "\"\n" + tailSeqHint
	if st.Hint != want {
		t.Errorf("hint = %q, want %q", st.Hint, want)
	}
}

func TestTailPassesTheFilterThrough(t *testing.T) {
	srv := tailServer(t)
	s := connected(t, srv)

	if _, err := invoke(t, Tail(), s, "/mydb", "--filter", "app/by_type"); err != nil {
		t.Fatal(err)
	}
	if got := srv.Last("GET", "/mydb/_changes").Query("filter"); got != "app/by_type" {
		t.Errorf("filter = %q, want %q", got, "app/by_type")
	}
}

func TestTailIsRegistered(t *testing.T) {
	c, ok := Default().Lookup("tail")
	if !ok {
		t.Fatal("tail is not in the default registry")
	}
	if !c.NeedsClient {
		t.Error("tail does not declare NeedsClient")
	}
}

func asUsageError(err error, target **UsageError) bool {
	return errors.As(err, target)
}
