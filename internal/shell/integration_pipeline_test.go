package shell

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sriannamalai/CDB.CLI/internal/command"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// liveShell connects a shell to CDB_TEST_URL, or skips.
func liveShell(t *testing.T) (*Shell, *bytes.Buffer) {
	t.Helper()
	base := os.Getenv("CDB_TEST_URL")
	if base == "" {
		t.Skip("set CDB_TEST_URL (e.g. http://localhost:15984/) to run integration tests")
	}
	t.Setenv("CDB_USER", liveEnv("CDB_TEST_USER", "admin"))
	t.Setenv("CDB_PASSWORD", liveEnv("CDB_TEST_PASSWORD", "password"))
	out := &bytes.Buffer{}
	s := session.New(strings.NewReader(""), out, out)
	s.Prefs.Yes = true
	if err := command.Open(context.Background(), s, base); err != nil {
		t.Fatalf("connect to %s = %v", base, err)
	}
	t.Cleanup(func() { _ = s.Detach() })
	sh, err := New(command.Default(), s, Config{})
	if err != nil {
		t.Fatal(err)
	}
	return sh, out
}

func liveEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// putDocFile writes doc to a temp file and returns its path: put outside a
// pipeline always reads a file (or "-"/stdin), never an inline argument.
func putDocFile(t *testing.T, doc string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "doc.json")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// liveDatabase makes a database with three documents and removes it at the end.
func liveDatabase(t *testing.T, sh *Shell) string {
	t.Helper()
	name := fmt.Sprintf("t9-pipe-%d", time.Now().UnixNano())
	ctx := context.Background()
	if err := sh.RunLine(ctx, "mkdir /"+name); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { _ = sh.RunLine(context.Background(), "rmdir /"+name+" --yes") })
	for i, year := range []int{1999, 2001, 2003} {
		doc := fmt.Sprintf(`{"_id":"m%d","title":"film %d","year":%d}`, i, i, year)
		path := putDocFile(t, doc)
		if err := sh.RunLine(ctx, fmt.Sprintf("put /%s/m%d %s", name, i, path)); err != nil {
			t.Fatalf("put m%d: %v", i, err)
		}
	}
	return name
}

func TestLiveFindIntoPut(t *testing.T) {
	sh, out := liveShell(t)
	src := liveDatabase(t, sh)
	dst := src + "-copy"
	ctx := context.Background()
	if err := sh.RunLine(ctx, "mkdir /"+dst); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sh.RunLine(context.Background(), "rmdir /"+dst+" --yes") })
	out.Reset()
	line := fmt.Sprintf(`find /%s '{"year":{"$gt":2000}}' | del(._rev) | put /%s`, src, dst)
	if err := sh.RunLine(ctx, line); err != nil {
		t.Fatalf("%s: %v", line, err)
	}
	if n := strings.Count(out.String(), "ok"); n != 2 {
		t.Errorf("output = %q; want two written documents", out.String())
	}
}

func TestLiveLsCatSelectPut(t *testing.T) {
	sh, out := liveShell(t)
	src := liveDatabase(t, sh)
	dst := src + "-old"
	ctx := context.Background()
	if err := sh.RunLine(ctx, "mkdir /"+dst); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sh.RunLine(context.Background(), "rmdir /"+dst+" --yes") })
	out.Reset()
	line := fmt.Sprintf(`ls /%s | cat /%s | select(.year < 2002) | del(._rev) | put /%s`, src, src, dst)
	if err := sh.RunLine(ctx, line); err != nil {
		t.Fatalf("%s: %v", line, err)
	}
	if n := strings.Count(out.String(), "ok"); n != 2 {
		t.Errorf("output = %q; want the two documents before 2002", out.String())
	}
}

func TestLiveLsIntoRm(t *testing.T) {
	sh, out := liveShell(t)
	db := liveDatabase(t, sh)
	ctx := context.Background()
	out.Reset()
	if err := sh.RunLine(ctx, fmt.Sprintf("ls /%s | rm /%s --yes", db, db)); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(out.String(), "ok"); n != 3 {
		t.Errorf("output = %q; want three deletions", out.String())
	}
	out.Reset()
	if err := sh.RunLine(ctx, fmt.Sprintf("ls /%s --json", db)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), `"m0"`) {
		t.Errorf("documents survived the pipeline: %q", out.String())
	}
}

func TestLiveTailFollowIntoPut(t *testing.T) {
	sh, out := liveShell(t)
	src := liveDatabase(t, sh)
	dst := src + "-audit"
	if err := sh.RunLine(context.Background(), "mkdir /"+dst); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sh.RunLine(context.Background(), "rmdir /"+dst+" --yes") })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	out.Reset()
	go func() {
		done <- sh.RunLine(ctx, fmt.Sprintf(
			`tail /%s --follow --include-docs | .doc | del(._rev) | put /%s`, src, dst))
	}()
	// Give the feed a moment to be listening, then make exactly one change.
	time.Sleep(time.Second)
	writer, _ := liveShell(t)
	path := putDocFile(t, `{"_id":"m3","title":"new","year":2010}`)
	if err := writer.RunLine(context.Background(), fmt.Sprintf(`put /%s/m3 %s`, src, path)); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(20 * time.Second)
	for !strings.Contains(out.String(), "m3") {
		select {
		case err := <-done:
			t.Fatalf("the pipeline ended before the change arrived: %v; output %q", err, out.String())
		case <-deadline:
			t.Fatalf("no change reached put within 20s; output %q", out.String())
		case <-time.After(200 * time.Millisecond):
		}
	}
	cancel()
	<-done
}

func TestLiveScriptCapturesARevisionAndUsesIt(t *testing.T) {
	sh, out := liveShell(t)
	db := liveDatabase(t, sh)
	script := fmt.Sprintf(`# capture a revision and delete with it
set rev = cat /%s/m0 | ._rev
rm /%s/m0 --rev $rev --yes
`, db, db)
	path := filepath.Join(t.TempDir(), "job.cdb")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := sh.RunScript(context.Background(), f, "job.cdb", nil, true); err != nil {
		t.Fatalf("script: %v; output %q", err, out.String())
	}
	out.Reset()
	if err := sh.RunLine(context.Background(), fmt.Sprintf("ls /%s --json", db)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), `"m0"`) {
		t.Errorf("m0 survived: %q", out.String())
	}
}
