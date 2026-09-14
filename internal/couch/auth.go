package couch

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"sync"
)

// sessionTransport keeps a CouchDB `_session` cookie fresh. It logs in lazily
// before the first request and, on a 401, logs in once more and replays the
// request.
type sessionTransport struct {
	base     http.RoundTripper
	jar      http.CookieJar
	baseURL  string
	username string
	password string

	mu     sync.Mutex
	authed bool
}

// login performs POST /_session. It runs under the caller's context, so a
// cancelled or timed-out request does not leave a login blocking on the network.
func (t *sessionTransport) login(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.authed {
		// Another goroutine logged in while this one waited for the lock. Without
		// this check every request made on a cold client sends its own
		// POST /_session.
		return nil
	}
	body, err := json.Marshal(map[string]string{"name": t.username, "password": t.password})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+"/_session", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := (&http.Client{Transport: t.base, Jar: t.jar}).Do(req)
	if err != nil {
		return Wrap(err, "authenticate", "")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return loginError(res, unauthorizedTarget(t.username, hostOf(t.baseURL)))
	}
	t.authed = true
	return nil
}

// loginError turns a failed POST /_session into an *Error. The server's own
// error and reason are used when it sent them; the credentials message is only
// claimed for a 401, since any other status means something else went wrong.
func loginError(res *http.Response, target string) error {
	var body struct {
		Error  string `json:"error"`
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(res.Body).Decode(&body)
	name, reason := body.Error, body.Reason
	if name == "" {
		name = nameForStatus(res.StatusCode)
	}
	if reason == "" {
		if res.StatusCode == http.StatusUnauthorized {
			reason = "Name or password is incorrect."
		} else {
			reason = strings.ToLower(http.StatusText(res.StatusCode))
		}
	}
	return NewError(res.StatusCode, name, reason, "authenticate", target)
}

// hostOf returns the host:port of a base URL, or the URL itself if it cannot be
// parsed.
func hostOf(base string) string {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return base
	}
	return u.Host
}

func (t *sessionTransport) authenticated() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.authed
}

func (t *sessionTransport) invalidate() {
	t.mu.Lock()
	t.authed = false
	t.mu.Unlock()
}

func (t *sessionTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	if !t.authenticated() {
		if err := t.login(ctx); err != nil {
			return nil, err
		}
	}
	res, err := t.base.RoundTrip(withCookies(req, t.jar))
	if err != nil || res.StatusCode != http.StatusUnauthorized {
		return res, err
	}

	// Decide replayability before touching the session. The first attempt
	// consumed req.Body, so the replay needs a fresh one. Relying on net/http to
	// rewind is not enough: it only does so for its own internal connection
	// retries, and a body that cannot be rewound would fail with
	// "ContentLength=N with Body length 0" instead of surfacing the 401.
	var body io.ReadCloser
	if req.Body != nil && req.Body != http.NoBody {
		if req.GetBody == nil {
			// A streamed body (a large attachment upload) cannot be replayed.
			// Hand the caller the 401 rather than a confusing transport error.
			return res, nil
		}
		body, err = req.GetBody()
		if err != nil {
			return res, nil
		}
	}

	res.Body.Close()
	t.invalidate()
	if err := t.login(ctx); err != nil {
		if body != nil {
			body.Close()
		}
		return nil, err
	}

	// Build the retry only now. withCookies copies the jar's cookies into the
	// request at call time, so doing it any earlier would replay the stale
	// cookie that just drew the 401 instead of the one login stored.
	retry := withCookies(req, t.jar)
	if body != nil {
		retry.Body = body
	}
	return t.base.RoundTrip(retry)
}

func withCookies(req *http.Request, jar http.CookieJar) *http.Request {
	out := req.Clone(req.Context())
	out.Header.Del("Cookie")
	for _, c := range jar.Cookies(req.URL) {
		out.AddCookie(c)
	}
	return out
}

// CloseIdleConnections lets http.Client.CloseIdleConnections reach the pool the
// base transport owns. net/http only calls it on a transport that has the
// method, so without it Client.Close was a no-op for every authenticated
// client — which is every real one — and the sockets stayed open.
func (t *sessionTransport) CloseIdleConnections() { closeIdle(t.base) }

// jwtTransport adds a bearer token to every request.
type jwtTransport struct {
	base  http.RoundTripper
	token string
}

func (t *jwtTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.base.RoundTrip(withBearer(req, t.token))
}

// withBearer clones req with an Authorization bearer header. Cloning rather
// than mutating matters: net/http may hand the same *http.Request to a
// transport more than once, and a retry has to carry the new token rather than
// the one that just drew a 401.
func withBearer(req *http.Request, token string) *http.Request {
	out := req.Clone(req.Context())
	out.Header.Set("Authorization", "Bearer "+token)
	return out
}

// CloseIdleConnections delegates to the base transport; see the session
// transport's method for why it has to exist.
func (t *jwtTransport) CloseIdleConnections() { closeIdle(t.base) }

// proxyTransport presents cdb as the trusted proxy CouchDB's
// proxy_authentication_handler expects. The token proves knowledge of the
// server's shared secret; it is computed once, at client construction, because
// neither the secret, the user name, nor the digest changes for the life of a
// client — internal/command's verifyLogin probes the digest by building a
// second client, not by rewriting this one.
//
// There is nothing to refresh and nothing to retry: a token the server will not
// accept does not produce a 401, it produces an anonymous session (verified on
// 3.0.1 and 3.5.2), which is why internal/command's verifyLogin checks
// GET /_session rather than trusting a 200.
type proxyTransport struct {
	base     http.RoundTripper
	username string
	// roles is the comma-joined role list, or "" for none.
	roles string
	token string
}

func (t *proxyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	out := req.Clone(req.Context())
	out.Header.Set("X-Auth-CouchDB-UserName", t.username)
	out.Header.Set("X-Auth-CouchDB-Token", t.token)
	if t.roles != "" {
		out.Header.Set("X-Auth-CouchDB-Roles", t.roles)
	}
	return t.base.RoundTrip(out)
}

// CloseIdleConnections delegates to the base transport; see sessionTransport's
// method for why it has to exist.
func (t *proxyTransport) CloseIdleConnections() { closeIdle(t.base) }

// proxyToken is the X-Auth-CouchDB-Token value: lowercase hex HMAC of the user
// name, keyed by the shared secret, under the digest hash names. An empty hash
// means the default, SHA-256.
//
// Which digest the server verifies is a version question, not a preference.
// CouchDB 3.3.2 added [chttpd_auth] hash_algorithms, whose default
// "sha256, sha" accepts either; 3.0 through 3.3.1 verify HMAC-SHA1 and nothing
// else
// (checked live against 3.0.1, which refuses the SHA-256 token, and 3.5.2,
// which takes both). cdb therefore emits SHA-256 by default and keeps SHA-1
// for the servers that need it, rather than picking one and calling the other
// server broken.
//
// The result is derived from the secret and must be treated as the secret is:
// never printed, logged, or put in an error message.
func proxyToken(secret, username, hash string) string {
	digest := sha256.New
	if hash == ProxyHashSHA1 {
		digest = sha1.New
	}
	mac := hmac.New(digest, []byte(secret))
	mac.Write([]byte(username))
	return hex.EncodeToString(mac.Sum(nil))
}

// normaliseProxyHash fills in the default and rejects anything cdb cannot
// compute. A hand-edited proxy_hash of "md5" would otherwise fall back to
// the default and authenticate as nobody against the very server it was pinned
// for, which is the failure the pin exists to avoid.
func normaliseProxyHash(hash string) (string, error) {
	switch hash {
	case "":
		return ProxyHashSHA256, nil
	case ProxyHashSHA256, ProxyHashSHA1:
		return hash, nil
	}
	return "", fmt.Errorf("unknown proxy token hash %q; expected %s or %s", hash, ProxyHashSHA256, ProxyHashSHA1)
}

// proxyHeaders is the same three headers as a map, for a _replicator
// document's per-endpoint "headers" object. Like every endpoint map, it is for
// request bodies only and must never be rendered, logged, or surfaced in a
// command.Result.
func proxyHeaders(username, roles, token string) map[string]any {
	h := map[string]any{
		"X-Auth-CouchDB-UserName": username,
		"X-Auth-CouchDB-Token":    token,
	}
	if roles != "" {
		h["X-Auth-CouchDB-Roles"] = roles
	}
	return h
}

// closeIdle passes a close down to a transport that supports one. The interface
// is the same unexported contract net/http itself checks for.
func closeIdle(rt http.RoundTripper) {
	if c, ok := rt.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}

// newTransport builds the base transport, applying TLS settings.
func newTransport(cfg Config) (http.RoundTripper, error) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.InsecureTLS {
		tlsCfg.InsecureSkipVerify = true
	}
	if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read ca_file %q: %w", cfg.CAFile, err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("ca_file %q contains no certificates", cfg.CAFile)
		}
		tlsCfg.RootCAs = pool
	}
	tr.TLSClientConfig = tlsCfg
	return tr, nil
}

func newJar() (http.CookieJar, error) { return cookiejar.New(nil) }
