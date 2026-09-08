package command

import (
	"compress/gzip"
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

// writeDump builds a small dump file and returns its path.
func writeDump(t *testing.T, partitioned bool) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "dump.cdb.gz")
	f, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := backup.NewWriter(f)
	if err := w.WriteHeader(backup.Header{DB: "mydb", Server: "3.5.2", Partitioned: partitioned}); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteDoc(json.RawMessage(`{"_id":"a","_rev":"1-aa","_revisions":{"start":1,"ids":["aa"]},"v":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteDoc(json.RawMessage(`{"_id":"b","_rev":"2-bb","_revisions":{"start":2,"ids":["bb","cc"]},"v":2}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteAttachment(backup.Att{ID: "b", Rev: "2-bb", Name: "note.txt", ContentType: "text/plain", Length: 5}, strings.NewReader("hello")); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteCheckpoint("2-y"); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteFooter(backup.Footer{Docs: 2, Attachments: 1, LastSeq: "2-y"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestRestoreIntoAnEmptyDatabaseInlinesAttachments(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/target", 200, `{"db_name":"target","doc_count":0,"sizes":{"file":1,"external":1},"cluster":{"q":1,"n":1},"props":{}}`)
	srv.On("HEAD", "/target", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	srv.JSON("POST", "/target/_bulk_docs", 201, `[]`)
	dump := writeDump(t, false)
	s := connected(t, srv)

	res, err := invoke(t, Restore(), s, dump, "/target")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := res.(Message); !ok {
		t.Fatalf("result is %T, want Message", res)
	}
	body := string(srv.Last("POST", "/target/_bulk_docs").Body)
	if !strings.Contains(body, `"new_edits":false`) {
		t.Errorf("bulk body = %s, want new_edits false", body)
	}
	if !strings.Contains(body, `"_revisions"`) {
		t.Errorf("bulk body = %s, want _revisions preserved", body)
	}
	// "hello" base64-encoded.
	if !strings.Contains(body, `"aGVsbG8="`) {
		t.Errorf("bulk body = %s, want the attachment inlined as base64", body)
	}
	if !strings.Contains(body, `"note.txt"`) {
		t.Errorf("bulk body = %s, want the attachment name", body)
	}
	if strings.Contains(body, `"stub":true`) {
		t.Errorf("bulk body = %s, must not contain an attachment stub", body)
	}
	if srv.Last("PUT", "/target/b/note.txt") != nil {
		t.Error("restore uploaded the attachment separately, which bumps the revision")
	}
}

func TestRestoreCreatesTheTargetWithTheHeaderPartitioning(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("HEAD", "/target", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(404) })
	srv.JSON("PUT", "/target", 201, `{"ok":true}`)
	srv.JSON("POST", "/target/_bulk_docs", 201, `[]`)
	dump := writeDump(t, true)
	s := connected(t, srv)
	if _, err := invoke(t, Restore(), s, dump, "/target", "--create"); err != nil {
		t.Fatal(err)
	}
	if got := srv.Last("PUT", "/target").Query("partitioned"); got != "true" {
		t.Errorf("partitioned = %q, want true", got)
	}
}

func TestRestoreCreatesTheTargetForAnEmptyDump(t *testing.T) {
	name := filepath.Join(t.TempDir(), "empty.cdb.gz")
	f, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	w := backup.NewWriter(f)
	if err := w.WriteHeader(backup.Header{DB: "mydb", Server: "3.5.2"}); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteFooter(backup.Footer{LastSeq: "0"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	srv := couchtest.New(t)
	srv.On("HEAD", "/target", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(404) })
	srv.JSON("PUT", "/target", 201, `{"ok":true}`)
	s := connected(t, srv)
	if _, err := invoke(t, Restore(), s, name, "/target", "--create"); err != nil {
		t.Fatal(err)
	}
	if srv.Last("PUT", "/target") == nil {
		t.Error("restore --create of an empty dump created nothing but reported success")
	}
}

func TestRestoreRefusesANonEmptyTargetWithoutMerge(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("HEAD", "/target", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	srv.JSON("GET", "/target", 200, `{"db_name":"target","doc_count":5,"sizes":{"file":1,"external":1},"cluster":{"q":1,"n":1},"props":{}}`)
	dump := writeDump(t, false)
	s := connected(t, srv)
	_, err := invoke(t, Restore(), s, dump, "/target")
	if _, ok := err.(*UsageError); !ok {
		t.Fatalf("err = %#v, want *UsageError", err)
	}
	if srv.Last("POST", "/target/_bulk_docs") != nil {
		t.Error("restore wrote into a non-empty target")
	}
}

func TestRestoreIntoANonEmptyTargetWithMerge(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("HEAD", "/target", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	srv.JSON("GET", "/target", 200, `{"db_name":"target","doc_count":5,"sizes":{"file":1,"external":1},"cluster":{"q":1,"n":1},"props":{}}`)
	srv.JSON("POST", "/target/_bulk_docs", 201, `[]`)
	dump := writeDump(t, false)
	s := connected(t, srv)
	if _, err := invoke(t, Restore(), s, dump, "/target", "--merge"); err != nil {
		t.Fatal(err)
	}
	if srv.Last("POST", "/target/_bulk_docs") == nil {
		t.Error("restore --merge wrote nothing")
	}
}

func TestRestoreReportsBulkErrors(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("HEAD", "/target", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	srv.JSON("GET", "/target", 200, `{"db_name":"target","doc_count":0,"sizes":{"file":1,"external":1},"cluster":{"q":1,"n":1},"props":{}}`)
	srv.JSON("POST", "/target/_bulk_docs", 201, `[{"id":"a","error":"forbidden","reason":"nope"}]`)
	dump := writeDump(t, false)
	s := connected(t, srv)
	_, err := invoke(t, Restore(), s, dump, "/target")
	if err == nil {
		t.Fatal("restore hid a per-document error")
	}
	if !strings.Contains(err.Error(), "forbidden") {
		t.Errorf("error = %v, want it to mention the rejection", err)
	}
}

func TestRestoreRejectsANonDatabaseTarget(t *testing.T) {
	srv := couchtest.New(t)
	dump := writeDump(t, false)
	s := connected(t, srv)
	_, err := invoke(t, Restore(), s, dump, "/target/doc1")
	if _, ok := err.(*UsageError); !ok {
		t.Fatalf("err = %#v, want *UsageError", err)
	}
}

// writeIncompleteDump builds a dump that stops after a checkpoint, as an
// interrupted backup leaves it: every record is intact but the footer is
// missing, so the dump does not hold everything the database held.
func writeIncompleteDump(t *testing.T) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "partial.cdb.gz")
	f, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := backup.NewWriter(f)
	if err := w.WriteHeader(backup.Header{DB: "mydb", Server: "3.5.2"}); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteDoc(json.RawMessage(`{"_id":"a","_rev":"1-aa","_revisions":{"start":1,"ids":["aa"]}}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteCheckpoint("1-x"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestRestoreRefusesAnIncompleteDumpWithoutPartial(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("HEAD", "/target", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	srv.JSON("GET", "/target", 200, `{"db_name":"target","doc_count":0,"sizes":{"file":1,"external":1},"cluster":{"q":1,"n":1},"props":{}}`)
	srv.JSON("POST", "/target/_bulk_docs", 201, `[]`)
	s := connected(t, srv)
	_, err := invoke(t, Restore(), s, writeIncompleteDump(t), "/target")
	if _, ok := err.(*UsageError); !ok {
		t.Fatalf("err = %#v, want *UsageError", err)
	}
	if srv.Last("POST", "/target/_bulk_docs") != nil {
		t.Error("restore wrote from an incomplete dump before refusing it")
	}
}

func TestRestoreLoadsAnIncompleteDumpWithPartial(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("HEAD", "/target", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	srv.JSON("GET", "/target", 200, `{"db_name":"target","doc_count":0,"sizes":{"file":1,"external":1},"cluster":{"q":1,"n":1},"props":{}}`)
	srv.JSON("POST", "/target/_bulk_docs", 201, `[]`)
	s := connected(t, srv)
	if _, err := invoke(t, Restore(), s, writeIncompleteDump(t), "/target", "--partial"); err != nil {
		t.Fatal(err)
	}
	req := srv.Last("POST", "/target/_bulk_docs")
	if req == nil {
		t.Fatal("restore --partial wrote nothing")
	}
	if !strings.Contains(string(req.Body), `"_id":"a"`) {
		t.Errorf("bulk body = %s, want the one document the dump holds", req.Body)
	}
}

func TestRestoreRejectsAFileThatIsNotADump(t *testing.T) {
	name := filepath.Join(t.TempDir(), "notadump.gz")
	f, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	w := backup.NewWriter(f)
	// A well-formed gzip stream whose first record is not a header.
	if err := w.WriteDoc(json.RawMessage(`{"_id":"a"}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	srv := couchtest.New(t)
	s := connected(t, srv)
	_, err = invoke(t, Restore(), s, name, "/target")
	if err == nil {
		t.Fatal("restore accepted a file that is not a cdb dump")
	}
	if !strings.Contains(err.Error(), "not a cdb dump") {
		t.Errorf("error = %v, want it to say the file is not a dump", err)
	}
	// Naming the wrong file is a usage mistake (exit 2), not a command
	// failure — and the command the operator ran was restore, not backup.
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Errorf("error is %T, want a *UsageError so the front-ends exit 2", err)
	}
	if strings.Contains(err.Error(), "backup:") {
		t.Errorf("error = %v, but the command the operator ran was restore", err)
	}
	if srv.Last("HEAD", "/target") != nil {
		t.Error("restore touched the server before reading the dump")
	}
}

func TestRestoreReportsATruncatedAttachmentPayload(t *testing.T) {
	// An att record declaring more bytes than the dump holds: the tail was
	// lost. Reading only what is there would silently restore a short
	// attachment, so restore must report the mismatch instead.
	name := filepath.Join(t.TempDir(), "torn.cdb.gz")
	f, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	for _, line := range []string{
		`{"kind":"header","db":"mydb","server":"3.5.2"}`,
		`{"kind":"doc","doc":{"_id":"b","_rev":"2-bb","_revisions":{"start":2,"ids":["bb","cc"]}}}`,
		`{"kind":"att","id":"b","rev":"2-bb","name":"note.txt","content_type":"text/plain","length":64}`,
	} {
		if _, err := io.WriteString(gz, line+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	// 5 bytes where the record declared 64, and then the stream ends.
	if _, err := io.WriteString(gz, "hello"); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	srv := couchtest.New(t)
	srv.On("HEAD", "/target", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	srv.JSON("GET", "/target", 200, `{"db_name":"target","doc_count":0,"sizes":{"file":1,"external":1},"cluster":{"q":1,"n":1},"props":{}}`)
	srv.JSON("POST", "/target/_bulk_docs", 201, `[]`)
	s := connected(t, srv)
	if _, err := invoke(t, Restore(), s, name, "/target", "--partial"); err == nil {
		t.Fatal("restore accepted a dump whose attachment payload is short")
	}
}

// writeLargeAttachmentDump builds a dump whose single document carries an
// attachment just over inlineAttachmentLimit, which restore cannot inline.
func writeLargeAttachmentDump(t *testing.T) (string, []byte) {
	t.Helper()
	payload := make([]byte, inlineAttachmentLimit+1)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	name := filepath.Join(t.TempDir(), "big.cdb.gz")
	f, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := backup.NewWriter(f)
	if err := w.WriteHeader(backup.Header{DB: "mydb", Server: "3.5.2"}); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteDoc(json.RawMessage(`{"_id":"b","_rev":"2-bb","_revisions":{"start":2,"ids":["bb","cc"]}}`)); err != nil {
		t.Fatal(err)
	}
	att := backup.Att{ID: "b", Rev: "2-bb", Name: "big.bin", ContentType: "application/octet-stream", Length: int64(len(payload))}
	if err := w.WriteAttachment(att, strings.NewReader(string(payload))); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteFooter(backup.Footer{Docs: 1, Attachments: 1, LastSeq: "1-x"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return name, payload
}

func TestRestoreUploadsAnOversizedAttachmentAndReportsTheNewRevision(t *testing.T) {
	dump, payload := writeLargeAttachmentDump(t)
	srv := couchtest.New(t)
	srv.On("HEAD", "/target", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	srv.JSON("GET", "/target", 200, `{"db_name":"target","doc_count":0,"sizes":{"file":1,"external":1},"cluster":{"q":1,"n":1},"props":{}}`)
	srv.JSON("POST", "/target/_bulk_docs", 201, `[]`)
	srv.JSON("PUT", "/target/b/big.bin", 201, `{"ok":true,"id":"b","rev":"3-cc"}`)
	s := connected(t, srv)

	res, err := invoke(t, Restore(), s, dump, "/target")
	if err != nil {
		t.Fatal(err)
	}
	bulk := srv.Last("POST", "/target/_bulk_docs")
	if bulk == nil {
		t.Fatal("restore wrote no documents")
	}
	if strings.Contains(string(bulk.Body), `"_attachments"`) {
		t.Errorf("bulk body = %s, must not inline an oversized attachment", bulk.Body)
	}
	put := srv.Last("PUT", "/target/b/big.bin")
	if put == nil {
		t.Fatal("restore never uploaded the oversized attachment")
	}
	if got := put.Query("rev"); got != "2-bb" {
		t.Errorf("upload rev = %q, want the revision the bulk write restored", got)
	}
	if string(put.Body) != string(payload) {
		t.Errorf("uploaded %d bytes, want %d", len(put.Body), len(payload))
	}
	msg, ok := res.(Message)
	if !ok {
		t.Fatalf("result is %T, want Message", res)
	}
	if !strings.Contains(msg.Text, "changed revision") || !strings.Contains(msg.Text, "3-cc") {
		t.Errorf("message = %q, want it to name the document whose revision changed", msg.Text)
	}
}

func TestRestoreKeepsAttachmentsInlineAcrossABatchBoundary(t *testing.T) {
	// The attachment of the document that closes a batch must still be
	// inlined: uploading it separately would bump the revision the batch just
	// restored.
	name := filepath.Join(t.TempDir(), "batched.cdb.gz")
	f, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	w := backup.NewWriter(f)
	if err := w.WriteHeader(backup.Header{DB: "mydb", Server: "3.5.2"}); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteDoc(json.RawMessage(`{"_id":"a","_rev":"1-aa","_revisions":{"start":1,"ids":["aa"]}}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteDoc(json.RawMessage(`{"_id":"b","_rev":"1-bb","_revisions":{"start":1,"ids":["bb"]}}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteAttachment(backup.Att{ID: "b", Rev: "1-bb", Name: "note.txt", ContentType: "text/plain", Length: 5}, strings.NewReader("hello")); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteDoc(json.RawMessage(`{"_id":"c","_rev":"1-cc","_revisions":{"start":1,"ids":["cc"]}}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteFooter(backup.Footer{Docs: 3, Attachments: 1, LastSeq: "3-z"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	srv := couchtest.New(t)
	srv.On("HEAD", "/target", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	srv.JSON("GET", "/target", 200, `{"db_name":"target","doc_count":0,"sizes":{"file":1,"external":1},"cluster":{"q":1,"n":1},"props":{}}`)
	srv.JSON("POST", "/target/_bulk_docs", 201, `[]`)
	s := connected(t, srv)
	if _, err := invoke(t, Restore(), s, name, "/target", "--batch", "2"); err != nil {
		t.Fatal(err)
	}
	if srv.Last("PUT", "/target/b/note.txt") != nil {
		t.Error("restore uploaded a batch-boundary attachment separately, bumping its revision")
	}
	var inlined bool
	for _, r := range srv.Requests() {
		if r.Method == "POST" && r.Path == "/target/_bulk_docs" && strings.Contains(string(r.Body), `"aGVsbG8="`) {
			inlined = true
		}
	}
	if !inlined {
		t.Error("no bulk write carried the inlined attachment")
	}
}

func TestRestorePartialReadsUpToTheLastIntactMember(t *testing.T) {
	// The shape a Ctrl-C leaves: the records written before the last
	// checkpoint are intact and the trailing gzip member is torn. --partial
	// has to load the intact prefix rather than fail on the torn bytes.
	dump := writeDump(t, false)
	data, err := os.ReadFile(dump)
	if err != nil {
		t.Fatal(err)
	}
	torn := filepath.Join(t.TempDir(), "torn-tail.cdb.gz")
	if err := os.WriteFile(torn, data[:len(data)-5], 0o600); err != nil {
		t.Fatal(err)
	}

	srv := couchtest.New(t)
	srv.On("HEAD", "/target", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	srv.JSON("GET", "/target", 200, `{"db_name":"target","doc_count":0,"sizes":{"file":1,"external":1},"cluster":{"q":1,"n":1},"props":{}}`)
	srv.JSON("POST", "/target/_bulk_docs", 201, `[]`)
	s := connected(t, srv)
	if _, err := invoke(t, Restore(), s, torn, "/target", "--partial"); err != nil {
		t.Fatal(err)
	}
	req := srv.Last("POST", "/target/_bulk_docs")
	if req == nil {
		t.Fatal("restore --partial of a torn dump wrote nothing")
	}
	body := string(req.Body)
	for _, want := range []string{`"_id":"a"`, `"_id":"b"`, `"aGVsbG8="`} {
		if !strings.Contains(body, want) {
			t.Errorf("bulk body = %s, want it to contain %s", body, want)
		}
	}
}
