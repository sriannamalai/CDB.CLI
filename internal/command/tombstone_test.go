package command

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/backup"
	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
)

// tombstoneServer has one live document and one deleted one.
func tombstoneServer(t *testing.T) *couchtest.Server {
	t.Helper()
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb", 200, `{"db_name":"mydb","doc_count":1,"doc_del_count":1,"sizes":{"file":1,"external":1},"cluster":{"q":1,"n":1},"props":{}}`)
	srv.JSONSeq("GET", "/mydb/_changes", 200,
		`{"results":[
			{"seq":"1-x","id":"a","changes":[{"rev":"1-aa"}]},
			{"seq":"2-y","id":"gone","deleted":true,"changes":[{"rev":"3-c0ffee"}]}],
			"last_seq":"2-y","pending":0}`,
		`{"results":[],"last_seq":"2-y","pending":0}`)
	// The stub must answer for the ids actually asked for, not with a fixed
	// body: without --tombstones, backup filters the deleted row out before it
	// posts, and a static answer would hand back the tombstone anyway and make
	// the default-behaviour test assert the opposite of what it means to.
	srv.On("POST", "/mydb/_bulk_get", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Docs []struct{ ID, Rev string } `json:"docs"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		results := make([]string, 0, len(req.Docs))
		for _, d := range req.Docs {
			switch d.ID {
			case "a":
				results = append(results, `{"id":"a","docs":[{"ok":{"_id":"a","_rev":"1-aa","_revisions":{"start":1,"ids":["aa"]}}}]}`)
			case "gone":
				results = append(results, `{"id":"gone","docs":[{"ok":{"_id":"gone","_rev":"3-c0ffee","_deleted":true,"_revisions":{"start":3,"ids":["c0ffee","dead","beef"]}}}]}`)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, `{"results":[`+strings.Join(results, ",")+`]}`)
	})
	return srv
}

// readDump walks a dump and returns its header, footer and document bodies.
func readDump(t *testing.T, p string) (*backup.Header, *backup.Footer, []json.RawMessage) {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r, err := backup.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	var (
		header *backup.Header
		footer *backup.Footer
		docs   []json.RawMessage
	)
	for {
		rec, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		switch rec.Kind {
		case backup.KindHeader:
			header = rec.Header
		case backup.KindFooter:
			footer = rec.Footer
		case backup.KindDoc:
			docs = append(docs, append(json.RawMessage(nil), rec.Doc...))
		}
	}
	return header, footer, docs
}

func TestBackupSkipsTombstonesByDefault(t *testing.T) {
	srv := tombstoneServer(t)
	s := connected(t, srv)
	out := filepath.Join(t.TempDir(), "mydb.cdb.gz")

	res, err := invoke(t, Backup(), s, "/mydb", out)
	if err != nil {
		t.Fatal(err)
	}
	header, footer, docs := readDump(t, out)
	if header.Version != "1.1" {
		t.Errorf("version = %q, want 1.1 on every 1.1 dump", header.Version)
	}
	if header.Tombstones {
		t.Error("header claims tombstones without the flag")
	}
	if len(docs) != 1 || !strings.Contains(string(docs[0]), `"_id":"a"`) {
		t.Fatalf("docs = %s", docs)
	}
	if footer.Deleted != 0 {
		t.Errorf("footer.Deleted = %d, want 0", footer.Deleted)
	}
	if msg := res.(Message).Text; strings.Contains(msg, "deletions") {
		t.Errorf("message mentions deletions: %q", msg)
	}
	// The wide feed is still what backup reads.
	if got := srv.Last("GET", "/mydb/_changes").Query("style"); got != "all_docs" {
		t.Errorf("style = %q, want all_docs", got)
	}
}

func TestBackupWithTombstonesWritesThem(t *testing.T) {
	srv := tombstoneServer(t)
	s := connected(t, srv)
	out := filepath.Join(t.TempDir(), "mydb.cdb.gz")

	res, err := invoke(t, Backup(), s, "/mydb", out, "--tombstones")
	if err != nil {
		t.Fatal(err)
	}
	header, footer, docs := readDump(t, out)
	if !header.Tombstones {
		t.Error("header does not record tombstones")
	}
	if len(docs) != 2 {
		t.Fatalf("got %d doc records, want 2: %s", len(docs), docs)
	}
	if !backup.IsTombstone(docs[1]) {
		t.Errorf("second record is not a tombstone: %s", docs[1])
	}
	if !strings.Contains(string(docs[1]), `"_revisions"`) {
		t.Errorf("tombstone lost its _revisions: %s", docs[1])
	}
	if footer.Docs != 2 || footer.Deleted != 1 {
		t.Errorf("footer = %+v, want Docs 2 and Deleted 1", footer)
	}
	if msg := res.(Message).Text; !strings.Contains(msg, "Wrote 2 document(s) (1 of them deletions)") {
		t.Errorf("message = %q", msg)
	}
	// The deleted revision must have been asked for.
	body := string(srv.Last("POST", "/mydb/_bulk_get").Body)
	if !strings.Contains(body, `"id":"gone"`) || !strings.Contains(body, `"rev":"3-c0ffee"`) {
		t.Errorf("_bulk_get body did not request the deleted revision: %s", body)
	}
	if got := srv.Last("POST", "/mydb/_bulk_get").Query("revs"); got != "true" {
		t.Errorf("revs = %q, want true", got)
	}
}

func TestBackupResumeRefusesAModeMismatch(t *testing.T) {
	for _, tc := range []struct {
		name       string
		wrote      bool
		resumeWith []string
		want       string
	}{
		{"file has none, flag given", false, []string{"--resume", "--tombstones"}, "was written without tombstones; resume it without --tombstones, or start a new dump."},
		{"file has them, flag absent", true, []string{"--resume"}, "was written with tombstones; resume it with --tombstones, or start a new dump."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := tombstoneServer(t)
			s := connected(t, srv)
			// The dump has to be *incomplete*: a dump that ends in a footer is
			// short-circuited by backup.go's res.Complete branch, which returns
			// a Message and a nil error long before any mode check runs. So the
			// file is built here — header, one doc, a checkpoint, no footer —
			// rather than by running backup to completion.
			out := filepath.Join(t.TempDir(), "mydb.cdb.gz")
			f, err := os.Create(out)
			if err != nil {
				t.Fatal(err)
			}
			w := backup.NewWriter(f)
			if err := w.WriteHeader(backup.Header{Version: backup.FormatVersion, DB: "mydb", Tombstones: tc.wrote}); err != nil {
				t.Fatal(err)
			}
			_ = w.WriteDoc(json.RawMessage(`{"_id":"a","_rev":"1-aa"}`))
			_ = w.WriteCheckpoint("1-x")
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(out)
			if err != nil {
				t.Fatal(err)
			}

			_, err = invoke(t, Backup(), s, append([]string{"/mydb", out}, tc.resumeWith...)...)
			var ue *UsageError
			if err == nil || !errors.As(err, &ue) {
				t.Fatalf("err = %v, want a UsageError", err)
			}
			if !strings.Contains(ue.Error(), tc.want) {
				t.Errorf("message = %q, want it to contain %q", ue.Error(), tc.want)
			}
			// A refused resume must not have touched the operator's dump: the
			// check has to run before backup.Truncate, not after it.
			after, err := os.Stat(out)
			if err != nil {
				t.Fatal(err)
			}
			if after.Size() != before.Size() {
				t.Errorf("dump size %d → %d; a refused resume truncated the file", before.Size(), after.Size())
			}
		})
	}
}

// tombstoneDumpFile writes a dump holding one live document and one tombstone.
func tombstoneDumpFile(t *testing.T) string {
	t.Helper()
	srv := tombstoneServer(t)
	s := connected(t, srv)
	out := filepath.Join(t.TempDir(), "mydb.cdb.gz")
	if _, err := invoke(t, Backup(), s, "/mydb", out, "--tombstones"); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRestoreLoadsTombstones(t *testing.T) {
	file := tombstoneDumpFile(t)

	srv := couchtest.New(t)
	srv.JSON("HEAD", "/target", 200, ``)
	srv.JSON("GET", "/target", 200, `{"db_name":"target","doc_count":0,"sizes":{"file":1,"external":1},"cluster":{"q":1,"n":1},"props":{}}`)
	srv.JSON("POST", "/target/_bulk_docs", 201, `[]`)
	s := connected(t, srv)

	res, err := invoke(t, Restore(), s, file, "/target")
	if err != nil {
		t.Fatal(err)
	}
	body := string(srv.Last("POST", "/target/_bulk_docs").Body)
	if !strings.Contains(body, `"new_edits":false`) {
		t.Errorf("_bulk_docs body has no new_edits:false: %s", body)
	}
	if !strings.Contains(body, `"_deleted":true`) {
		t.Errorf("_bulk_docs body carries no tombstone: %s", body)
	}
	if !strings.Contains(body, `"_revisions"`) {
		t.Errorf("_bulk_docs body lost the revision history: %s", body)
	}
	if msg := res.(Message).Text; !strings.Contains(msg, "Restored 2 document(s) (1 of them deletions)") {
		t.Errorf("message = %q", msg)
	}
}

func TestRestoreRefusesAnAttachmentAfterATombstone(t *testing.T) {
	p := filepath.Join(t.TempDir(), "bad.cdb.gz")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	w := backup.NewWriter(f)
	_ = w.WriteHeader(backup.Header{Version: backup.FormatVersion, DB: "mydb", Tombstones: true})
	_ = w.WriteDoc(json.RawMessage(`{"_id":"gone","_rev":"3-c0ffee","_deleted":true,"_revisions":{"start":3,"ids":["c0ffee","dead","beef"]}}`))
	_ = w.WriteAttachment(backup.Att{ID: "gone", Rev: "3-c0ffee", Name: "photo.jpg", ContentType: "image/jpeg", Length: 5}, strings.NewReader("hello"))
	_ = w.WriteFooter(backup.Footer{Docs: 1, Deleted: 1, Attachments: 1, LastSeq: "1-a"})
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	srv := couchtest.New(t)
	srv.JSON("HEAD", "/target", 200, ``)
	srv.JSON("GET", "/target", 200, `{"db_name":"target","doc_count":0,"sizes":{"file":1,"external":1},"cluster":{"q":1,"n":1},"props":{}}`)
	srv.JSON("POST", "/target/_bulk_docs", 201, `[]`)
	s := connected(t, srv)

	_, err = invoke(t, Restore(), s, p, "/target")
	if err == nil {
		t.Fatal("restore accepted an attachment on a deletion")
	}
	want := `attachment "photo.jpg" follows the deletion of "gone", which can hold no attachments.`
	if !strings.Contains(err.Error(), want) {
		t.Errorf("err = %q, want it to contain %q", err, want)
	}
}

func TestRestoreRefusesANewerDumpFormat(t *testing.T) {
	p := filepath.Join(t.TempDir(), "future.cdb.gz")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	w := backup.NewWriter(f)
	_ = w.WriteHeader(backup.Header{Version: "1.2", DB: "mydb"})
	_ = w.WriteFooter(backup.Footer{Docs: 0, LastSeq: "0"})
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	s := connected(t, couchtest.New(t))
	_, err = invoke(t, Restore(), s, p, "/target")
	var ue *UsageError
	if err == nil || !errors.As(err, &ue) {
		t.Fatalf("err = %v, want a UsageError", err)
	}
	if !strings.Contains(ue.Error(), "was written by a newer cdb (dump format 1.2); upgrade cdb to read it.") {
		t.Errorf("message = %q", ue.Error())
	}
}

// TestBackupResumeRefusesANewerDumpFormat is the backup-side companion to
// TestRestoreRefusesANewerDumpFormat: both commands map backup.VersionError
// to the same usage sentence, so both need their own command-level test of it.
func TestBackupResumeRefusesANewerDumpFormat(t *testing.T) {
	p := filepath.Join(t.TempDir(), "future.cdb.gz")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	w := backup.NewWriter(f)
	_ = w.WriteHeader(backup.Header{Version: "9.9", DB: "mydb"})
	_ = w.WriteFooter(backup.Footer{Docs: 0, LastSeq: "0"})
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	s := connected(t, couchtest.New(t))
	_, err = invoke(t, Backup(), s, "/mydb", p, "--resume")
	var ue *UsageError
	if err == nil || !errors.As(err, &ue) {
		t.Fatalf("err = %v, want a UsageError", err)
	}
	if !strings.Contains(ue.Error(), "was written by a newer cdb (dump format 9.9); upgrade cdb to read it.") {
		t.Errorf("message = %q", ue.Error())
	}
}
