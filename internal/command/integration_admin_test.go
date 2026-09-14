package command

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// Every live test in this file opens its session with integrationSession,
// which lives at internal/command/integration_tail_test.go:20 and is shared by
// the whole package. Do not open a second one.
func TestTasksAgainstALiveServer(t *testing.T) {
	if os.Getenv("CDB_TEST_URL") == "" {
		t.Skip("CDB_TEST_URL is not set")
	}
	s := integrationSession(t)
	res, err := invoke(t, Tasks(), s)
	if err != nil {
		t.Fatal(err)
	}
	switch v := res.(type) {
	case Message:
		if v.Text != "No active tasks." {
			t.Errorf("message = %q", v.Text)
		}
	case Rows:
		if len(v.Columns) != 6 {
			t.Errorf("columns = %#v", v.Columns)
		}
	default:
		t.Fatalf("result is %T", res)
	}
}

func TestConfigRoundTripAgainstALiveServer(t *testing.T) {
	if os.Getenv("CDB_TEST_URL") == "" {
		t.Skip("CDB_TEST_URL is not set")
	}
	s := integrationSession(t)
	s.Prefs.Yes = true
	ctx := context.Background()

	// [log] level is the harmless key section 10 names: CouchDB re-reads it at
	// once, and putting it back costs nothing. It is read through the section
	// and not through the key, because the key need not exist: a stock 3.5.2
	// answers GET /_node/_local/_config/log with {} and 404s the key itself
	// (checked live on cdb-test, 2026-09-14), while 3.0.1 has both level and
	// writer. So the cleanup restores what was there, and removes the key
	// again when there was nothing.
	before, err := s.Client.Config(ctx, "_local", "log", "")
	if err != nil {
		t.Fatal(err)
	}
	original, had := "", false
	for _, e := range before {
		if e.Key == "level" {
			original, had = e.Value, true
		}
	}
	t.Cleanup(func() {
		bg := context.Background()
		if had {
			_, _ = s.Client.SetConfig(bg, "_local", "log", "level", original)
			return
		}
		_, _ = s.Client.DeleteConfig(bg, "_local", "log", "level")
	})

	if _, err := invoke(t, ConfigCmd(), s, "set", "log/level", "debug"); err != nil {
		t.Fatal(err)
	}
	res, err := invoke(t, ConfigCmd(), s, "log/level")
	if err != nil {
		t.Fatal(err)
	}
	rows := res.(Rows)
	if len(rows.Items) != 1 || rows.Items[0].Cells[2] != "debug" {
		t.Fatalf("after set, config log/level = %#v", rows.Items)
	}

	if _, err := invoke(t, ConfigCmd(), s, "unset", "log/level"); err != nil {
		t.Fatal(err)
	}
	if _, err := invoke(t, ConfigCmd(), s, "reload"); err != nil {
		t.Fatal(err)
	}
}

func TestConfigRedactsTheProxySecretOnALiveServer(t *testing.T) {
	if os.Getenv("CDB_TEST_URL") == "" || os.Getenv("CDB_TEST_PROXY_SECRET") == "" {
		t.Skip("CDB_TEST_URL and CDB_TEST_PROXY_SECRET are not both set")
	}
	s := integrationSession(t)
	res, err := invoke(t, ConfigCmd(), s, "chttpd_auth/secret")
	if err != nil {
		t.Fatal(err)
	}
	rows := res.(Rows)
	if len(rows.Items) != 1 {
		t.Fatalf("rows = %#v", rows.Items)
	}
	if rows.Items[0].Cells[2] != "****" {
		t.Fatal("the live proxy secret was printed in full")
	}
	if strings.Contains(string(rows.Items[0].JSON), os.Getenv("CDB_TEST_PROXY_SECRET")) {
		t.Fatal("the live proxy secret reached Row.JSON")
	}
}

func TestSecurityRoundTripAgainstALiveServer(t *testing.T) {
	if os.Getenv("CDB_TEST_URL") == "" {
		t.Skip("CDB_TEST_URL is not set")
	}
	s := integrationSession(t)
	s.Prefs.Yes = true
	ctx := context.Background()
	const db = "cdb-test-security"
	if err := s.Client.CreateDatabase(ctx, db, false, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Client.DestroyDatabase(context.Background(), db) })

	if _, err := invoke(t, SecurityCmd(), s, "/"+db,
		"--add-member", "alice", "--add-member-role", "reader", "--add-admin", "ops"); err != nil {
		t.Fatal(err)
	}
	res, err := invoke(t, SecurityCmd(), s, "/"+db)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, item := range res.(Rows).Items {
		got[strings.Join(item.Cells, "/")] = true
	}
	for _, want := range []string{"admins/names/ops", "members/names/alice", "members/roles/reader"} {
		if !got[want] {
			t.Errorf("after the edit, %q is missing from %v", want, got)
		}
	}

	if _, err := invoke(t, SecurityCmd(), s, "/"+db, "--remove-member", "alice"); err != nil {
		t.Fatal(err)
	}
	res, err = invoke(t, SecurityCmd(), s, "/"+db)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range res.(Rows).Items {
		if strings.Join(item.Cells, "/") == "members/names/alice" {
			t.Fatal("alice is still a member after --remove-member")
		}
	}
}

// TestCompactAgainstALiveServer compacts a database with a few hundred
// documents, which is §10's case: big enough that CouchDB has work to do,
// small enough that the test finishes.
func TestCompactAgainstALiveServer(t *testing.T) {
	if os.Getenv("CDB_TEST_URL") == "" {
		t.Skip("CDB_TEST_URL is not set")
	}
	s := integrationSession(t)
	s.Prefs.Yes = true
	ctx := context.Background()
	const db = "cdb-test-compact"
	if err := s.Client.CreateDatabase(ctx, db, false, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Client.DestroyDatabase(context.Background(), db) })

	// Write each document twice, so compaction has old revisions to drop.
	for i := 0; i < 300; i++ {
		id := fmt.Sprintf("doc-%03d", i)
		rev, err := s.Client.PutDocument(ctx, db, id, json.RawMessage(`{"n":1}`), "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Client.PutDocument(ctx, db, id, json.RawMessage(`{"n":2}`), rev); err != nil {
			t.Fatal(err)
		}
	}

	res, err := invoke(t, Compact(), s, "/"+db, "--cleanup")
	if err != nil {
		t.Fatal(err)
	}
	want := "Compaction of " + db + " started. Orphaned view indexes are being cleaned up as well."
	if msg := res.(Message).Text; msg != want {
		t.Errorf("message = %q, want %q", msg, want)
	}

	// The watch: drive it directly, because --watch needs a terminal and the
	// test has none. A short window keeps the test bounded whether or not the
	// compaction is still running by the time the first poll lands.
	st := watchCompactionWith(ctx, s, db, db, 200*time.Millisecond, 10*time.Second, time.Now)
	lines := 0
	for {
		row, ok, err := st.Next()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		lines++
		t.Logf("watch row: %v", row.Cells)
		if lines > 200 {
			t.Fatal("the watch did not end")
		}
	}
	if lines == 0 {
		t.Fatal("the watch produced no rows at all; it must at least say the compaction finished")
	}
}
