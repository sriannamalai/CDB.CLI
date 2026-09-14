package command

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
)

func TestRunReadsTheFileAndPassesTheArguments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "job.cdb")
	if err := os.WriteFile(path, []byte("say hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var (
		gotName string
		gotArgs []string
		gotYes  bool
		gotText string
	)
	c := RunFrom(func(_ context.Context, r io.Reader, name string, args []string, yes bool) error {
		b, err := io.ReadAll(r)
		if err != nil {
			return err
		}
		gotName, gotArgs, gotYes, gotText = name, args, yes, string(b)
		return nil
	})
	s := connected(t, couchtest.New(t))
	// --yes belongs before the file name: everything after it is the script's.
	if _, err := invoke(t, c, s, "--yes", path, "one", "two"); err != nil {
		t.Fatal(err)
	}
	if gotName != path || gotText != "say hello\n" {
		t.Errorf("name = %q, text = %q", gotName, gotText)
	}
	if len(gotArgs) != 2 || gotArgs[0] != "one" || gotArgs[1] != "two" {
		t.Errorf("args = %v", gotArgs)
	}
	if !gotYes {
		t.Error("--yes did not reach the runner")
	}
}

func TestRunHandsAFlagAfterTheFileToTheScript(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "job.cdb")
	if err := os.WriteFile(path, []byte("say hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var gotArgs []string
	c := RunFrom(func(_ context.Context, _ io.Reader, _ string, args []string, _ bool) error {
		gotArgs = args
		return nil
	})
	s := connected(t, couchtest.New(t))
	if _, err := invoke(t, c, s, path, "--limit", "5"); err != nil {
		t.Fatal(err)
	}
	if len(gotArgs) != 2 || gotArgs[0] != "--limit" || gotArgs[1] != "5" {
		t.Errorf("args = %v; a flag after the file name belongs to the script", gotArgs)
	}
}

func TestRunReportsAMissingFile(t *testing.T) {
	c := RunFrom(func(context.Context, io.Reader, string, []string, bool) error { return nil })
	s := connected(t, couchtest.New(t))
	_, err := invoke(t, c, s, filepath.Join(t.TempDir(), "nope.cdb"))
	if err == nil || !strings.Contains(err.Error(), "nope.cdb") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunWithoutARunnerSaysSo(t *testing.T) {
	s := connected(t, couchtest.New(t))
	_, err := invoke(t, Run(), s, "anything.cdb")
	var ue *UsageError
	if !errorsAs(err, &ue) {
		t.Fatalf("error = %v (%T)", err, err)
	}
}

func TestRunPassesTheScriptFailureThrough(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "job.cdb")
	if err := os.WriteFile(path, []byte("say hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	boom := Usagef("say", "no")
	c := RunFrom(func(context.Context, io.Reader, string, []string, bool) error { return boom })
	s := connected(t, couchtest.New(t))
	_, err := invoke(t, c, s, path)
	// The exit code is the failing line's, so the error must arrive unchanged.
	if !errorsIs(err, boom) {
		t.Errorf("error = %v, want the script's own failure", err)
	}
}

func errorsIs(err, target error) bool { return errors.Is(err, target) }
