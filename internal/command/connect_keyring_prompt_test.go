package command

import (
	"context"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/config"
	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
)

// A proxy profile whose shared secret is not in the keyring -- a profile
// copied between machines, or one saved when the keyring was unavailable --
// has the proxy questions put to it and connects with the answers. Nothing
// else asks them: the bare-URL prompt collects a password, which proxy
// authentication has no use for.
func TestProxyProfileWithNoStoredSecretAsksAndConnects(t *testing.T) {
	srv := proxyStub(t, proxyTokenSHA256)
	cfg := config.Defaults()
	cfg.SetProfile(config.Profile{Name: "edge", URL: srv.URL(), Auth: "proxy"})
	withDeps(t, cfg, nil)
	// User name, roles, shared secret.
	s, out := guidedSession("ops\n_admin\nproxysecret\n")
	t.Cleanup(func() { _ = s.Detach() })

	if err := Open(context.Background(), s, "edge"); err != nil {
		t.Fatalf("open the proxy profile: %v (output: %s)", err, out.String())
	}
	if !s.Connected() {
		t.Fatal("the session was left unconnected")
	}
	if got := s.Client.Username(); got != "ops" {
		t.Errorf("username = %q, want the name typed at the prompt", got)
	}
	if toks := sentTokens(srv); len(toks) == 0 || toks[0] != proxyTokenSHA256 {
		t.Errorf("tokens = %v, want one signed with the typed secret", toks)
	}
	if strings.Contains(out.String(), "proxysecret") {
		t.Errorf("the shared secret was echoed:\n%s", out.String())
	}
}

// The same for IAM, whose credential is an API key: it is asked for with echo
// off and exchanged for a token, and the connection is attached.
func TestIAMProfileWithNoStoredKeyAsksAndConnects(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/", 200, `{"couchdb":"Welcome","version":"3.5.2+cloudant","vendor":{"name":"IBM Cloudant"}}`)
	srv.JSON("GET", "/_session", 200,
		`{"ok":true,"userCtx":{"name":"apikey-user","roles":["_admin"]},"info":{"authenticated":"iam"}}`)
	iam := couchtest.New(t)
	iam.JSON("POST", "/identity/token", 200, `{"access_token":"a-token","expires_in":3600,"token_type":"Bearer"}`)
	cfg := config.Defaults()
	cfg.SetProfile(config.Profile{Name: "cloudant", URL: srv.URL(), Auth: "iam"})
	withDeps(t, cfg, map[string]string{"CDB_IAM_URL": iam.URL() + "/identity/token"})
	s, out := guidedSession("iam-credential\n")
	t.Cleanup(func() { _ = s.Detach() })

	if err := Open(context.Background(), s, "cloudant"); err != nil {
		t.Fatalf("open the IAM profile: %v (output: %s)", err, out.String())
	}
	if !s.Connected() {
		t.Fatal("the session was left unconnected")
	}
	req := iam.Last("POST", "/identity/token")
	if req == nil {
		t.Fatal("no token request reached the IAM endpoint")
	}
	if !strings.Contains(string(req.Body), "iam-credential") {
		t.Errorf("token request body = %q, want the key typed at the prompt", req.Body)
	}
	if strings.Contains(out.String(), "iam-credential") {
		t.Errorf("the API key was echoed:\n%s", out.String())
	}
}
