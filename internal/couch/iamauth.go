package couch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DefaultIAMURL is IBM Cloud's public token endpoint, used when a profile sets
// no iam_url of its own.
const DefaultIAMURL = "https://iam.cloud.ibm.com/identity/token"

const (
	// iamRefreshWindow is how much life a token must have left to be reused. An
	// IAM token lives an hour; a minute is long enough for a slow request to
	// finish after the check and short enough to cost one extra exchange an
	// hour at most.
	iamRefreshWindow = 60 * time.Second
	// iamExchangeTimeout bounds the token exchange. Without it a hung IAM
	// endpoint would hang every cdb command behind it, including ones the
	// operator could otherwise Ctrl-C out of.
	iamExchangeTimeout = 20 * time.Second
	// iamBodyLimit caps how much of IAM's answer is read. The body is small;
	// the limit exists so a misrouted endpoint cannot stream indefinitely.
	iamBodyLimit = 1 << 20
)

// iamTransport turns an IBM Cloud IAM API key into a bearer token and keeps it
// fresh. Cloudant accepts the token exactly as CouchDB accepts a JWT, so the
// request side is jwtTransport's; everything here is about the token's life.
//
// The API key and every token minted from it are secrets: they are never
// logged, never put in an error message, and never reach a command.Result.
type iamTransport struct {
	base   http.RoundTripper
	apiKey string
	iamURL string
	// host is the Cloudant host, for the target of any error this transport
	// raises. It is never the IAM host: the operator's connection is to
	// Cloudant, and that is the one they can act on.
	host string
	// now is the clock, injected so the expiry path is testable without
	// sleeping for an hour.
	now func() time.Time

	mu      sync.Mutex
	token   string
	expires time.Time
}

// bearer returns a usable token, minting one when there is none, when force is
// set, or when the one held has less than iamRefreshWindow left. minted says
// whether this call performed an exchange, which is what tells a 401 on a
// stale token apart from a 401 on a brand-new one: only the first is worth
// retrying.
func (t *iamTransport) bearer(ctx context.Context, force bool) (token string, minted bool, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !force && t.token != "" && t.now().Before(t.expires.Add(-iamRefreshWindow)) {
		return t.token, false, nil
	}
	tok, exp, err := t.exchange(ctx)
	if err != nil {
		return "", false, err
	}
	t.token, t.expires = tok, exp
	return tok, true, nil
}

// exchange performs the one call IBM documents: a form post carrying the API
// key under the apikey grant type.
func (t *iamTransport) exchange(ctx context.Context) (string, time.Time, error) {
	form := url.Values{
		"grant_type": {"urn:ibm:params:oauth:grant-type:apikey"},
		"apikey":     {t.apiKey},
	}
	ctx, cancel := context.WithTimeout(ctx, iamExchangeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.iamURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", time.Time{}, t.exchangeError("the IAM endpoint is not a usable URL")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	// A redirect is not followed. net/http's default client would replay the
	// form -- API key and all -- to whatever host a 307 or 308 names, which is
	// a credential handed to a server IBM did not vouch for; the older 30x
	// codes would re-issue it as a GET with the key in neither place. IBM's
	// token endpoint does not redirect, so any 3xx here is something else.
	client := &http.Client{
		Transport:     t.base,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	res, err := client.Do(req)
	if err != nil {
		// net/http's text repeats the request URL, which is the IAM endpoint
		// and carries nothing secret -- the key is in the body. trimPrefixes
		// drops the decoration all the same, for the same reason every other
		// transport error goes through it.
		return "", time.Time{}, t.exchangeError(trimPrefixes(err.Error()))
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 && res.StatusCode < 400 {
		return "", time.Time{}, t.exchangeError(fmt.Sprintf("IAM answered %d, a redirect the exchange will not follow", res.StatusCode))
	}
	var body struct {
		AccessToken  string `json:"access_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Expiration   int64  `json:"expiration"`
		ErrorMessage string `json:"errorMessage"`
	}
	_ = json.NewDecoder(io.LimitReader(res.Body, iamBodyLimit)).Decode(&body)
	if res.StatusCode != http.StatusOK || body.AccessToken == "" {
		reason := body.ErrorMessage
		if reason == "" {
			reason = fmt.Sprintf("IAM answered %d with no token", res.StatusCode)
		}
		return "", time.Time{}, t.exchangeError(reason)
	}
	// expiration is IBM's absolute unix second and is what the refresh window
	// is measured against. expires_in is the fallback for an endpoint that
	// sends only the relative form.
	exp := time.Unix(body.Expiration, 0)
	if body.Expiration == 0 {
		exp = t.now().Add(time.Duration(body.ExpiresIn) * time.Second)
	}
	return body.AccessToken, exp, nil
}

// exchangeError is the 401 a failed exchange raises. It is a 401 rather than a
// 5xx deliberately: internal/cli maps 401 to exit 3, the connection-failure
// code, which is what a key IAM will not accept is. IAM's own errorMessage
// travels as the reason, which internal/render prints only under --verbose.
func (t *iamTransport) exchangeError(reason string) error {
	e := NewError(http.StatusUnauthorized, IAMExchangeFailed, reason, "authenticate", "server "+t.host)
	e.Auth = AuthIAM
	return e
}

func (t *iamTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	token, minted, err := t.bearer(ctx, false)
	if err != nil {
		return nil, err
	}

	// Buffer the body before the first attempt. The attempt consumes it, and a
	// replay needs a fresh one; net/http rewinds only for its own connection
	// retries, so without this a retried POST would fail with
	// "ContentLength=N with Body length 0" instead of succeeding.
	var body io.ReadCloser
	replayable := req.Body == nil || req.Body == http.NoBody
	if !replayable && req.GetBody != nil {
		if body, err = req.GetBody(); err == nil {
			replayable = true
		}
	}

	res, err := t.base.RoundTrip(withBearer(req, token))
	if err != nil || res.StatusCode != http.StatusUnauthorized || minted || !replayable {
		// minted: the token is seconds old, so the 401 is Cloudant's answer to
		// a valid token rather than an expired one, and asking IAM again would
		// only ask the same question twice.
		// !replayable: a streamed body (a large attachment upload) cannot be
		// sent twice; hand the caller the 401 rather than a transport error.
		if body != nil {
			body.Close()
		}
		return res, err
	}

	fresh, _, ferr := t.bearer(ctx, true)
	if ferr != nil {
		if body != nil {
			body.Close()
		}
		res.Body.Close()
		return nil, ferr
	}
	res.Body.Close()
	retry := withBearer(req, fresh)
	if body != nil {
		retry.Body = body
	}
	return t.base.RoundTrip(retry)
}

// CloseIdleConnections delegates to the base transport; see sessionTransport's
// method for why it has to exist.
func (t *iamTransport) CloseIdleConnections() { closeIdle(t.base) }
