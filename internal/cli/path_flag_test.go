package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/command"
	"github.com/sriannamalai/CDB.CLI/internal/config"
	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// TestExecuteHonoursThePathFlagAfterAutoConnect guards the seam where the
// global --path flag meets lazy auto-connect: session.Attach resets the
// current path to "/", so a --path applied before the connection is silently
// discarded and "ls --path /mydb" lists the whole server instead.
func TestExecuteHonoursThePathFlagAfterAutoConnect(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb/_all_docs", 200,
		`{"total_rows":1,"offset":0,"rows":[{"id":"doc1","key":"doc1","value":{"rev":"1-abc"}}]}`)
	command.SetDeps(&command.Deps{
		ConfigPath: filepath.Join(t.TempDir(), "config.toml"),
		Secrets:    config.NewMemorySecrets(),
		LookupEnv:  func(string) (string, bool) { return "", false },
	})
	t.Cleanup(func() { command.SetDeps(nil) })

	reg := command.NewRegistry()
	reg.Register(command.Ls())
	reg.Register(command.Pwd())

	var out, errOut bytes.Buffer
	s := session.New(strings.NewReader(""), &out, &errOut)
	code := Execute(context.Background(), reg, s, BuildInfo{}, []string{"ls", "--path", "/mydb", "--url", srv.URL()})
	if code != ExitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", code, ExitOK, errOut.String())
	}
	if req := srv.Last("GET", "/mydb/_all_docs"); req == nil {
		t.Fatalf("no _all_docs request for mydb; requests: %v", requestPaths(srv))
	}
	if !strings.Contains(out.String(), "doc1") {
		t.Errorf("output does not list the database's documents: %q", out.String())
	}
	if s.Path() != "/mydb" {
		t.Errorf("session path = %q, want %q", s.Path(), "/mydb")
	}
}

func requestPaths(srv *couchtest.Server) []string {
	var paths []string
	for _, r := range srv.Requests() {
		paths = append(paths, r.Method+" "+r.Path)
	}
	return paths
}
