package couch

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
)

// writeLines answers a continuous-feed request with each line followed by a
// flush, which is what makes the reader see them one at a time.
func writeLines(w http.ResponseWriter, lines ...string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	f, _ := w.(http.Flusher)
	for _, l := range lines {
		_, _ = io.WriteString(w, l+"\n")
		if f != nil {
			f.Flush()
		}
	}
}

func TestChangesFollowSkipsHeartbeatsAndLastSeq(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("GET", "/mydb/_changes", func(w http.ResponseWriter, _ *http.Request) {
		writeLines(w,
			"",
			`{"seq":"1-x","id":"a","changes":[{"rev":"1-aa"}]}`,
			"",
			`{"seq":"2-y","id":"b","deleted":true,"changes":[{"rev":"2-bb"}],"doc":{"_id":"b","_deleted":true}}`,
			`{"last_seq":"2-y","pending":0}`,
		)
	})
	c := newTestClient(t, srv)

	var got []ChangeRow
	err := c.ChangesFollow(context.Background(), "mydb", ChangesOptions{
		Since:       "now",
		IncludeDocs: true,
		HeartbeatMS: 30000,
	}, func(r ChangeRow) error {
		got = append(got, r)
		return nil
	})
	if err != nil {
		t.Fatalf("ChangesFollow = %v, want nil at the end of the body", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d changes, want 2: %+v", len(got), got)
	}
	if got[0].ID != "a" || got[0].Seq != "1-x" || got[0].Revs[0] != "1-aa" {
		t.Errorf("change 0 = %+v", got[0])
	}
	if !got[1].Deleted || string(got[1].Doc) != `{"_id":"b","_deleted":true}` {
		t.Errorf("change 1 = %+v", got[1])
	}

	req := srv.Last("GET", "/mydb/_changes")
	for _, tc := range []struct{ key, want string }{
		{"feed", "continuous"},
		{"since", "now"},
		{"style", "main_only"},
		{"include_docs", "true"},
		{"heartbeat", "30000"},
	} {
		if got := req.Query(tc.key); got != tc.want {
			t.Errorf("%s = %q, want %q", tc.key, got, tc.want)
		}
	}
	if got := req.Query("limit"); got != "" {
		t.Errorf("limit = %q, want empty on the continuous feed", got)
	}
}

func TestChangesFollowReturnsAtACleanEndOfBody(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("GET", "/mydb/_changes", func(w http.ResponseWriter, _ *http.Request) {
		writeLines(w, `{"seq":"1-x","id":"a","changes":[{"rev":"1-aa"}]}`)
		// Returning from the handler after a flush ends the chunked body
		// *cleanly*: the client sees a normal EOF. That is a feed the server
		// closed on purpose, and it is reported as nil so the caller reconnects.
	})
	c := newTestClient(t, srv)

	n := 0
	err := c.ChangesFollow(context.Background(), "mydb", ChangesOptions{}, func(ChangeRow) error {
		n++
		return nil
	})
	if err != nil {
		t.Fatalf("ChangesFollow = %v, want nil for a body that simply ended", err)
	}
	if n != 1 {
		t.Fatalf("delivered %d changes, want 1", n)
	}
}

// Spec section 13 asks for "a body cut short mid-stream", which is not the
// same as the clean end above: panicking with http.ErrAbortHandler kills the
// connection without a terminating chunk, so the client sees a torn body
// rather than an EOF.
func TestChangesFollowReportsATornBody(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("GET", "/mydb/_changes", func(w http.ResponseWriter, _ *http.Request) {
		writeLines(w, `{"seq":"1-x","id":"a","changes":[{"rev":"1-aa"}]}`)
		panic(http.ErrAbortHandler) // kills the connection mid-stream
	})
	c := newTestClient(t, srv)

	err := c.ChangesFollow(context.Background(), "mydb", ChangesOptions{}, func(ChangeRow) error { return nil })
	if err == nil {
		t.Fatal("a torn body returned nil; the caller cannot tell it from a clean end")
	}
	ce, ok := AsError(err)
	if !ok || ce.Status != StatusUnreachable {
		t.Fatalf("err = %v, want a transport *Error", err)
	}
}

// A single change larger than the scan buffer cannot be read, and cannot be
// read on a second attempt either: it has to be a terminal error, or a follow
// would reconnect from the same sequence and hit the same line forever.
func TestChangesFollowRefusesAnOverlongLine(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("GET", "/mydb/_changes", func(w http.ResponseWriter, _ *http.Request) {
		big := strings.Repeat("x", changesScanBuffer+1)
		writeLines(w, `{"seq":"1-x","id":"a","changes":[{"rev":"1-aa"}],"doc":{"_id":"a","big":"`+big+`"}}`)
	})
	c := newTestClient(t, srv)

	err := c.ChangesFollow(context.Background(), "mydb", ChangesOptions{IncludeDocs: true}, func(ChangeRow) error { return nil })
	ce, ok := AsError(err)
	if !ok || ce.Status != http.StatusRequestEntityTooLarge {
		t.Fatalf("err = %v, want a 413 *Error", err)
	}
	if !strings.Contains(ce.Reason, "4 MiB") || !strings.Contains(ce.Reason, "--include-docs") {
		t.Errorf("reason = %q, want it to name the limit and the flag", ce.Reason)
	}
}

func TestChangesFollowReturnsTheCallbackError(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("GET", "/mydb/_changes", func(w http.ResponseWriter, _ *http.Request) {
		writeLines(w,
			`{"seq":"1-x","id":"a","changes":[{"rev":"1-aa"}]}`,
			`{"seq":"2-y","id":"b","changes":[{"rev":"1-bb"}]}`,
		)
	})
	c := newTestClient(t, srv)

	stop := errors.New("stop")
	err := c.ChangesFollow(context.Background(), "mydb", ChangesOptions{}, func(ChangeRow) error {
		return stop
	})
	if !errors.Is(err, stop) {
		t.Fatalf("ChangesFollow = %v, want the callback's error", err)
	}
}

func TestChangesFollowMapsAFourZeroFour(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/gone/_changes", 404, `{"error":"not_found","reason":"Database does not exist."}`)
	c := newTestClient(t, srv)

	err := c.ChangesFollow(context.Background(), "gone", ChangesOptions{}, func(ChangeRow) error { return nil })
	ce, ok := AsError(err)
	if !ok || ce.Status != 404 || ce.Name != "not_found" {
		t.Fatalf("ChangesFollow = %v, want a mapped 404", err)
	}
}

func TestChangesFollowReturnsContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	srv := couchtest.New(t)
	srv.On("GET", "/mydb/_changes", func(w http.ResponseWriter, r *http.Request) {
		writeLines(w, `{"seq":"1-x","id":"a","changes":[{"rev":"1-aa"}]}`)
		<-r.Context().Done()
	})
	c := newTestClient(t, srv)

	err := c.ChangesFollow(ctx, "mydb", ChangesOptions{}, func(ChangeRow) error {
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ChangesFollow = %v, want context.Canceled", err)
	}
}
