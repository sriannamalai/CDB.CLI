package couch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
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
	// ReplicationURL is the address the server should use to reach itself when
	// cdb writes a replication endpoint. Empty means "use URL", which is what
	// cdb 1.0 always did. It is a server address, never a database one, and it
	// never carries credentials: those travel in the replication document's
	// per-endpoint auth object.
	ReplicationURL string
}

// Client talks to one CouchDB server over net/http. Every request it makes —
// documents, views, _dbs_info, _scheduler, streamed attachments — goes through
// the one authenticated http.Client below, so they are all authenticated,
// paged and error-mapped the same way.
type Client struct {
	hc  *http.Client
	cfg Config
	// userAgent is sent on every request.
	userAgent string
	// base is the URL requests are built against. It keeps any userinfo the
	// operator supplied, so net/http can turn it into a Basic auth header. It
	// must never be printed, logged, or put in an error message.
	base string
	// safe is base with the userinfo stripped. Everything operator-visible —
	// Client.URL, error targets — uses this one.
	safe string
	// replication is the validated, slash-trimmed ReplicationURL, or "" when
	// none was configured. It never carries userinfo, so it is safe to print.
	replication string
	host        string
}

// New builds a client. It performs no network I/O; call Ping to verify.
func New(cfg Config) (*Client, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("no server URL")
	}
	u, err := url.Parse(cfg.URL)
	if err != nil {
		// Neither the URL nor url.Error's own text may be printed as it
		// stands: both repeat whatever the operator typed, password included.
		// Only the parse failure's reason is safe.
		reason := err.Error()
		var ue *url.Error
		if errors.As(err, &ue) {
			reason = ue.Err.Error()
		}
		return nil, fmt.Errorf("invalid server URL %q: %s", RedactURL(cfg.URL), reason)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("server URL %q must start with http:// or https://", RedactURL(cfg.URL))
	}
	base := strings.TrimRight(u.String(), "/")
	// A URL may carry credentials (https://user:pass@host). Keep them on base so
	// requests still authenticate, and derive a redacted form for anything the
	// operator can see. url.URL.Redacted is not used: it substitutes "xxxxx",
	// which is noise in a prompt. The whole userinfo goes instead.
	redacted := *u
	redacted.User = nil
	safe := strings.TrimRight(redacted.String(), "/")

	replication, err := normaliseReplicationURL(cfg.ReplicationURL)
	if err != nil {
		return nil, err
	}

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
	case AuthNone, "":
		// Nothing to add. The empty kind is the zero value, not a mistake:
		// the config layer defaults an unset auth to "session" before it
		// reaches here, and a Config built by hand may legitimately leave it
		// out. A URL's own userinfo still authenticates, via net/http.
	default:
		// A kind that is neither known nor empty is a typo in a hand-edited
		// config. Ignoring it built a client that sends no credentials while
		// the profile says it will, and dial's anonymous-login guard does not
		// catch that: the guard fires only when Auth is not "none", which a
		// typo also is not.
		return nil, fmt.Errorf("unknown authentication kind %q; expected session, jwt or none", cfg.Auth)
	}
	hc := &http.Client{Transport: tr}

	ua := cfg.UserAgent
	if ua == "" {
		ua = "cdb"
	}
	return &Client{hc: hc, cfg: cfg, userAgent: ua, base: base, safe: safe, replication: replication, host: u.Host}, nil
}

// anonymous reports whether the client sends no credentials at all: no session
// login, no bearer token, and no userinfo on the base URL for net/http to turn
// into a Basic header. A 401 then means the server wants credentials, not that
// the ones supplied were wrong.
func (c *Client) anonymous() bool {
	return (c.cfg.Auth == AuthNone || c.cfg.Auth == "") && c.base == c.safe
}

// unauthorized builds the *Error for a 401. An authenticated client's 401 is
// retargeted at the user and host, so the sentence names the real user rather
// than whatever the call was about; an anonymous client has no user to name, so
// the call's own target is kept and the auth kind carries the distinction.
func (c *Client) unauthorized(reason, op, target string) *Error {
	if c.anonymous() {
		e := NewError(http.StatusUnauthorized, "unauthorized", reason, op, target)
		e.Auth = AuthNone
		return e
	}
	e := NewError(http.StatusUnauthorized, "unauthorized", reason, op,
		unauthorizedTarget(c.cfg.Username, c.host))
	// A URL's own userinfo authenticates through net/http while the configured
	// kind is still "none", so the kind is only recorded when it is one that
	// sends credentials of its own. Leaving it unset says "credentials were
	// sent", which is all the message needs.
	if c.cfg.Auth != AuthNone && c.cfg.Auth != "" {
		e.Auth = c.cfg.Auth
	}
	return e
}

// Close releases the connections the client is holding open.
func (c *Client) Close() error {
	c.hc.CloseIdleConnections()
	return nil
}

// URL is the server URL with no trailing slash and no embedded credentials. It
// is safe to print, log, or put in an error message.
func (c *Client) URL() string { return c.safe }

// Host is the host:port of the server, for prompts and messages.
func (c *Client) Host() string { return c.host }

// Username is the configured user name, or "" when unauthenticated.
func (c *Client) Username() string { return c.cfg.Username }

// HTTP exposes the shared, authenticated HTTP client for the callers that need
// the response itself rather than a decoded body (streamed attachments).
func (c *Client) HTTP() *http.Client { return c.hc }

// NewRequest builds a request against apiPath, which must begin with "/".
func (c *Client) NewRequest(ctx context.Context, method, apiPath string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+apiPath, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
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
// Every request cdb makes ends here: DoJSON builds the common case, and the
// methods that need their own header or body (PutDocument, DeleteDocument,
// CopyDocument) prepare the request themselves and call this directly.
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
			return c.unauthorized(e.Reason, op, target)
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

// normaliseReplicationURL validates the address the server is told to call
// itself on, and trims its trailing slash.
//
// The messages never echo the value: what the operator typed may hold the very
// userinfo the second case refuses, and an error message is one of the places
// a secret must never reach.
func normaliseReplicationURL(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errors.New("The replication URL must be an absolute http or https URL.")
	}
	if u.User != nil {
		return "", errors.New("The replication URL must not contain a user name or password; cdb sends the profile's credentials in the replication document instead.")
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", errors.New("The replication URL must be a bare server address, with no query string or fragment.")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// ReplicationURL is the address the server is told to call itself on when cdb
// writes a replication endpoint: the configured override when there is one,
// otherwise the client's own URL. Validation guarantees it carries no
// credentials, so it is safe to print, log, or put in a Result.
func (c *Client) ReplicationURL() string {
	if c.replication != "" {
		return c.replication
	}
	return c.safe
}
