package command

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/config"
	"github.com/sriannamalai/CDB.CLI/internal/couch"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// cloudantTestEnv returns the Cloudant URL and the IAM API key, or skips.
// These never appear in CI: they are read from the operator's own environment,
// which the README's Development section says to populate with the instance
// host and a service API key:
//
//	export CDB_TEST_CLOUDANT_URL="https://<instance>.cloudantnosqldb.appdomain.cloud"
//	export CDB_TEST_CLOUDANT_API_KEY=...
//
// Neither value is ever logged, echoed or written to a file, and the export
// itself belongs in a shell that reads them from wherever they are kept.
func cloudantTestEnv(t *testing.T) (string, string) {
	t.Helper()
	url := os.Getenv("CDB_TEST_CLOUDANT_URL")
	key := os.Getenv("CDB_TEST_CLOUDANT_API_KEY")
	if url == "" || key == "" {
		t.Skip("set CDB_TEST_CLOUDANT_URL and CDB_TEST_CLOUDANT_API_KEY to run the Cloudant tests; see the Development section of the README")
	}
	return url, key
}

// iamSession installs a Deps whose environment carries the API key, which is
// how Env.Apply selects auth = "iam" and Env.Secret hands the credential over.
func iamSession(t *testing.T, apiKey string) *session.Session {
	t.Helper()
	env := map[string]string{"CDB_IAM_API_KEY": apiKey}
	if u := os.Getenv("CDB_IAM_URL"); u != "" {
		env["CDB_IAM_URL"] = u
	}
	SetDeps(&Deps{
		ConfigPath: filepath.Join(t.TempDir(), "config.toml"),
		Secrets:    config.NewMemorySecrets(),
		LookupEnv:  func(k string) (string, bool) { v, ok := env[k]; return v, ok },
	})
	t.Cleanup(func() { SetDeps(nil) })
	s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	t.Cleanup(func() { _ = s.Detach() })
	return s
}

func TestIntegrationIAMConnects(t *testing.T) {
	url, key := cloudantTestEnv(t)
	s := iamSession(t, key)
	if err := Open(context.Background(), s, url); err != nil {
		t.Fatalf("connect with an IAM API key = %v", err)
	}
	info, err := s.Client.Session(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Method != "iam" {
		t.Errorf("authenticated = %q, want iam", info.Method)
	}
	if _, err := invoke(t, Ls(), s, "/"); err != nil {
		t.Errorf("ls / against Cloudant = %v", err)
	}
	res, err := invoke(t, SessionCmd(), s)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res.(Rows).Items {
		for _, cell := range r.Cells {
			if strings.Contains(cell, key) {
				t.Fatal("the session table leaked the API key")
			}
		}
	}
}

func TestIntegrationIAMWrongKeyIsAnAuthFailure(t *testing.T) {
	url, _ := cloudantTestEnv(t)
	s := iamSession(t, "definitely-not-a-key")
	err := Open(context.Background(), s, url)
	if err == nil {
		t.Fatal("a bogus API key connected")
	}
	ce, ok := couch.AsError(err)
	if !ok {
		t.Fatalf("err is %T (%v), want a *couch.Error", err, err)
	}
	if ce.Status != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 so the exit code is 3", ce.Status)
	}
	if ce.Name != couch.IAMExchangeFailed {
		t.Errorf("name = %q, want %q", ce.Name, couch.IAMExchangeFailed)
	}
}
