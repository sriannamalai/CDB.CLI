package couch

import (
	"context"
	"net/http"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
)

// The token vectors below are real HMAC-SHA256 digests, computed with
//
//	printf 'ops' | openssl dgst -sha256 -hmac "proxysecret" -r
//
// so a refactor that quietly changes the hash or the message is caught by a
// value CouchDB itself would compute, not by one this package produced.
const (
	tokenOpsProxysecret   = "9fd98e6f0b43d9a40402668112d5bc133a04ffffdba3051d8cb68f1bd1945929"
	tokenAdminProxysecret = "0cb0b7c34348f55ba0fb9b9805d2120680f5c36fb95588a23d55b9010ed2f04b"
	tokenOpsS3cret        = "53ba181c9eeea06ea20b3fc1093e1583f798f8f746beae087b403f4675ed682e"

	// The SHA-1 vectors, computed the same way with -sha1. CouchDB 3.0
	// verifies HMAC-SHA1 and nothing else, so cdb has to be able to emit it.
	sha1TokenOpsProxysecret   = "ce2e3c21babe720409fce16423fb13caa1d7532f"
	sha1TokenAdminProxysecret = "f24af8c48255bc727f580cfb15eddc1ceae12608"
)

func TestProxyTokenIsHexHMACSHA256OfTheUserName(t *testing.T) {
	for _, tc := range []struct{ secret, user, want string }{
		{"proxysecret", "ops", tokenOpsProxysecret},
		{"proxysecret", "admin", tokenAdminProxysecret},
		{"s3cret", "ops", tokenOpsS3cret},
	} {
		if got := proxyToken(tc.secret, tc.user, ProxyHashSHA256); got != tc.want {
			t.Errorf("proxyToken(secret, %q) = %q, want %q", tc.user, got, tc.want)
		}
	}
}

// A server that verifies HMAC-SHA1 only — every CouchDB before 3.4 — needs the
// other digest, and the empty hash has to keep meaning SHA-256 so a Config
// written before the field existed behaves as it did.
func TestProxyTokenHonoursTheConfiguredHash(t *testing.T) {
	for _, tc := range []struct{ hash, user, want string }{
		{ProxyHashSHA1, "ops", sha1TokenOpsProxysecret},
		{ProxyHashSHA1, "admin", sha1TokenAdminProxysecret},
		{ProxyHashSHA256, "ops", tokenOpsProxysecret},
		{"", "ops", tokenOpsProxysecret},
	} {
		if got := proxyToken("proxysecret", tc.user, tc.hash); got != tc.want {
			t.Errorf("proxyToken(secret, %q, %q) = %q, want %q", tc.user, tc.hash, got, tc.want)
		}
	}
}

// The client reports the hash it settled on, because verifyLogin probes with
// one and falls back to the other and the winner has to be saved on the
// profile and reused for replication endpoints.
func TestProxyClientReportsAndUsesItsHash(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/", 200, `{"couchdb":"Welcome","version":"3.0.1"}`)
	c, err := New(Config{URL: srv.URL(), Auth: AuthProxy, Username: "ops", Secret: "proxysecret", ProxyHash: ProxyHashSHA1})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if got := c.ProxyHash(); got != ProxyHashSHA1 {
		t.Errorf("ProxyHash() = %q, want %q", got, ProxyHashSHA1)
	}
	if _, err := c.ServerInfo(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := srv.Last("GET", "/").Header.Get("X-Auth-CouchDB-Token"); got != sha1TokenOpsProxysecret {
		t.Errorf("token = %q, want the SHA-1 one", got)
	}
}

// A hash cdb cannot compute is a typo in a hand-edited profile, and building a
// client that silently used the default would authenticate as nobody on the
// one server the operator pinned it for.
func TestProxyRejectsAnUnknownHash(t *testing.T) {
	if _, err := New(Config{URL: "http://localhost:5984", Auth: AuthProxy, Username: "ops", Secret: "s", ProxyHash: "md5"}); err == nil {
		t.Fatal("an unknown proxy hash built a client")
	}
}

func TestProxyClientSendsTheThreeHeadersOnEveryRequest(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb", 200, `{"db_name":"mydb","doc_count":1,"sizes":{"file":1,"external":1},"cluster":{"q":1,"n":1},"props":{}}`)
	c, err := New(Config{URL: srv.URL(), Auth: AuthProxy, Username: "ops", Secret: "proxysecret", Roles: []string{"_admin", "editor"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ServerInfo(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DatabaseInfo(context.Background(), "mydb"); err != nil {
		t.Fatal(err)
	}
	reqs := srv.Requests()
	if len(reqs) != 2 {
		t.Fatalf("got %d requests, want 2", len(reqs))
	}
	for _, r := range reqs {
		if got := r.Header.Get("X-Auth-CouchDB-UserName"); got != "ops" {
			t.Errorf("%s %s: user name header = %q", r.Method, r.Path, got)
		}
		if got := r.Header.Get("X-Auth-CouchDB-Roles"); got != "_admin,editor" {
			t.Errorf("%s %s: roles header = %q", r.Method, r.Path, got)
		}
		if got := r.Header.Get("X-Auth-CouchDB-Token"); got != tokenOpsProxysecret {
			t.Errorf("%s %s: token header = %q", r.Method, r.Path, got)
		}
	}
}

// An empty roles list means "send no roles header at all". CouchDB reads an
// empty X-Auth-CouchDB-Roles as the single empty role rather than as none, and
// a user carrying "" as a role is not the user the operator described.
func TestProxyClientOmitsTheRolesHeaderWhenThereAreNoRoles(t *testing.T) {
	srv := couchtest.New(t)
	c, err := New(Config{URL: srv.URL(), Auth: AuthProxy, Username: "ops", Secret: "proxysecret"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ServerInfo(context.Background()); err != nil {
		t.Fatal(err)
	}
	r := srv.Requests()[0]
	if _, ok := r.Header["X-Auth-Couchdb-Roles"]; ok {
		t.Errorf("the roles header was sent with no roles: %q", r.Header.Get("X-Auth-CouchDB-Roles"))
	}
}

// A 401 under proxy auth has to carry the kind, so internal/render can say the
// sentence spec §7 names instead of "check the password".
func TestProxy401CarriesTheAuthKind(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb", http.StatusUnauthorized, `{"error":"unauthorized","reason":"Authentication required."}`)
	c, err := New(Config{URL: srv.URL(), Auth: AuthProxy, Username: "ops", Secret: "proxysecret"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.DatabaseInfo(context.Background(), "mydb")
	ce, ok := AsError(err)
	if !ok {
		t.Fatalf("err is %T (%v), want a *couch.Error", err, err)
	}
	if ce.Status != http.StatusUnauthorized || ce.Auth != AuthProxy {
		t.Errorf("status %d auth %q, want 401 and %q", ce.Status, ce.Auth, AuthProxy)
	}
}
