package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// integrationSession returns a session connected to CDB_TEST_URL with the
// server's admin credentials, or skips. The credentials go through the
// environment Deps.LookupEnv serves, because that is how Env.Apply selects
// session auth and Env.Secret hands the password over — the same path a real
// "CDB_USER=… CDB_PASSWORD=… cdb ls /" takes.
func integrationSession(t *testing.T) *session.Session {
	t.Helper()
	base := os.Getenv("CDB_TEST_URL")
	if base == "" {
		t.Skip("set CDB_TEST_URL (e.g. http://localhost:15984/) to run integration tests")
	}
	withDeps(t, nil, map[string]string{
		"CDB_USER":     envOrDefault("CDB_TEST_USER", "admin"),
		"CDB_PASSWORD": envOrDefault("CDB_TEST_PASSWORD", "password"),
	})
	s := newSession(t)
	// CouchDB dials a replication endpoint itself, so the tombstone round-trip
	// needs the address the server knows itself by whenever that differs from
	// the one the test connects to — the documented dev container publishes
	// 5984 as 15984. This is Task 7's --replication-url, reached through the
	// session preference openProfile applies. Unset in CI, where the service
	// container reaches itself at the same localhost:5984, and an empty value
	// changes nothing.
	s.Prefs.ReplicationURL = os.Getenv("CDB_TEST_REPLICATION_URL")
	if err := Open(context.Background(), s, base); err != nil {
		t.Fatalf("connect to %s = %v", base, err)
	}
	return s
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// integrationDB creates a throwaway database and removes it when the test
// ends, whether it passed or failed.
func integrationDB(t *testing.T, s *session.Session, name string) string {
	t.Helper()
	ctx := context.Background()
	_ = s.Client.DestroyDatabase(ctx, name)
	if err := s.Client.CreateDatabase(ctx, name, false, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Client.DestroyDatabase(context.Background(), name) })
	return name
}

// nextResult is one Stream.Next call, so a test can put a deadline on a
// blocking read.
type nextResult struct {
	row  Row
	more bool
	err  error
}

// A follow only earns its keep if a change written after it started arrives
// without anything else happening first. Only a real server proves that: the
// unit tests stub the feed, and a stub cannot show that CouchDB flushes a
// change as it commits it.
func TestIntegrationTailFollowSeesConcurrentWrites(t *testing.T) {
	s := integrationSession(t)
	db := integrationDB(t, s, "cdb_it_tail")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start from the sequence the database is at now rather than from "now",
	// which the server resolves only when it handles the request: the writes
	// below would otherwise race the feed being opened, and a missed change
	// would look like a bug in tail.
	info, err := s.Client.DatabaseInfo(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	res, err := invokeContext(ctx, Tail(), s, "/"+db, "--follow", "--since", info.UpdateSeq)
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

	results := make(chan nextResult, 8)
	go func() {
		for {
			row, more, err := st.Next()
			results <- nextResult{row, more, err}
			if !more || err != nil {
				return
			}
		}
	}()

	const docs = 3
	go func() {
		for i := 0; i < docs; i++ {
			id := fmt.Sprintf("doc%d", i)
			body := json.RawMessage(fmt.Sprintf(`{"_id":%q,"n":%d}`, id, i))
			if _, err := s.Client.PutDocument(ctx, db, id, body, ""); err != nil {
				return // the read side fails on its deadline, with a clearer message
			}
		}
	}()

	seen := map[string]bool{}
	deadline := time.After(30 * time.Second)
	for len(seen) < docs {
		select {
		case r := <-results:
			if r.err != nil || !r.more {
				t.Fatalf("stream ended early: more=%v err=%v", r.more, r.err)
			}
			// Columns are seq, id, rev, deleted (Task 4's tailColumns).
			seen[r.row.Cells[1]] = true
		case <-deadline:
			t.Fatalf("only %d of %d changes arrived within 30s: %v", len(seen), docs, seen)
		}
	}
	for i := 0; i < docs; i++ {
		if id := fmt.Sprintf("doc%d", i); !seen[id] {
			t.Errorf("change for %q never arrived", id)
		}
	}

	// Ctrl-C: the stream ends with context.Canceled, which is what exits 130.
	cancel()
	for {
		select {
		case r := <-results:
			if r.err != nil {
				if !errors.Is(r.err, context.Canceled) {
					t.Errorf("after cancel, Next err = %v, want context.Canceled", r.err)
				}
				return
			}
			if !r.more {
				t.Error("the stream ended without reporting context.Canceled")
				return
			}
		case <-time.After(30 * time.Second):
			t.Fatal("the follow did not stop within 30s of cancelling it")
		}
	}
}
