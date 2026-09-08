package command

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/backup"
	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
)

// writeCountedDump writes a dump with one document and the footer counts the
// caller asks for, so the footer can be made to disagree with the body.
func writeCountedDump(t *testing.T, footer backup.Footer) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "dump.cdb.gz")
	f, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := backup.NewWriter(f)
	if err := w.WriteHeader(backup.Header{DB: "mydb", Server: "3.5.2"}); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteDoc(json.RawMessage(`{"_id":"a","_rev":"1-aa","_revisions":{"start":1,"ids":["aa"]},"v":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteFooter(footer); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return name
}

func restoreTarget(t *testing.T) *couchtest.Server {
	t.Helper()
	srv := couchtest.New(t)
	srv.JSON("GET", "/target", 200, `{"db_name":"target","doc_count":0,"sizes":{"file":1,"external":1},"cluster":{"q":1,"n":1},"props":{}}`)
	srv.On("HEAD", "/target", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	srv.JSON("POST", "/target/_bulk_docs", 201, `[]`)
	return srv
}

// The footer is the backup's own count of what it wrote. A complete dump that
// restores fewer documents than the footer claims has lost records somewhere
// between the two, and a restore that reports success would hide it.
func TestRestoreRefusesAFooterThatDisagrees(t *testing.T) {
	srv := restoreTarget(t)
	dump := writeCountedDump(t, backup.Footer{Docs: 3, Attachments: 0, LastSeq: "2-y"})
	s := connected(t, srv)

	_, err := invoke(t, Restore(), s, dump, "/target")
	if err == nil {
		t.Fatal("restore accepted a dump whose footer over-counts")
	}
	for _, want := range []string{"3", "1", "document"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q is missing %q", err, want)
		}
	}
}

func TestRestoreRefusesAFooterAttachmentCountThatDisagrees(t *testing.T) {
	srv := restoreTarget(t)
	dump := writeCountedDump(t, backup.Footer{Docs: 1, Attachments: 2, LastSeq: "2-y"})
	s := connected(t, srv)

	_, err := invoke(t, Restore(), s, dump, "/target")
	if err == nil {
		t.Fatal("restore accepted a dump whose footer over-counts attachments")
	}
	for _, want := range []string{"2", "0", "attachment"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q is missing %q", err, want)
		}
	}
}

// A dump with no footer is read under --partial, where there is nothing to
// cross-check against.
func TestRestorePartialSkipsTheFooterCheck(t *testing.T) {
	srv := restoreTarget(t)
	name := filepath.Join(t.TempDir(), "dump.cdb.gz")
	f, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	w := backup.NewWriter(f)
	if err := w.WriteHeader(backup.Header{DB: "mydb", Server: "3.5.2"}); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteDoc(json.RawMessage(`{"_id":"a","_rev":"1-aa","v":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	s := connected(t, srv)
	if _, err := invoke(t, Restore(), s, name, "/target", "--partial"); err != nil {
		t.Fatal(err)
	}
}

// bigAttachmentDump writes one document carrying count attachments of size
// bytes each.
func bigAttachmentDump(t *testing.T, count int, size int64) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "dump.cdb.gz")
	f, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := backup.NewWriter(f)
	if err := w.WriteHeader(backup.Header{DB: "mydb", Server: "3.5.2"}); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteDoc(json.RawMessage(`{"_id":"a","_rev":"1-aa","_revisions":{"start":1,"ids":["aa"]},"v":1}`)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < count; i++ {
		att := backup.Att{
			ID: "a", Rev: "1-aa",
			Name:        fmt.Sprintf("blob%d.bin", i),
			ContentType: "application/octet-stream",
			Length:      size,
		}
		if err := w.WriteAttachment(att, io.LimitReader(zeroes{}, size)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.WriteFooter(backup.Footer{Docs: 1, Attachments: int64(count), LastSeq: "1-y"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return name
}

type zeroes struct{}

func (zeroes) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

// The batch byte bound is closed at a document boundary, so it could never fire
// inside one document. A document carrying more inline attachment bytes than
// the bound allows has to spill the excess to the streamed PUT path instead of
// holding it all in one _bulk_docs body — and say so, because that path costs
// the document the revision the dump recorded.
func TestRestoreSpillsADocumentWhoseInlineAttachmentsExceedTheBound(t *testing.T) {
	srv := restoreTarget(t)
	var puts atomic.Int32
	srv.On("PUT", "/target/a/blob0.bin", attachmentPut(&puts))
	srv.On("PUT", "/target/a/blob1.bin", attachmentPut(&puts))
	srv.On("PUT", "/target/a/blob2.bin", attachmentPut(&puts))
	srv.On("PUT", "/target/a/blob3.bin", attachmentPut(&puts))
	srv.On("PUT", "/target/a/blob4.bin", attachmentPut(&puts))
	srv.On("PUT", "/target/a/blob5.bin", attachmentPut(&puts))

	// Six 3 MiB attachments are 18 MiB decoded and 24 MiB as base64, over the
	// 16 MiB bound, yet each one is under the 4 MiB inline limit.
	dump := bigAttachmentDump(t, 6, 3<<20)
	s := connected(t, srv)

	res, err := invoke(t, Restore(), s, dump, "/target")
	if err != nil {
		t.Fatal(err)
	}
	if puts.Load() == 0 {
		t.Fatal("no attachment was streamed; the whole document was held inline")
	}
	msg, ok := res.(Message)
	if !ok || !strings.Contains(msg.Text, "changed revision") {
		t.Errorf("result = %#v, want the revision-bump warning", res)
	}
	body := string(srv.Last("POST", "/target/_bulk_docs").Body)
	if n := strings.Count(body, `"content_type"`); n >= 6 {
		t.Errorf("the bulk body inlined %d attachments; the bound should have held some back", n)
	}
}

// The instruction's own case: attachments larger than the inline limit go up on
// their own whatever the batch bound says.
func TestRestoreStreamsAttachmentsOverTheInlineLimit(t *testing.T) {
	srv := restoreTarget(t)
	var puts atomic.Int32
	srv.On("PUT", "/target/a/blob0.bin", attachmentPut(&puts))
	srv.On("PUT", "/target/a/blob1.bin", attachmentPut(&puts))
	srv.On("PUT", "/target/a/blob2.bin", attachmentPut(&puts))

	dump := bigAttachmentDump(t, 3, 6<<20)
	s := connected(t, srv)
	if _, err := invoke(t, Restore(), s, dump, "/target"); err != nil {
		t.Fatal(err)
	}
	if got := puts.Load(); got != 3 {
		t.Errorf("streamed %d attachments, want 3", got)
	}
}

// attachmentPut answers a streamed attachment upload with a fresh revision.
func attachmentPut(n *atomic.Int32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		i := n.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = io.WriteString(w, fmt.Sprintf(`{"ok":true,"id":"a","rev":"%d-bb"}`, i+1))
	}
}
