package backup

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	if err := w.WriteHeader(Header{DB: "mydb", Server: "3.5.2", Started: "2026-09-08T00:00:00Z", Partitioned: true}); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteDoc(json.RawMessage(`{"_id":"doc1","_rev":"1-a","v":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteAttachment(Att{ID: "doc1", Rev: "1-a", Name: "photo.jpg", ContentType: "image/jpeg", Length: 8}, strings.NewReader("JPEGDATA")); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteCheckpoint("5-abc"); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteDoc(json.RawMessage(`{"_id":"doc2","_rev":"1-b"}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteFooter(Footer{Docs: 2, Attachments: 1, LastSeq: "9-xyz"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := NewReader(&buf)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for {
		rec, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, rec.Kind)
		switch rec.Kind {
		case KindHeader:
			if rec.Header.DB != "mydb" || !rec.Header.Partitioned {
				t.Errorf("header = %+v", rec.Header)
			}
		case KindAtt:
			b, err := io.ReadAll(rec.Content)
			if err != nil {
				t.Fatal(err)
			}
			if string(b) != "JPEGDATA" {
				t.Errorf("attachment content = %q, want %q", b, "JPEGDATA")
			}
		case KindCheckpoint:
			if rec.Checkpoint != "5-abc" {
				t.Errorf("checkpoint = %q", rec.Checkpoint)
			}
		case KindFooter:
			if rec.Footer.Docs != 2 || rec.Footer.LastSeq != "9-xyz" {
				t.Errorf("footer = %+v", rec.Footer)
			}
		}
	}
	want := []string{KindHeader, KindDoc, KindAtt, KindCheckpoint, KindDoc, KindFooter}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Fatalf("kinds = %v, want %v", kinds, want)
	}
}

func TestScanResumeAfterTruncation(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "dump.cdb.gz")
	f, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	w := NewWriter(f)
	if err := w.WriteHeader(Header{DB: "mydb", Server: "3.5.2"}); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteDoc(json.RawMessage(`{"_id":"a"}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteCheckpoint("1-aaa"); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteDoc(json.RawMessage(`{"_id":"b"}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteCheckpoint("2-bbb"); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteDoc(json.RawMessage(`{"_id":"c"}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	// Simulate an interrupt: chop 10 bytes off the trailing member.
	fi, _ := os.Stat(name)
	if err := Truncate(name, fi.Size()-10); err != nil {
		t.Fatal(err)
	}

	f, err = os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Scan(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if res.Seq != "2-bbb" {
		t.Errorf("Scan().Seq = %q, want %q", res.Seq, "2-bbb")
	}
	if res.Docs != 2 {
		t.Errorf("Scan().Docs = %d, want 2", res.Docs)
	}
	if res.Complete {
		t.Errorf("Scan().Complete = true, want false")
	}

	if err := Truncate(name, res.Offset); err != nil {
		t.Fatal(err)
	}
	f, err = os.OpenFile(name, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	w = NewWriter(f)
	if err := w.WriteDoc(json.RawMessage(`{"_id":"c"}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteFooter(Footer{Docs: 3, LastSeq: "3-ccc"}); err != nil {
		t.Fatal(err)
	}
	w.Close()
	f.Close()

	f, _ = os.Open(name)
	defer f.Close()
	r, err := NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	var footer *Footer
	for {
		rec, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		switch rec.Kind {
		case KindDoc:
			var d struct {
				ID string `json:"_id"`
			}
			_ = json.Unmarshal(rec.Doc, &d)
			ids = append(ids, d.ID)
		case KindFooter:
			footer = rec.Footer
		}
	}
	if strings.Join(ids, ",") != "a,b,c" {
		t.Fatalf("ids = %v, want [a b c]", ids)
	}
	if footer == nil || footer.LastSeq != "3-ccc" {
		t.Fatalf("footer = %+v", footer)
	}
}

func TestScanCompleteDump(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "dump.cdb.gz")
	f, _ := os.Create(name)
	w := NewWriter(f)
	_ = w.WriteHeader(Header{DB: "mydb"})
	_ = w.WriteCheckpoint("1-a")
	_ = w.WriteFooter(Footer{Docs: 0, LastSeq: "1-a"})
	_ = w.Close()
	f.Close()
	f, _ = os.Open(name)
	defer f.Close()
	res, err := Scan(f)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Complete {
		t.Fatalf("Scan().Complete = false, want true")
	}
}

// A member whose deflate stream is intact but whose gzip trailer was lost can
// decode a checkpoint record and only then fail. Scan must discard everything
// that member reported: committing its sequence would leave Seq newer than
// Offset, and --resume would restart from that sequence after truncating to the
// older offset, silently dropping every document in between.
func TestScanIgnoresACheckpointInATornMember(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "dump.cdb.gz")
	f, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	w := NewWriter(f)
	if err := w.WriteHeader(Header{DB: "mydb"}); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteDoc(json.RawMessage(`{"_id":"a"}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteCheckpoint("1-aaa"); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteDoc(json.RawMessage(`{"_id":"b"}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteCheckpoint("2-bbb"); err != nil {
		t.Fatal(err)
	}
	// WriteCheckpoint closed the member, and the fresh one has not been written
	// to, so the file ends exactly on a member boundary. Close the file without
	// closing the Writer, which would append an empty member.
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	boundary := fi.Size()

	// Build a third member and append it without its 8-byte gzip trailer.
	var mem bytes.Buffer
	w2 := NewWriter(&mem)
	if err := w2.WriteDoc(json.RawMessage(`{"_id":"c"}`)); err != nil {
		t.Fatal(err)
	}
	if err := w2.WriteCheckpoint("3-ccc"); err != nil {
		t.Fatal(err)
	}
	torn := mem.Bytes()[:mem.Len()-8]
	af, err := os.OpenFile(name, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := af.Write(torn); err != nil {
		t.Fatal(err)
	}
	af.Close()

	f, err = os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	res, err := Scan(f)
	if err != nil {
		t.Fatal(err)
	}
	if res.Seq != "2-bbb" {
		t.Errorf("Scan().Seq = %q, want %q", res.Seq, "2-bbb")
	}
	if res.Offset != boundary {
		t.Errorf("Scan().Offset = %d, want %d", res.Offset, boundary)
	}
	if res.Docs != 2 {
		t.Errorf("Scan().Docs = %d, want 2", res.Docs)
	}
	if res.Complete {
		t.Errorf("Scan().Complete = true, want false")
	}
}

func TestWriteAttachmentRejectsAShortReader(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	err := w.WriteAttachment(Att{ID: "a", Name: "x", Length: 10}, strings.NewReader("short"))
	if err == nil {
		t.Fatal("WriteAttachment accepted a reader shorter than Length")
	}
}

// os.Truncate zero-extends a file when the offset is past its end, so a stale
// resume offset would pad a dump with NUL bytes instead of failing.
func TestTruncateRefusesToGrowTheFile(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "dump.cdb.gz")
	if err := os.WriteFile(name, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Truncate(name, 11); err == nil {
		t.Fatal("Truncate past the end of the file returned nil")
	}
	if err := Truncate(name, -1); err == nil {
		t.Fatal("Truncate to a negative offset returned nil")
	}
	got, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "0123456789" {
		t.Fatalf("file = %q, want %q", got, "0123456789")
	}
	if err := Truncate(name, 4); err != nil {
		t.Fatalf("Truncate within the file: %v", err)
	}
	got, _ = os.ReadFile(name)
	if string(got) != "0123" {
		t.Fatalf("after Truncate(4) file = %q, want %q", got, "0123")
	}
}

func TestWriterLatchesAFailure(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	if err := w.WriteHeader(Header{DB: "mydb"}); err != nil {
		t.Fatal(err)
	}
	first := w.WriteAttachment(Att{ID: "a", Name: "x", Length: 10}, strings.NewReader("short"))
	if first == nil {
		t.Fatal("WriteAttachment accepted a reader shorter than Length")
	}
	before := buf.Len()

	if err := w.WriteDoc(json.RawMessage(`{"_id":"a"}`)); !errors.Is(err, first) {
		t.Errorf("WriteDoc after a failure = %v, want %v", err, first)
	}
	if err := w.WriteCheckpoint("1-a"); !errors.Is(err, first) {
		t.Errorf("WriteCheckpoint after a failure = %v, want %v", err, first)
	}
	if err := w.WriteFooter(Footer{LastSeq: "1-a"}); !errors.Is(err, first) {
		t.Errorf("WriteFooter after a failure = %v, want %v", err, first)
	}
	if err := w.Close(); !errors.Is(err, first) {
		t.Errorf("Close after a failure = %v, want %v", err, first)
	}
	if buf.Len() != before {
		t.Errorf("a failed Writer wrote %d more bytes", buf.Len()-before)
	}
}

func TestScanRejectsANonDumpGzipFile(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "other.gz")
	f, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	if _, err := gz.Write([]byte(`{"hello":"world"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	f, err = os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	res, err := Scan(f)
	if err == nil {
		t.Fatalf("Scan of a non-dump gzip file returned %+v, want an error", res)
	}
	if !strings.Contains(err.Error(), "not a cdb dump") {
		t.Errorf("Scan error = %v, want it to say the file is not a cdb dump", err)
	}
}

func TestScanOfAnEmptyFileIsAFreshStart(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "dump.cdb.gz")
	if err := os.WriteFile(name, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	res, err := Scan(f)
	if err != nil {
		t.Fatalf("Scan of an empty file: %v", err)
	}
	if res != (Resume{}) {
		t.Errorf("Scan of an empty file = %+v, want the zero Resume", res)
	}
}
