package command

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sriannamalai/CDB.CLI/internal/config"
	"github.com/sriannamalai/CDB.CLI/internal/couch"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// jwtTestEnv returns the live server URL and the HS256 secret it was
// configured with, or skips. Both are needed: CouchDB's default
// authentication_handlers has no JWT handler, so without a server that was
// deliberately set up for it a bearer token is ignored and the test would
// assert nothing.
func jwtTestEnv(t *testing.T) (string, string) {
	t.Helper()
	base := os.Getenv("CDB_TEST_URL")
	secret := os.Getenv("CDB_TEST_JWT_SECRET")
	if base == "" || secret == "" {
		t.Skip("set CDB_TEST_URL and CDB_TEST_JWT_SECRET to run the JWT test; see the Development section of the README")
	}
	// CouchDB gained jwt_authentication_handler in 3.1. On 3.0 the handler
	// cannot be put in the chain at all, so a bearer token is ignored and the
	// test would assert nothing; the CI matrix runs 3.0 with no JWT step.
	if v := serverVersion(t, base); strings.HasPrefix(v, "3.0.") || v == "3.0" {
		t.Skipf("CouchDB %s has no JWT handler; JWT needs 3.1 or later", v)
	}
	return base, secret
}

// serverVersion reads the version out of the server banner. GET / answers for
// anyone, so this works before any credential has been offered.
func serverVersion(t *testing.T, base string) string {
	t.Helper()
	res, err := http.Get(strings.TrimSuffix(base, "/") + "/")
	if err != nil {
		t.Fatalf("read the server banner at %s: %v", base, err)
	}
	defer res.Body.Close()
	var banner struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(res.Body).Decode(&banner); err != nil {
		t.Fatalf("decode the server banner: %v", err)
	}
	return banner.Version
}

// mintHS256 signs a CouchDB-shaped admin token. A JWT library is not worth a
// dependency for twenty lines: header and claims are base64url without
// padding, and the signature is HMAC-SHA256 over "header.claims".
//
// CouchDB's [jwt_auth] required_claims defaults to "exp", so the token must
// carry one; roles come from "_couchdb.roles".
func mintHS256(t *testing.T, secret string, expiry time.Time) string {
	t.Helper()
	enc := func(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
	header := enc([]byte(`{"alg":"HS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]any{
		"sub":            "admin",
		"_couchdb.roles": []string{"_admin"},
		"exp":            expiry.Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := enc(claims)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(header + "." + payload))
	return header + "." + payload + "." + enc(mac.Sum(nil))
}

// jwtSession installs a Deps whose environment carries the token, which is how
// Env.Apply selects auth = "jwt" and Env.Secret hands the credential over. The
// keyring is in memory and the config file is a temp file, so no real keychain
// is touched — CDB_KEYRING_BACKEND=file still applies in CI besides.
func jwtSession(t *testing.T, token string) *session.Session {
	t.Helper()
	env := map[string]string{"CDB_TOKEN": token, "CDB_USER": "admin"}
	SetDeps(&Deps{
		ConfigPath: filepath.Join(t.TempDir(), "config.toml"),
		Secrets:    config.NewMemorySecrets(),
		LookupEnv: func(k string) (string, bool) {
			v, ok := env[k]
			return v, ok
		},
	})
	t.Cleanup(func() { SetDeps(nil) })
	s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	t.Cleanup(func() { _ = s.Detach() })
	return s
}

func TestIntegrationJWTConnects(t *testing.T) {
	base, secret := jwtTestEnv(t)
	s := jwtSession(t, mintHS256(t, secret, time.Now().Add(5*time.Minute)))

	if err := Open(context.Background(), s, base); err != nil {
		t.Fatalf("connect with a JWT = %v", err)
	}
	info, err := s.Client.Session(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "admin" {
		t.Errorf("session user = %q, want admin", info.Name)
	}
	admin := false
	for _, r := range info.Roles {
		if r == "_admin" {
			admin = true
		}
	}
	if !admin {
		t.Errorf("session roles = %v, want _admin among them", info.Roles)
	}
	if _, err := invoke(t, Ls(), s, "/"); err != nil {
		t.Errorf("ls / with a JWT = %v", err)
	}
}

func TestIntegrationJWTExpiredTokenIsAnAuthFailure(t *testing.T) {
	base, secret := jwtTestEnv(t)
	s := jwtSession(t, mintHS256(t, secret, time.Now().Add(-time.Minute)))

	err := Open(context.Background(), s, base)
	if err == nil {
		t.Fatal("an expired token connected")
	}
	ce, ok := couch.AsError(err)
	if !ok {
		t.Fatalf("err is %T (%v), want a *couch.Error", err, err)
	}
	// 401 is what cli.ExitCode maps to exit 3 and what render.ErrorMessage
	// turns into the login-failure sentence. A 500 would be exit 1 and a
	// server-error sentence, which is the regression this pins.
	if ce.Status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; the server answered %q", ce.Status, ce.Reason)
	}
	if ce.Auth != couch.AuthJWT {
		t.Errorf("Auth = %q, want jwt so the message names the credential that failed", ce.Auth)
	}
}
