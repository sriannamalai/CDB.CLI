package command

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sriannamalai/CDB.CLI/internal/couch"
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
		{"filter without a name", []string{"/mydb", "--filter", "app/"}, `--filter takes a design document and a filter name, as "app/by_type".`},
		{"filter without a design document", []string{"/mydb", "--filter", "/by_type"}, `--filter takes a design document and a filter name, as "app/by_type".`},
		{"filter with too many halves", []string{"/mydb", "--filter", "app/by_type/extra"}, `--filter takes a design document and a filter name, as "app/by_type".`},
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

// followStartSeq is the update sequence the stub database reports. A follow
// resolves "now" to it once, before its first request, and every reconnect
// that has no change of its own to resume from goes back to it.
const followStartSeq = "0-resolved"

// followClock records the reconnect waits instead of sleeping through them, so
// a follow test drives the documented schedule in no time at all. It is handed
// to tailCommand per call rather than installed in a package variable: the
// producer goroutine reads the sleeper for as long as it runs, and a package
// variable one test restored while another test's producer was still going is
// a data race under -race.
type followClock struct {
	mu    sync.Mutex
	waits []time.Duration
}

// sleep records d and returns immediately, so the loop reconnects at once
// unless the operator has interrupted, which is what a real wait reports too.
func (c *followClock) sleep(ctx context.Context, d time.Duration) error {
	c.mu.Lock()
	c.waits = append(c.waits, d)
	c.mu.Unlock()
	return ctx.Err()
}

// recorded copies the waits under the lock, so a test may read them while the
// producer goroutine is still running.
func (c *followClock) recorded() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.waits...)
}

// followServer answers the continuous feed once per distinct "since" value.
// The first two connections each deliver one change and then end the body,
// which is what a dropped feed looks like; the third delivers a change and
// blocks until the client goes away. Every drop follows a delivered change, so
// a reader of all three rows has watched the backoff reset twice.
func followServer(t *testing.T) *couchtest.Server {
	t.Helper()
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb", 200, `{"db_name":"mydb","doc_count":2,"update_seq":"`+followStartSeq+`"}`)
	srv.On("GET", "/mydb/_changes", func(w http.ResponseWriter, r *http.Request) {
		write := writeFeed(w)
		switch r.URL.Query().Get("since") {
		case followStartSeq:
			write(`{"seq":"1-x","id":"a","changes":[{"rev":"1-aa"}]}`)
			// Returning ends the body: the feed has dropped.
		case "1-x":
			write(`{"seq":"2-y","id":"b","changes":[{"rev":"1-bb"}]}`)
		case "2-y":
			write(`{"seq":"3-z","id":"c","changes":[{"rev":"1-cc"}]}`)
			<-r.Context().Done()
		default:
			<-r.Context().Done()
		}
	})
	return srv
}

// writeFeed starts a continuous-feed response and returns a writer for one
// change line, flushed so the client sees it as it is written.
func writeFeed(w http.ResponseWriter) func(string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	f, _ := w.(http.Flusher)
	return func(line string) {
		_, _ = io.WriteString(w, line+"\n")
		if f != nil {
			f.Flush()
		}
	}
}

// drainStream reads a follow stream until Next reports an error or the end.
// Every follow test must call it before returning, because an error from Next
// is the only signal that the producer goroutine has exited; a test that
// returned first would leave it writing to the session while the next test ran.
//
// Call it once per stream, and never after Next has already returned an error:
// the producer reports its error exactly once, so a further read would block.
func drainStream(st Stream) {
	for {
		if _, more, err := st.Next(); err != nil || !more {
			return
		}
	}
}

// firstChangesRequest is the first request the follow made to the feed, which
// is the one carrying the sequence it resolved before opening anything.
func firstChangesRequest(t *testing.T, srv *couchtest.Server) *couchtest.Request {
	t.Helper()
	for _, req := range srv.Requests() {
		if req.Path == "/mydb/_changes" {
			return req
		}
	}
	t.Fatal("the follow never opened the changes feed")
	return nil
}

func TestTailFollowReconnectsFromTheLastSequence(t *testing.T) {
	clock := &followClock{}
	srv := followServer(t)
	s := connected(t, srv)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	res, err := invokeContext(ctx, tailCommand(clock.sleep), s, "/mydb", "--follow")
	if err != nil {
		t.Fatal(err)
	}
	st, ok := res.(Stream)
	if !ok {
		t.Fatalf("tail --follow returned %#v, want a Stream", res)
	}
	if !st.Live {
		t.Error("tail --follow did not mark the stream Live")
	}
	if st.Hint != "" {
		t.Errorf("hint = %q, want none on a follow", st.Hint)
	}

	// The cells carry the shortened sequence the table shows; the row JSON
	// keeps the whole one, which is what --since takes.
	for i, want := range []string{"1-x", "2-y"} {
		row, more, err := st.Next()
		if err != nil || !more {
			t.Fatalf("row %d: more=%v err=%v", i, more, err)
		}
		if row.Cells[0] != shortSeq(want) {
			t.Errorf("row %d seq cell = %q, want %q", i, row.Cells[0], shortSeq(want))
		}
		if got := string(row.JSON); !strings.Contains(got, `"seq":"`+want+`"`) {
			t.Errorf("row %d JSON = %s, want the full sequence %q", i, got, want)
		}
	}
	cancel()
	if _, _, err := st.Next(); !errors.Is(err, context.Canceled) {
		t.Fatalf("after cancel, Next err = %v, want context.Canceled", err)
	}

	// A request reconnected from the sequence of the last change delivered.
	found := false
	for _, req := range srv.Requests() {
		if req.Path == "/mydb/_changes" && req.Query("since") == "1-x" {
			found = true
			if req.Query("feed") != "continuous" {
				t.Errorf("reconnect feed = %q, want continuous", req.Query("feed"))
			}
			if req.Query("heartbeat") != "30000" {
				t.Errorf("reconnect heartbeat = %q, want 30000", req.Query("heartbeat"))
			}
		}
	}
	if !found {
		t.Error("no request reconnected from sequence 1-x")
	}
	if got := clock.recorded(); len(got) == 0 || got[0] != time.Second {
		t.Errorf("backoff waits = %v, want the first to be 1s", got)
	}
	if msg := s.Stderr.(*bytes.Buffer).String(); !strings.Contains(msg, `Lost the changes feed for "mydb"; reconnecting from 1-x in 1s.`) {
		t.Errorf("stderr = %q, want the reconnect notice", msg)
	}
}

func TestTailFollowResolvesNowBeforeItsFirstRequest(t *testing.T) {
	clock := &followClock{}
	srv := followServer(t)
	s := connected(t, srv)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	res, err := invokeContext(ctx, tailCommand(clock.sleep), s, "/mydb", "--follow")
	if err != nil {
		t.Fatal(err)
	}
	st := res.(Stream)
	if _, _, err := st.Next(); err != nil {
		t.Fatal(err)
	}
	cancel()
	drainStream(st)

	// "now" is resolved to the database's update sequence before anything is
	// opened, so every reconnect has a fixed sequence to fall back to.
	if got := firstChangesRequest(t, srv).Query("since"); got != followStartSeq {
		t.Errorf("first since = %q, want the resolved start %q", got, followStartSeq)
	}
}

func TestTailFollowKeepsAnExplicitSince(t *testing.T) {
	clock := &followClock{}
	srv := followServer(t)
	s := connected(t, srv)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// "1-x" is a sequence the stub answers, so reading one row is proof the
	// feed was opened before the test looks at what it was opened with.
	res, err := invokeContext(ctx, tailCommand(clock.sleep), s, "/mydb", "--follow", "--since", "1-x")
	if err != nil {
		t.Fatal(err)
	}
	st := res.(Stream)
	if _, _, err := st.Next(); err != nil {
		t.Fatal(err)
	}
	cancel()
	drainStream(st)

	if got := firstChangesRequest(t, srv).Query("since"); got != "1-x" {
		t.Errorf("first since = %q, want the sequence the operator gave", got)
	}
	// Nothing to resolve, so the database was never asked for its sequence.
	if req := srv.Last("GET", "/mydb"); req != nil {
		t.Error("a follow with --since still read the database info")
	}
}

func TestTailFollowResumesFromTheResolvedStartWhenNothingArrived(t *testing.T) {
	clock := &followClock{}
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb", 200, `{"db_name":"mydb","doc_count":0,"update_seq":"`+followStartSeq+`"}`)
	var mu sync.Mutex
	conns := 0
	srv.On("GET", "/mydb/_changes", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		conns++
		n := conns
		mu.Unlock()
		write := writeFeed(w)
		if n <= 2 {
			// Delivered nothing and dropped, twice. The reconnect has no
			// change of its own to resume from, and anything but the resolved
			// start would lose whatever was written in between.
			return
		}
		if r.URL.Query().Get("since") == followStartSeq {
			write(`{"seq":"9-z","id":"resumed","changes":[{"rev":"1-aa"}]}`)
		} else {
			write(`{"seq":"9-z","id":"restarted","changes":[{"rev":"1-aa"}]}`)
		}
		<-r.Context().Done()
	})
	s := connected(t, srv)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	res, err := invokeContext(ctx, tailCommand(clock.sleep), s, "/mydb", "--follow")
	if err != nil {
		t.Fatal(err)
	}
	st := res.(Stream)
	row, more, err := st.Next()
	if err != nil || !more {
		t.Fatalf("more=%v err=%v, want the change the reconnect read", more, err)
	}
	if row.Cells[1] != "resumed" {
		t.Errorf("row id = %q: the reconnect asked for %q, not the resolved start %q",
			row.Cells[1], "restarted", followStartSeq)
	}
	cancel()
	drainStream(st)

	if msg := s.Stderr.(*bytes.Buffer).String(); !strings.Contains(msg, `reconnecting from `+followStartSeq+` in 1s.`) {
		t.Errorf("stderr = %q, want the notice to name the resolved start", msg)
	}
	// Nothing came through either connection, so the wait doubled. Reading the
	// change is proof both waits are already recorded: the connection that
	// carried it was opened after them.
	want := []time.Duration{time.Second, 2 * time.Second}
	if got := clock.recorded(); !slices.Equal(got, want) {
		t.Errorf("backoff waits = %v, want %v: a feed delivering nothing backs off further", got, want)
	}
}

func TestTailFollowEndsOnAFourZeroFour(t *testing.T) {
	clock := &followClock{}
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb", 200, `{"db_name":"mydb","doc_count":0,"update_seq":"`+followStartSeq+`"}`)
	srv.JSON("GET", "/mydb/_changes", 404, `{"error":"not_found","reason":"Database does not exist."}`)
	s := connected(t, srv)

	res, err := invoke(t, tailCommand(clock.sleep), s, "/mydb", "--follow")
	if err != nil {
		t.Fatal(err)
	}
	_, more, err := res.(Stream).Next()
	if more {
		t.Fatal("a 404 produced a row")
	}
	ce, ok := couch.AsError(err)
	if !ok || ce.Status != 404 {
		t.Fatalf("Next err = %v, want a mapped 404", err)
	}
	if got := clock.recorded(); len(got) != 0 {
		t.Errorf("a 404 was retried after %v; only 5xx and transport failures reconnect", got)
	}
}

func TestTailFollowFailsWhenTheDatabaseIsGone(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb", 404, `{"error":"not_found","reason":"Database does not exist."}`)
	s := connected(t, srv)

	// The sequence is resolved before the feed opens, so a database that is
	// not there fails the command outright rather than starting a stream that
	// only reports it on the first read.
	_, err := invoke(t, tailCommand((&followClock{}).sleep), s, "/mydb", "--follow")
	ce, ok := couch.AsError(err)
	if !ok || ce.Status != 404 {
		t.Fatalf("err = %v, want a mapped 404", err)
	}
}

// Which endings are the feed dropping and which are the end of the command.
// The torn-body and over-long-line cases are the two ChangesFollow reports as
// *Error rather than nil (Task 2), and they must be classified oppositely: a
// torn body is worth reopening, an over-long change line never will be, and
// retrying it would print a reconnect notice forever.
func TestTailReconnectable(t *testing.T) {
	const target = `changes for "mydb"`
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"clean end of body", nil, true},
		{"torn body", couch.NewError(couch.StatusUnreachable, "network", "unexpected EOF", "read", target), true},
		{"server error", couch.NewError(http.StatusInternalServerError, "internal_server_error", "", "read", target), true},
		{"deleted database", couch.NewError(http.StatusNotFound, "not_found", "Database does not exist.", "read", target), false},
		{"rejected token", couch.NewError(http.StatusUnauthorized, "unauthorized", "", "read", target), false},
		{"over-long change line", couch.NewError(http.StatusRequestEntityTooLarge, "too_large", "a change was larger than the 4 MiB this feed can read; re-run without --include-docs", "read", target), false},
		{"cancelled", context.Canceled, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tailReconnectable(tc.err); got != tc.want {
				t.Errorf("tailReconnectable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestTailBackoffSchedule(t *testing.T) {
	want := []time.Duration{
		1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
		16 * time.Second, 30 * time.Second, 30 * time.Second,
	}
	for i, w := range want {
		if got := tailBackoff(i); got != w {
			t.Errorf("tailBackoff(%d) = %v, want %v", i, got, w)
		}
	}
}

// A feed that drops after delivering something is not a feed in trouble: the
// wait starts over at 1s each time rather than doubling towards 30s, or a
// database that drops its feed every few minutes would end up checked twice an
// hour. Reading the third change is proof the second wait already happened,
// because the connection carrying it is only opened after it.
func TestTailFollowResetsTheBackoffAfterEachChange(t *testing.T) {
	clock := &followClock{}
	srv := followServer(t)
	s := connected(t, srv)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	res, err := invokeContext(ctx, tailCommand(clock.sleep), s, "/mydb", "--follow")
	if err != nil {
		t.Fatal(err)
	}
	st := res.(Stream)
	for i, want := range []string{"1-x", "2-y", "3-z"} {
		row, more, err := st.Next()
		if err != nil || !more {
			t.Fatalf("row %d: more=%v err=%v", i, more, err)
		}
		if row.Cells[0] != shortSeq(want) {
			t.Errorf("row %d seq cell = %q, want %q", i, row.Cells[0], shortSeq(want))
		}
	}
	cancel()
	drainStream(st)

	want := []time.Duration{time.Second, time.Second}
	if got := clock.recorded(); !slices.Equal(got, want) {
		t.Errorf("backoff waits = %v, want %v: a change through the feed resets it", got, want)
	}
}

func TestTailFollowReportsTheSameErrorOnEveryRead(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb", 200, `{"db_name":"mydb","doc_count":0,"update_seq":"`+followStartSeq+`"}`)
	srv.JSON("GET", "/mydb/_changes", 404, `{"error":"not_found","reason":"Database does not exist."}`)
	s := connected(t, srv)

	res, err := invoke(t, tailCommand((&followClock{}).sleep), s, "/mydb", "--follow")
	if err != nil {
		t.Fatal(err)
	}
	st := res.(Stream)
	// The producer says why it stopped once. A second read must be given the
	// same answer rather than waiting on a channel nothing will write to
	// again: the shell's filter reads a stream to its end and then reads once
	// more.
	for i := 0; i < 2; i++ {
		_, more, err := st.Next()
		if more {
			t.Fatalf("read %d produced a row after the feed ended", i)
		}
		ce, ok := couch.AsError(err)
		if !ok || ce.Status != 404 {
			t.Fatalf("read %d err = %v, want a mapped 404", i, err)
		}
	}
}

func TestTailSleepHonoursCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err := tailSleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("tailSleep = %v, want context.Canceled", err)
	}
	// Ctrl-C during a 30s backoff must return to the prompt now, not when the
	// wait would have been over.
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("tailSleep took %v to notice a cancelled context", elapsed)
	}
}
