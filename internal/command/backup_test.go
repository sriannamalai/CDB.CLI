package command

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/backup"
	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
)

func backupServer(t *testing.T) *couchtest.Server {
	t.Helper()
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb", 200, `{"db_name":"mydb","doc_count":2,"sizes":{"file":1,"external":1},"cluster":{"q":1,"n":1},"props":{"partitioned":true}}`)
	srv.JSONSeq("GET", "/mydb/_changes", 200,
		`{"results":[{"seq":"1-x","id":"a","changes":[{"rev":"1-aa"}]},{"seq":"2-y","id":"b","changes":[{"rev":"1-bb"}]}],"last_seq":"2-y","pending":0}`,
		`{"results":[],"last_seq":"2-y","pending":0}`)
	srv.JSON("POST", "/mydb/_bulk_get", 200, `{"results":[
		{"id":"a","docs":[{"ok":{"_id":"a","_rev":"1-aa","_revisions":{"start":1,"ids":["aa"]}}}]},
		{"id":"b","docs":[{"ok":{"_id":"b","_rev":"1-bb","_revisions":{"start":1,"ids":["bb"]},"_attachments":{"note.txt":{"content_type":"text/plain","length":33,"stub":true,"revpos":1}}}}]}]}`)
	srv.On("GET", "/mydb/b/note.txt", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "hello")
	})
	return srv
}

func TestBackupWritesADump(t *testing.T) {
	srv := backupServer(t)
	s := connected(t, srv)
	out := filepath.Join(t.TempDir(), "mydb.cdb.gz")
	res, err := invoke(t, Backup(), s, "/mydb", out)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := res.(Message); !ok {
		t.Fatalf("result is %T, want Message", res)
	}

	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r, err := backup.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	var (
		header  *backup.Header
		ids     []string
		attName string
		attBody string
		footer  *backup.Footer
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
		case backup.KindDoc:
			var d struct {
				ID   string          `json:"_id"`
				Revs json.RawMessage `json:"_revisions"`
			}
			_ = json.Unmarshal(rec.Doc, &d)
			if len(d.Revs) == 0 {
				t.Errorf("document %s has no _revisions", d.ID)
			}
			if strings.Contains(string(rec.Doc), `"stub":true`) {
				t.Errorf("document %s still carries an attachment stub", d.ID)
			}
			ids = append(ids, d.ID)
		case backup.KindAtt:
			attName = rec.Att.Name
			b, _ := io.ReadAll(rec.Content)
			attBody = string(b)
			if rec.Att.Length != int64(len(b)) {
				t.Errorf("att record Length = %d, want the real byte count %d", rec.Att.Length, len(b))
			}
		case backup.KindFooter:
			footer = rec.Footer
		}
	}
	if header == nil || header.DB != "mydb" || !header.Partitioned {
		t.Errorf("header = %+v", header)
	}
	if strings.Join(ids, ",") != "a,b" {
		t.Errorf("ids = %v", ids)
	}
	if attName != "note.txt" || attBody != "hello" {
		t.Errorf("attachment = %q / %q", attName, attBody)
	}
	if footer == nil || footer.Docs != 2 || footer.Attachments != 1 || footer.LastSeq != "2-y" {
		t.Errorf("footer = %+v", footer)
	}
}

func TestBackupRefusesToOverwriteWithoutResume(t *testing.T) {
	srv := backupServer(t)
	s := connected(t, srv)
	out := filepath.Join(t.TempDir(), "mydb.cdb.gz")
	if err := os.WriteFile(out, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := invoke(t, Backup(), s, "/mydb", out)
	if _, ok := err.(*UsageError); !ok {
		t.Fatalf("err = %#v, want *UsageError", err)
	}
}

func TestBackupResumeContinuesFromTheLastCheckpoint(t *testing.T) {
	srv := backupServer(t)
	s := connected(t, srv)
	out := filepath.Join(t.TempDir(), "mydb.cdb.gz")

	// Write a partial dump that already checkpointed at 1-x.
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	w := backup.NewWriter(f)
	_ = w.WriteHeader(backup.Header{DB: "mydb", Server: "3.5.2"})
	_ = w.WriteDoc(json.RawMessage(`{"_id":"a","_rev":"1-aa"}`))
	_ = w.WriteCheckpoint("1-x")
	_ = w.Close()
	f.Close()

	if _, err := invoke(t, Backup(), s, "/mydb", out, "--resume"); err != nil {
		t.Fatal(err)
	}
	if got := srv.Last("GET", "/mydb/_changes").Query("since"); got != "1-x" {
		t.Errorf("resumed since = %q, want 1-x", got)
	}
}

// A revision the server cannot return is missing data. Backup must stop rather
// than write a footer over a dump that is quietly short a document.
func TestBackupAbortsWhenARevisionCannotBeFetched(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb", 200, `{"db_name":"mydb","doc_count":2,"sizes":{"file":1,"external":1},"cluster":{"q":1,"n":1},"props":{}}`)
	srv.JSONSeq("GET", "/mydb/_changes", 200,
		`{"results":[{"seq":"1-x","id":"a","changes":[{"rev":"1-aa"}]}],"last_seq":"1-x","pending":1}`,
		`{"results":[{"seq":"2-y","id":"b","changes":[{"rev":"1-bb"}]}],"last_seq":"2-y","pending":0}`)
	srv.JSONSeq("POST", "/mydb/_bulk_get", 200,
		`{"results":[{"id":"a","docs":[{"ok":{"_id":"a","_rev":"1-aa","_revisions":{"start":1,"ids":["aa"]}}}]}]}`,
		`{"results":[{"id":"b","docs":[{"error":{"id":"b","rev":"1-bb","error":"not_found","reason":"missing"}}]}]}`)
	s := connected(t, srv)
	out := filepath.Join(t.TempDir(), "mydb.cdb.gz")

	_, err := invoke(t, Backup(), s, "/mydb", out)
	if err == nil {
		t.Fatal("backup reported success despite a revision it could not fetch")
	}
	for _, want := range []string{"1 of 1 revisions", "b@1-bb", "not_found"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q", err, want)
		}
	}

	// The partial dump must carry no footer, and must still resume from the
	// checkpoint the first batch wrote.
	f, oerr := os.Open(out)
	if oerr != nil {
		t.Fatal(oerr)
	}
	defer f.Close()
	res, serr := backup.Scan(f)
	if serr != nil {
		t.Fatal(serr)
	}
	if res.Complete {
		t.Error("the aborted dump carries a footer")
	}
	if res.Seq != "1-x" || res.Docs != 1 {
		t.Errorf("resume = %+v, want the 1-x checkpoint with 1 document", res)
	}
}

// A dump whose first record is not a header is not a cdb dump: backup.Scan
// refuses it, so --resume could never continue it. A fresh dump therefore
// writes a header even when --since starts it part way through the feed.
func TestBackupWithSinceStillWritesAHeader(t *testing.T) {
	srv := backupServer(t)
	s := connected(t, srv)
	out := filepath.Join(t.TempDir(), "mydb.cdb.gz")
	if _, err := invoke(t, Backup(), s, "/mydb", out, "--since", "1-x"); err != nil {
		t.Fatal(err)
	}
	if got := srv.Last("GET", "/mydb/_changes").Query("since"); got != "1-x" {
		t.Errorf("since = %q, want 1-x", got)
	}

	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	res, err := backup.Scan(f)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if !res.Complete {
		t.Errorf("resume = %+v, want a complete dump", res)
	}
}

func TestBackupRejectsANonDatabasePath(t *testing.T) {
	srv := backupServer(t)
	s := connected(t, srv)
	_, err := invoke(t, Backup(), s, "/mydb/doc1", filepath.Join(t.TempDir(), "x.gz"))
	if _, ok := err.(*UsageError); !ok {
		t.Fatalf("err = %#v, want *UsageError", err)
	}
}
