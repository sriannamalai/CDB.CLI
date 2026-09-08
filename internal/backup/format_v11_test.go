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

// writeDump builds a dump file and returns its path.
func writeDump(t *testing.T, name string, build func(*Writer)) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	w := NewWriter(f)
	build(w)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

// gunzip decompresses a whole dump, for the tests that assert on the bytes a
// record was written as rather than on the decoded value.
func gunzip(t *testing.T, b []byte) string {
	t.Helper()
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

const tombstoneDoc = `{"_id":"gone","_rev":"3-c0ffee","_deleted":true,"_revisions":{"start":3,"ids":["c0ffee","dead","beef"]}}`

func TestHeaderCarriesVersionAndTombstones(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	if err := w.WriteHeader(Header{Version: FormatVersion, DB: "mydb", Server: "3.5.2", Started: "2026-09-08T10:00:00Z", Tombstones: true}); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteFooter(Footer{Docs: 2, Deleted: 1, Attachments: 0, LastSeq: "9-z"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := NewReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	rec, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if rec.Header.Version != "1.1" || !rec.Header.Tombstones {
		t.Fatalf("header = %+v", rec.Header)
	}
	rec, err = r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if rec.Footer.Deleted != 1 {
		t.Fatalf("footer = %+v", rec.Footer)
	}
}

func TestFooterOmitsDeletedWhenZero(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	_ = w.WriteHeader(Header{Version: FormatVersion, DB: "mydb"})
	_ = w.WriteFooter(Footer{Docs: 1, LastSeq: "1-a"})
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	gz := gunzip(t, buf.Bytes())
	if strings.Contains(gz, `"deleted"`) {
		t.Errorf("footer wrote a zero deleted count: %s", gz)
	}
}

// Spec section 13 asks internal/backup for a round-trip of a dump containing
// tombstones: the deletion has to survive Writer and Reader byte for byte,
// because it is the _revisions in it that make the deletion replicate onward.
func TestRoundTripsATombstoneDoc(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	_ = w.WriteHeader(Header{Version: FormatVersion, DB: "mydb", Tombstones: true})
	_ = w.WriteDoc(json.RawMessage(tombstoneDoc))
	_ = w.WriteFooter(Footer{Docs: 1, Deleted: 1, LastSeq: "1-a"})
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := NewReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Next(); err != nil { // the header
		t.Fatal(err)
	}
	rec, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if rec.Kind != KindDoc {
		t.Fatalf("second record is %q, want a doc", rec.Kind)
	}
	if string(rec.Doc) != tombstoneDoc {
		t.Errorf("doc = %s, want it verbatim", rec.Doc)
	}
	if !IsTombstone(rec.Doc) {
		t.Error("the round-tripped doc is not a tombstone")
	}
}

func TestReaderAcceptsAOneZeroDump(t *testing.T) {
	// A 1.0 header has no "version" field at all.
	var buf bytes.Buffer
	w := NewWriter(&buf)
	_ = w.WriteHeader(Header{DB: "mydb", Server: "3.5.2"})
	_ = w.WriteDoc(json.RawMessage(`{"_id":"a","_rev":"1-a"}`))
	_ = w.WriteFooter(Footer{Docs: 1, LastSeq: "1-a"})
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := NewReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	rec, err := r.Next()
	if err != nil {
		t.Fatalf("Next = %v, want a 1.0 header to be accepted", err)
	}
	if rec.Header.Version != "" {
		t.Errorf("version = %q, want empty", rec.Header.Version)
	}
}

func TestReaderRefusesANewerVersion(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	_ = w.WriteHeader(Header{Version: "1.2", DB: "mydb"})
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := NewReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Next()
	var ve *VersionError
	if !errors.As(err, &ve) || ve.Version != "1.2" {
		t.Fatalf("Next = %v, want a VersionError naming 1.2", err)
	}
}

func TestScanReportsTombstonesAndDeleted(t *testing.T) {
	p := writeDump(t, "tomb.cdb.gz", func(w *Writer) {
		_ = w.WriteHeader(Header{Version: FormatVersion, DB: "mydb", Tombstones: true})
		_ = w.WriteDoc(json.RawMessage(`{"_id":"a","_rev":"1-a"}`))
		_ = w.WriteDoc(json.RawMessage(tombstoneDoc))
		_ = w.WriteCheckpoint("2-b")
		_ = w.WriteFooter(Footer{Docs: 2, Deleted: 1, LastSeq: "2-b"})
	})
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	res, err := Scan(f)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Tombstones {
		t.Error("Scan did not report the tombstones header flag")
	}
	if res.Docs != 2 || res.Deleted != 1 {
		t.Errorf("Scan = %+v, want Docs 2 and Deleted 1", res)
	}
	if !res.Complete {
		t.Error("Scan did not see the footer")
	}
}

func TestScanRefusesANewerVersion(t *testing.T) {
	p := writeDump(t, "future.cdb.gz", func(w *Writer) {
		_ = w.WriteHeader(Header{Version: "1.2", DB: "mydb"})
		_ = w.WriteCheckpoint("1-a")
	})
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, err = Scan(f)
	var ve *VersionError
	if !errors.As(err, &ve) || ve.Version != "1.2" {
		t.Fatalf("Scan = %v, want a VersionError naming 1.2", err)
	}
}

func TestIsTombstone(t *testing.T) {
	for _, tc := range []struct {
		doc  string
		want bool
	}{
		{tombstoneDoc, true},
		{`{"_id":"a","_rev":"1-a"}`, false},
		{`{"_id":"a","_deleted":false}`, false},
		{`not json`, false},
	} {
		if got := IsTombstone(json.RawMessage(tc.doc)); got != tc.want {
			t.Errorf("IsTombstone(%s) = %v, want %v", tc.doc, got, tc.want)
		}
	}
}
