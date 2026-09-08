package command

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/sriannamalai/CDB.CLI/internal/replicate"
)

// This is the one test that proves the claim --tombstones exists for: a
// deletion that survives a dump, a restore into a fresh database, and a
// replication onward from it. Everything else — the footer count, the
// _bulk_docs body — is evidence for it, not the thing itself.
func TestIntegrationTombstoneRoundTripReplicatesTheDeletion(t *testing.T) {
	s := integrationSession(t)
	ctx := context.Background()

	src := integrationDB(t, s, "cdb_it_tomb_src")
	// restore --create and replicate --create-target make these two, so they
	// are only registered for cleanup here.
	dst, onward := "cdb_it_tomb_dst", "cdb_it_tomb_onward"
	for _, db := range []string{dst, onward} {
		name := db
		_ = s.Client.DestroyDatabase(ctx, name)
		t.Cleanup(func() { _ = s.Client.DestroyDatabase(context.Background(), name) })
	}

	if _, err := s.Client.PutDocument(ctx, src, "keep", json.RawMessage(`{"_id":"keep","n":1}`), ""); err != nil {
		t.Fatal(err)
	}
	rev, err := s.Client.PutDocument(ctx, src, "gone", json.RawMessage(`{"_id":"gone","n":2}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Client.DeleteDocument(ctx, src, "gone", rev); err != nil {
		t.Fatal(err)
	}

	file := filepath.Join(t.TempDir(), "src.cdb.gz")
	if _, err := invoke(t, Backup(), s, "/"+src, file, "--tombstones"); err != nil {
		t.Fatal(err)
	}
	if _, err := invoke(t, Restore(), s, file, "/"+dst, "--create"); err != nil {
		t.Fatal(err)
	}

	got, err := s.Client.DatabaseInfo(ctx, dst)
	if err != nil {
		t.Fatal(err)
	}
	if got.DocCount != 1 || got.DeletedCount != 1 {
		t.Fatalf("restored database has doc_count %d and doc_del_count %d, want 1 and 1",
			got.DocCount, got.DeletedCount)
	}

	// A named job so the _replicator document this leaves behind can be
	// removed again: a generated id would be a new orphan on every run.
	const job = "cdb_it_tomb_job"
	_ = replicate.Cancel(ctx, s.Client, job)
	t.Cleanup(func() { _ = replicate.Cancel(context.Background(), s.Client, job) })
	if _, err := invoke(t, Replicate(), s, "/"+dst, "/"+onward, "--create-target", "--id", job); err != nil {
		t.Fatal(err)
	}
	// The replicator runs asynchronously, so poll rather than assert once. A
	// database that does not exist yet is an expected answer here.
	deadline := time.Now().Add(60 * time.Second)
	for {
		info, err := s.Client.DatabaseInfo(ctx, onward)
		if err == nil && info.DocCount == 1 && info.DeletedCount == 1 {
			return // the deletion carried onward, which is the whole point
		}
		if time.Now().After(deadline) {
			t.Fatalf("the deletion did not replicate onward within 60s (last info %+v, err %v). "+
				"If the server cannot dial CDB_TEST_URL from where it runs — a container "+
				"published on a different host port — set CDB_TEST_REPLICATION_URL to the "+
				"address it knows itself by.", info, err)
		}
		time.Sleep(time.Second)
	}
}
