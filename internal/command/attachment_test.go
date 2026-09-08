package command

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
)

func TestAttachUploadsAFile(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("HEAD", "/mydb/doc1", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `"1-a"`)
		w.WriteHeader(200)
	})
	srv.JSON("PUT", "/mydb/doc1/photo.jpg", 201, `{"ok":true,"id":"doc1","rev":"2-b"}`)
	dir := t.TempDir()
	file := filepath.Join(dir, "photo.jpg")
	if err := os.WriteFile(file, []byte("JPEGDATA"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := connected(t, srv)
	s.SetPath("/mydb")
	res, err := invoke(t, Attach(), s, "doc1", file)
	if err != nil {
		t.Fatal(err)
	}
	msg, ok := res.(Message)
	if !ok || !strings.Contains(msg.Text, "2-b") {
		t.Errorf("result = %#v", res)
	}
	req := srv.Last("PUT", "/mydb/doc1/photo.jpg")
	if string(req.Body) != "JPEGDATA" {
		t.Error("attach did not send the file body")
	}
	if req.Query("rev") != "1-a" {
		t.Errorf("rev query = %q, want the document's current revision", req.Query("rev"))
	}
	if ct := req.Header.Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("Content-Type = %q, want it guessed from the extension", ct)
	}
}

func TestAttachHonoursName(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("HEAD", "/mydb/doc1", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `"1-a"`)
		w.WriteHeader(200)
	})
	srv.JSON("PUT", "/mydb/doc1/renamed.bin", 201, `{"ok":true,"id":"doc1","rev":"2-b"}`)
	file := filepath.Join(t.TempDir(), "photo.jpg")
	_ = os.WriteFile(file, []byte("x"), 0o600)
	s := connected(t, srv)
	s.SetPath("/mydb")
	if _, err := invoke(t, Attach(), s, "doc1", file, "--name", "renamed.bin"); err != nil {
		t.Fatal(err)
	}
	if srv.Last("PUT", "/mydb/doc1/renamed.bin") == nil {
		t.Error("attach ignored --name")
	}
}

func TestAttachHonoursContentType(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("HEAD", "/mydb/doc1", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `"1-a"`)
		w.WriteHeader(200)
	})
	srv.JSON("PUT", "/mydb/doc1/photo.jpg", 201, `{"ok":true,"id":"doc1","rev":"2-b"}`)
	file := filepath.Join(t.TempDir(), "photo.jpg")
	_ = os.WriteFile(file, []byte("x"), 0o600)
	s := connected(t, srv)
	s.SetPath("/mydb")
	if _, err := invoke(t, Attach(), s, "doc1", file, "--content-type", "application/x-thing"); err != nil {
		t.Fatal(err)
	}
	if ct := srv.Last("PUT", "/mydb/doc1/photo.jpg").Header.Get("Content-Type"); ct != "application/x-thing" {
		t.Errorf("Content-Type = %q, want the flag's value", ct)
	}
}

func TestAttachRejectsANonDocumentPath(t *testing.T) {
	srv := couchtest.New(t)
	file := filepath.Join(t.TempDir(), "photo.jpg")
	_ = os.WriteFile(file, []byte("x"), 0o600)
	s := connected(t, srv)
	if _, err := invoke(t, Attach(), s, "/mydb", file); err == nil {
		t.Fatal("attach accepted a database path")
	}
}

func TestFetchWritesToAFile(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("GET", "/mydb/doc1/photo.jpg", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "JPEGDATA")
	})
	dir := t.TempDir()
	out := filepath.Join(dir, "downloaded.jpg")
	s := connected(t, srv)
	if _, err := invoke(t, Fetch(), s, "/mydb/doc1/photo.jpg", out); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "JPEGDATA" {
		t.Errorf("file = %q", b)
	}
}

// leftovers lists the part files fetch has left behind in dir.
func leftovers(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "*.part"))
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

func TestFetchLeavesNoTempFileOnSuccess(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("GET", "/mydb/doc1/photo.jpg", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "JPEGDATA")
	})
	dir := t.TempDir()
	out := filepath.Join(dir, "downloaded.jpg")
	s := connected(t, srv)
	if _, err := invoke(t, Fetch(), s, "/mydb/doc1/photo.jpg", out); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(out); err != nil || string(b) != "JPEGDATA" {
		t.Fatalf("file = %q, err = %v", b, err)
	}
	if left := leftovers(t, dir); len(left) != 0 {
		t.Errorf("fetch left %v behind", left)
	}
}

func TestFetchKeepsTheExistingFileWhenTheDownloadFails(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("GET", "/mydb/doc1/photo.jpg", func(w http.ResponseWriter, _ *http.Request) {
		// Promise more bytes than the handler writes. net/http closes the
		// connection when the handler returns short, so the client sees the
		// stream break part way through the body, exactly as a dropped network
		// connection would.
		w.Header().Set("Content-Length", "4096")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, strings.Repeat("x", 16))
	})
	dir := t.TempDir()
	out := filepath.Join(dir, "downloaded.jpg")
	if err := os.WriteFile(out, []byte("OLD"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := connected(t, srv)
	if _, err := invoke(t, Fetch(), s, "/mydb/doc1/photo.jpg", out, "--force"); err == nil {
		t.Fatal("fetch reported success on a truncated download")
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("fetch destroyed the existing file: %v", err)
	}
	if string(b) != "OLD" {
		t.Errorf("file = %q, want the previous contents left intact", b)
	}
	if left := leftovers(t, dir); len(left) != 0 {
		t.Errorf("fetch left %v behind", left)
	}
}

func TestFetchRefusesToOverwriteWithoutForce(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("GET", "/mydb/doc1/photo.jpg", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "NEW")
	})
	out := filepath.Join(t.TempDir(), "downloaded.jpg")
	if err := os.WriteFile(out, []byte("OLD"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := connected(t, srv)
	if _, err := invoke(t, Fetch(), s, "/mydb/doc1/photo.jpg", out); err == nil {
		t.Fatal("fetch overwrote an existing file without --force")
	}
	if b, _ := os.ReadFile(out); string(b) != "OLD" {
		t.Errorf("file = %q, want it left alone", b)
	}
	if _, err := invoke(t, Fetch(), s, "/mydb/doc1/photo.jpg", out, "--force"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(out); string(b) != "NEW" {
		t.Errorf("file = %q, want it overwritten with --force", b)
	}
}

func TestFetchToStdoutReturnsRaw(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("GET", "/mydb/doc1/photo.jpg", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "JPEGDATA")
	})
	s := connected(t, srv)
	res, err := invoke(t, Fetch(), s, "/mydb/doc1/photo.jpg", "-")
	if err != nil {
		t.Fatal(err)
	}
	raw, ok := res.(Raw)
	if !ok {
		t.Fatalf("result is %T, want Raw", res)
	}
	b, _ := io.ReadAll(raw.Reader)
	if string(b) != "JPEGDATA" {
		t.Errorf("raw = %q", b)
	}
}

func TestFetchRejectsADocumentPath(t *testing.T) {
	srv := couchtest.New(t)
	s := connected(t, srv)
	if _, err := invoke(t, Fetch(), s, "/mydb/doc1", "-"); err == nil {
		t.Fatal("fetch accepted a document path")
	}
}

func TestCatOnAnAttachmentStreamsItRaw(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("GET", "/mydb/doc1/notes.txt", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "hello")
	})
	s := connected(t, srv)
	res, err := invoke(t, Cat(), s, "/mydb/doc1/notes.txt")
	if err != nil {
		t.Fatal(err)
	}
	raw, ok := res.(Raw)
	if !ok {
		t.Fatalf("result is %T, want Raw", res)
	}
	b, _ := io.ReadAll(raw.Reader)
	if string(b) != "hello" {
		t.Errorf("raw = %q", b)
	}
}

func TestRmDeletesAnAttachment(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("HEAD", "/mydb/doc1", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `"2-b"`)
		w.WriteHeader(200)
	})
	srv.JSON("DELETE", "/mydb/doc1/photo.jpg", 200, `{"ok":true,"id":"doc1","rev":"3-c"}`)
	s := connected(t, srv)
	if _, err := invoke(t, Rm(), s, "/mydb/doc1/photo.jpg", "--yes"); err != nil {
		t.Fatal(err)
	}
	req := srv.Last("DELETE", "/mydb/doc1/photo.jpg")
	if req == nil {
		t.Fatal("rm did not delete the attachment")
	}
	if req.Query("rev") != "2-b" {
		t.Errorf("rev query = %q, want the document's current revision", req.Query("rev"))
	}
	if srv.Last("DELETE", "/mydb/doc1") != nil {
		t.Error("rm deleted the document as well as the attachment")
	}
}

func TestRmConfirmsBeforeDeletingAnAttachment(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("HEAD", "/mydb/doc1", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `"2-b"`)
		w.WriteHeader(200)
	})
	srv.JSON("DELETE", "/mydb/doc1/photo.jpg", 200, `{"ok":true,"id":"doc1","rev":"3-c"}`)
	s := connected(t, srv)
	s.Prefs.Interactive = true
	s.SetStdin(strings.NewReader("n\n"))
	if _, err := invoke(t, Rm(), s, "/mydb/doc1/photo.jpg"); err == nil {
		t.Fatal("rm deleted the attachment without a confirmation")
	}
	if srv.Last("DELETE", "/mydb/doc1/photo.jpg") != nil {
		t.Error("rm deleted the attachment after the operator declined")
	}
}
