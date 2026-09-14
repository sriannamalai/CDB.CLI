package command

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

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

// The §4.3 check: a same-account replication whose credential is the auth
// object cdb wrote. It creates two databases, replicates one into the other,
// reads the job back through "replications show" — which must print neither the
// key nor a token — and deletes both databases.
func TestIntegrationIAMReplicates(t *testing.T) {
	url, key := cloudantTestEnv(t)
	s := iamSession(t, key)
	s.Prefs.Yes = true
	if err := Open(context.Background(), s, url); err != nil {
		t.Fatalf("connect: %v", err)
	}
	ctx := context.Background()
	stamp := strconv.FormatInt(time.Now().UnixNano(), 36)
	src, dst := "cdbtest-iamsrc"+stamp, "cdbtest-iamdst"+stamp
	for _, db := range []string{src, dst} {
		if err := s.Client.CreateDatabase(ctx, db, false, 0); err != nil {
			t.Fatalf("create %s: %v", db, err)
		}
		defer func(db string) { _ = s.Client.DestroyDatabase(context.Background(), db) }(db)
	}
	doc := filepath.Join(t.TempDir(), "one.json")
	if err := os.WriteFile(doc, []byte(`{"hello":"iam"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := invoke(t, Put(), s, "/"+src+"/one", doc); err != nil {
		t.Fatalf("put: %v", err)
	}
	jobID := "cdbtest-iam" + stamp
	if _, err := invoke(t, Replicate(), s, "/"+src, "/"+dst, "--id", jobID, "--yes"); err != nil {
		t.Fatalf("replicate under IAM: %v", err)
	}
	// The document that is cancelled here carries the API key, so a cleanup
	// that quietly failed would leave a credential behind in _replicator.
	// The document that is cancelled here carries the API key, so a cleanup
	// that quietly failed would leave a credential behind in _replicator. The
	// scheduler writes its own state back into that document while the job
	// runs, so a delete can lose a revision race; retrying is what makes the
	// cleanup reliable rather than usually-fine.
	defer func() {
		var cerr error
		for attempt := 0; attempt < 5; attempt++ {
			if _, cerr = invoke(t, Replications(), s, "cancel", jobID, "--yes"); cerr == nil {
				return
			}
			time.Sleep(time.Second)
		}
		t.Errorf("cancel left the replication document behind: %v", cerr)
	}()

	deadline := time.Now().Add(2 * time.Minute)
	for {
		info, err := s.Client.DatabaseInfo(ctx, dst)
		if err == nil && info.DocCount > 0 {
			break
		}
		if time.Now().After(deadline) {
			// Read the job's own error back; it is what tells the auth form
			// apart from an unrelated failure.
			res, serr := invoke(t, Replications(), s, "show", jobID)
			t.Fatalf("the document never arrived in %s (db err %v; job %v, %v)", dst, err, res, serr)
		}
		time.Sleep(time.Second)
	}

	res, err := invoke(t, Replications(), s, "show", jobID)
	if err != nil {
		t.Fatalf("replications show: %v", err)
	}
	rendered := fmt.Sprintf("%v", res)
	for _, forbidden := range []string{key, "api_key", "Bearer"} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("replications show leaked a credential")
		}
	}
}
