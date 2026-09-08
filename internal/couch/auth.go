package couch

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"os"
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

func (t *sessionTransport) login() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	body, err := json.Marshal(map[string]string{"name": t.username, "password": t.password})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, t.baseURL+"/_session", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := (&http.Client{Transport: t.base, Jar: t.jar}).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return NewError(res.StatusCode, "unauthorized", "Name or password is incorrect.", "authenticate", "")
	}
	t.authed = true
	return nil
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
	if !t.authenticated() {
		if err := t.login(); err != nil {
			return nil, err
		}
	}
	res, err := t.base.RoundTrip(withCookies(req, t.jar))
	if err != nil || res.StatusCode != http.StatusUnauthorized {
		return res, err
	}
	res.Body.Close()
	t.invalidate()
	if err := t.login(); err != nil {
		return nil, err
	}
	return t.base.RoundTrip(withCookies(req, t.jar))
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
