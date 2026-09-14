package command

import (
	"bytes"
	"context"
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

// proxyTestEnv returns the live server URL and the shared secret it was
// configured with, or skips. Both are needed: a stock CouchDB has no
// proxy_authentication_handler in its chain, so without a server set up for it
// the headers are ignored and the test would assert nothing. Unlike JWT there
// is no version gate — the handler exists on 3.0 as well as 3.5, and the
// digest it verifies is what the probe is for.
func proxyTestEnv(t *testing.T) (string, string) {
	t.Helper()
	base := os.Getenv("CDB_TEST_URL")
	secret := os.Getenv("CDB_TEST_PROXY_SECRET")
	if base == "" || secret == "" {
		t.Skip("set CDB_TEST_URL and CDB_TEST_PROXY_SECRET to run the proxy tests; see the Development section of the README")
	}
	return base, secret
}

// proxySession installs a Deps whose environment carries the shared secret in
// the one secret slot every kind uses, and names the kind explicitly so no
// CDB_PASSWORD in the ambient environment can change it.
func proxySession(t *testing.T, secret string) *session.Session {
	t.Helper()
	env := map[string]string{"CDB_PASSWORD": secret, "CDB_USER": "ops"}
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

func TestIntegrationProxyConnects(t *testing.T) {
	base, secret := proxyTestEnv(t)
	s := proxySession(t, secret)
	if _, err := invoke(t, Connect(), s, stripUserinfo(base), "--auth", "proxy", "--roles", "_admin"); err != nil {
		t.Fatalf("connect --auth proxy = %v", err)
	}
	info, err := s.Client.Session(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Method != "proxy" {
		t.Errorf("authenticated = %q, want proxy", info.Method)
	}
	if info.Name != "ops" {
		t.Errorf("session user = %q, want ops", info.Name)
	}
	// Which digest the probe settled on is the server's business, not the
	// test's: 3.4 and later verify either, everything before it only SHA-1.
	// What matters is that one of them was found without the operator saying
	// which, so it is reported rather than asserted.
	t.Logf("proxy token hash: %s", s.Client.ProxyHash())
	if _, err := invoke(t, Ls(), s, "/"); err != nil {
		t.Errorf("ls / under proxy auth = %v", err)
	}
}

func TestIntegrationProxyWrongSecretIsAnAuthFailure(t *testing.T) {
	base, _ := proxyTestEnv(t)
	s := proxySession(t, "definitely-not-the-secret")
	_, err := invoke(t, Connect(), s, stripUserinfo(base), "--auth", "proxy")
	if err == nil {
		t.Fatal("a wrong shared secret connected")
	}
	ce, ok := couch.AsError(err)
	if !ok {
		t.Fatalf("err is %T (%v), want a *couch.Error", err, err)
	}
	if ce.Status != http.StatusUnauthorized || ce.Auth != couch.AuthProxy {
		t.Fatalf("status %d auth %q, want 401 and proxy", ce.Status, ce.Auth)
	}
}

// stripUserinfo drops any admin:password the README's CDB_TEST_URL carries.
// Proxy authentication is the credential under test; userinfo on the URL would
// authenticate through net/http and mask a proxy failure entirely.
func stripUserinfo(raw string) string {
	clean, _, _ := splitURLCredentials(raw)
	return clean
}

// A same-server replication under proxy authentication: the credential the
// server uses is the one cdb wrote into the document, so this is the only
// check that proves the headers survive the round trip through _replicator.
func TestIntegrationProxyReplicates(t *testing.T) {
	base, secret := proxyTestEnv(t)
	repl := os.Getenv("CDB_TEST_REPLICATION_URL")
	if repl == "" {
		t.Skip("set CDB_TEST_REPLICATION_URL to run the proxy replication test")
	}
	s := proxySession(t, secret)
	s.Prefs.ReplicationURL = repl
	s.Prefs.Yes = true
	if _, err := invoke(t, Connect(), s, stripUserinfo(base), "--auth", "proxy", "--roles", "_admin"); err != nil {
		t.Fatalf("connect --auth proxy = %v", err)
	}
	ctx := context.Background()
	src := "cdbproxysrc" + strconv.FormatInt(time.Now().UnixNano(), 36)
	dst := "cdbproxydst" + strconv.FormatInt(time.Now().UnixNano(), 36)
	for _, db := range []string{src, dst} {
		if err := s.Client.CreateDatabase(ctx, db, false, 0); err != nil {
			t.Fatalf("create %s: %v", db, err)
		}
		defer func(db string) { _ = s.Client.DestroyDatabase(context.Background(), db) }(db)
	}
	docFile := filepath.Join(t.TempDir(), "doc.json")
	if err := os.WriteFile(docFile, []byte(`{"hello":"proxy"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := invoke(t, Put(), s, "/"+src+"/one", docFile); err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, err := invoke(t, Replicate(), s, "/"+src, "/"+dst, "--yes"); err != nil {
		t.Fatalf("replicate under proxy auth: %v", err)
	}
	// Poll rather than sleep: a one-document job finishes in well under a
	// second, and a fixed sleep is either slower than it needs to be or flaky.
	deadline := time.Now().Add(30 * time.Second)
	for {
		info, err := s.Client.DatabaseInfo(ctx, dst)
		if err == nil && info.DocCount > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the document never arrived in %s (last err %v)", dst, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
