package couch

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
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
		return loginError(res)
	}
	t.authed = true
	return nil
}

// loginError turns a failed POST /_session into an *Error. The server's own
// error and reason are used when it sent them; the credentials message is only
// claimed for a 401, since any other status means something else went wrong.
func loginError(res *http.Response) error {
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
	return NewError(res.StatusCode, name, reason, "authenticate", "")
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

// jwtTransport adds a bearer token to every request.
type jwtTransport struct {
	base  http.RoundTripper
	token string
}

func (t *jwtTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	out := req.Clone(req.Context())
	out.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(out)
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
