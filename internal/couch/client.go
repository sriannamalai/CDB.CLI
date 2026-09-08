package couch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	kivik "github.com/go-kivik/kivik/v4"
	"github.com/go-kivik/kivik/v4/couchdb"
)

// AuthKind selects how the client authenticates.
type AuthKind string

const (
	AuthNone    AuthKind = "none"
	AuthSession AuthKind = "session"
	AuthJWT     AuthKind = "jwt"
)

// Config describes a connection. Secret is never logged or serialised.
type Config struct {
	URL         string
	Auth        AuthKind
	Username    string
	Secret      string
	InsecureTLS bool
	CAFile      string
	UserAgent   string
}

// Client talks to one CouchDB server. Its Kivik client and its raw HTTP client
// share one transport, so both are authenticated the same way.
type Client struct {
	kc  *kivik.Client
	hc  *http.Client
	cfg Config
	// base is the URL requests are built against. It keeps any userinfo the
	// operator supplied, so net/http can turn it into a Basic auth header. It
	// must never be printed, logged, or put in an error message.
	base string
	// safe is base with the userinfo stripped. Everything operator-visible —
	// Client.URL, error targets — uses this one.
	safe string
	host string
}

// New builds a client. It performs no network I/O; call Ping to verify.
func New(cfg Config) (*Client, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("no server URL")
	}
	u, err := url.Parse(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("invalid server URL %q: %w", cfg.URL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("server URL %q must start with http:// or https://", cfg.URL)
	}
	base := strings.TrimRight(u.String(), "/")
	// A URL may carry credentials (https://user:pass@host). Keep them on base so
	// requests still authenticate, and derive a redacted form for anything the
	// operator can see. url.URL.Redacted is not used: it substitutes "xxxxx",
	// which is noise in a prompt. The whole userinfo goes instead.
	redacted := *u
	redacted.User = nil
	safe := strings.TrimRight(redacted.String(), "/")

	tr, err := newTransport(cfg)
	if err != nil {
		return nil, err
	}
	switch cfg.Auth {
	case AuthSession:
		jar, err := newJar()
		if err != nil {
			return nil, err
		}
		tr = &sessionTransport{base: tr, jar: jar, baseURL: base, username: cfg.Username, password: cfg.Secret}
	case AuthJWT:
		tr = &jwtTransport{base: tr, token: cfg.Secret}
	}
	hc := &http.Client{Transport: tr}

	ua := cfg.UserAgent
	if ua == "" {
		ua = "cdb"
	}
	kc, err := kivik.New("couch", base+"/", couchdb.OptionHTTPClient(hc), couchdb.OptionUserAgent(ua))
	if err != nil {
		return nil, Wrap(err, "connect to", safe)
	}
	return &Client{kc: kc, hc: hc, cfg: cfg, base: base, safe: safe, host: u.Host}, nil
}

// Close releases the underlying Kivik client.
func (c *Client) Close() error { return c.kc.Close() }

// URL is the server URL with no trailing slash and no embedded credentials. It
// is safe to print, log, or put in an error message.
func (c *Client) URL() string { return c.safe }

// Host is the host:port of the server, for prompts and messages.
func (c *Client) Host() string { return c.host }

// Username is the configured user name, or "" when unauthenticated.
func (c *Client) Username() string { return c.cfg.Username }

// Kivik exposes the underlying Kivik client. Only internal/couch may use it.
func (c *Client) Kivik() *kivik.Client { return c.kc }

// HTTP exposes the shared, authenticated HTTP client for endpoints Kivik does
// not model (_dbs_info, _scheduler, streamed attachments, partitioned paths).
func (c *Client) HTTP() *http.Client { return c.hc }

// NewRequest builds a request against apiPath, which must begin with "/".
func (c *Client) NewRequest(ctx context.Context, method, apiPath string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+apiPath, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

// DoJSON performs a request and decodes a JSON response into out. A non-2xx
// response is decoded as a CouchDB error and returned as *Error.
func (c *Client) DoJSON(ctx context.Context, method, apiPath string, body any, out any, op, target string) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = strings.NewReader(string(b))
	}
	req, err := c.NewRequest(ctx, method, apiPath, rdr)
	if err != nil {
		return Wrap(err, op, target)
	}
	return c.doDecode(req, out, op, target)
}

// doDecode performs a prepared request and decodes a JSON response into out,
// which may be nil to discard the body. A non-2xx response is decoded as a
// CouchDB error and returned as *Error.
//
// Every request cdb makes outside Kivik ends here: DoJSON builds the common
// case, and the methods that need a header or a body Kivik cannot express
// (PutDocument, DeleteDocument, CopyDocument) prepare their own request and
// call this directly.
func (c *Client) doDecode(req *http.Request, out any, op, target string) error {
	res, err := c.hc.Do(req)
	if err != nil {
		return Wrap(err, op, target)
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		var e struct {
			Error  string `json:"error"`
			Reason string `json:"reason"`
		}
		_ = json.NewDecoder(res.Body).Decode(&e)
		if e.Error == "" {
			e.Error = nameForStatus(res.StatusCode)
		}
		if res.StatusCode == http.StatusUnauthorized {
			return NewError(res.StatusCode, "unauthorized", e.Reason, op,
				unauthorizedTarget(c.cfg.Username, c.host))
		}
		return NewError(res.StatusCode, e.Error, e.Reason, op, target)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, res.Body)
		return nil
	}
	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return Wrap(err, op, target)
	}
	return nil
}
