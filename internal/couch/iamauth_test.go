package couch

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeIAM is an IBM IAM token endpoint. Each exchange hands out a new token so
// a test can tell a refreshed request from a replayed one, and every request is
// checked to be the form post IAM actually requires.
type fakeIAM struct {
	srv       *httptest.Server
	exchanges atomic.Int64
	// expiresIn is the token lifetime the endpoint reports, in seconds.
	expiresIn int64
	// now is the clock the endpoint dates its tokens by; the test moves it.
	now func() time.Time
	// fail, when set, is the status and body the endpoint answers with.
	fail      int
	failBody  string
	malformed bool
	// accept is the Accept header of the most recent exchange request.
	accept string
}

func newFakeIAM(t *testing.T) *fakeIAM {
	t.Helper()
	f := &fakeIAM{expiresIn: 3600, now: time.Now}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("IAM request body did not parse as a form: %v", err)
		}
		if got := r.Header.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
			t.Errorf("IAM Content-Type = %q", got)
		}
		if got := r.PostForm.Get("grant_type"); got != "urn:ibm:params:oauth:grant-type:apikey" {
			t.Errorf("IAM grant_type = %q", got)
		}
		if r.PostForm.Get("apikey") == "" {
			t.Error("IAM request carried no apikey")
		}
		f.accept = r.Header.Get("Accept")
		n := f.exchanges.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case f.fail != 0:
			w.WriteHeader(f.fail)
			_, _ = io.WriteString(w, f.failBody)
		case f.malformed:
			w.WriteHeader(200)
			_, _ = io.WriteString(w, `{"token_type":"Bearer","expires_in":3600}`)
		default:
			exp := f.now().Add(time.Duration(f.expiresIn) * time.Second).Unix()
			_, _ = fmt.Fprintf(w, `{"access_token":"tok-%d","token_type":"Bearer","expires_in":%d,"expiration":%d,"scope":"ibm openid"}`, n, f.expiresIn, exp)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// cloudant is a stub of the Cloudant side: it records the bearer it was given
// and answers 401 for the first n requests.
type cloudant struct {
	srv      *httptest.Server
	bearers  []string
	failures int
}

func newCloudant(t *testing.T) *cloudant {
	t.Helper()
	c := &cloudant{}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.bearers = append(c.bearers, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		w.Header().Set("Content-Type", "application/json")
		if c.failures > 0 {
			c.failures--
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":"unauthorized","reason":"credentials expired"}`)
			return
		}
		_, _ = io.WriteString(w, `{"couchdb":"Welcome","version":"3.5.2+cloudant","vendor":{"name":"IBM Cloudant"}}`)
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func iamClient(t *testing.T, iam *fakeIAM, target string, clock func() time.Time) *Client {
	t.Helper()
	c, err := New(Config{URL: target, Auth: AuthIAM, Secret: "an-api-key", IAMURL: iam.srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if clock != nil {
		c.hc.Transport.(*iamTransport).now = clock
	}
	return c
}

// Case 1 of spec §4.4: the exchange happens once and the bearer is sent.
func TestIAMExchangesOnceAndSendsTheBearer(t *testing.T) {
	iam := newFakeIAM(t)
	cl := newCloudant(t)
	c := iamClient(t, iam, cl.srv.URL, nil)
	for i := 0; i < 3; i++ {
		if _, err := c.ServerInfo(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if got := iam.exchanges.Load(); got != 1 {
		t.Errorf("%d exchanges, want 1: the token is not being cached", got)
	}
	for i, b := range cl.bearers {
		if b != "tok-1" {
			t.Errorf("request %d carried bearer %q, want tok-1", i, b)
		}
	}
}

// Case 2: the token is refreshed when fewer than 60 seconds are left, before
// the request goes out, so a long-running command never sends a dead token.
func TestIAMRefreshesBeforeExpiry(t *testing.T) {
	iam := newFakeIAM(t)
	iam.expiresIn = 120
	cl := newCloudant(t)
	base := time.Now()
	clock := base
	iam.now = func() time.Time { return clock }
	c := iamClient(t, iam, cl.srv.URL, func() time.Time { return clock })
	if _, err := c.ServerInfo(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 30 seconds in, 90 remain: no refresh.
	clock = base.Add(30 * time.Second)
	if _, err := c.ServerInfo(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := iam.exchanges.Load(); got != 1 {
		t.Fatalf("%d exchanges after 30s of a 120s token, want 1", got)
	}
	// 70 seconds in, 50 remain, which is inside the 60-second window.
	clock = base.Add(70 * time.Second)
	if _, err := c.ServerInfo(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := iam.exchanges.Load(); got != 2 {
		t.Fatalf("%d exchanges after 70s of a 120s token, want 2", got)
	}
	if last := cl.bearers[len(cl.bearers)-1]; last != "tok-2" {
		t.Errorf("the last request carried %q, want tok-2", last)
	}
}

// Case 3: a 401 on a token that was not just minted refreshes once and replays
// the request, body and all.
func TestIAM401RefreshesAndRetriesWithTheBody(t *testing.T) {
	iam := newFakeIAM(t)
	cl := newCloudant(t)
	c := iamClient(t, iam, cl.srv.URL, nil)
	// Warm the cache so the request's own token is not a freshly minted one.
	if _, err := c.ServerInfo(context.Background()); err != nil {
		t.Fatal(err)
	}
	cl.failures = 1
	if err := c.DoJSON(context.Background(), "POST", "/mydb", map[string]any{"hello": "iam"}, nil, "write", `database "mydb"`); err != nil {
		t.Fatalf("the retried request failed: %v", err)
	}
	if got := iam.exchanges.Load(); got != 2 {
		t.Errorf("%d exchanges, want 2", got)
	}
	want := []string{"tok-1", "tok-1", "tok-2"}
	if len(cl.bearers) != len(want) {
		t.Fatalf("bearers = %v, want %v", cl.bearers, want)
	}
	for i := range want {
		if cl.bearers[i] != want[i] {
			t.Errorf("request %d carried %q, want %q", i, cl.bearers[i], want[i])
		}
	}
}

// A second 401 is reported rather than retried forever.
func TestIAMSecond401IsReported(t *testing.T) {
	iam := newFakeIAM(t)
	cl := newCloudant(t)
	c := iamClient(t, iam, cl.srv.URL, nil)
	if _, err := c.ServerInfo(context.Background()); err != nil {
		t.Fatal(err)
	}
	cl.failures = 2
	_, err := c.ServerInfo(context.Background())
	ce, ok := AsError(err)
	if !ok {
		t.Fatalf("err is %T (%v), want a *couch.Error", err, err)
	}
	if ce.Status != http.StatusUnauthorized || ce.Auth != AuthIAM {
		t.Errorf("status %d auth %q, want 401 and iam", ce.Status, ce.Auth)
	}
	if ce.Name == IAMExchangeFailed {
		t.Error("a Cloudant rejection was reported as an IAM exchange failure")
	}
}

// Case 4: the exchange itself fails. The API key never appears in the error,
// and the name is the one internal/render matches on.
func TestIAMExchangeFailureIsAnAuthError(t *testing.T) {
	iam := newFakeIAM(t)
	iam.fail = http.StatusBadRequest
	iam.failBody = `{"errorCode":"BXNIM0415E","errorMessage":"Provided API key could not be found."}`
	cl := newCloudant(t)
	c := iamClient(t, iam, cl.srv.URL, nil)
	_, err := c.ServerInfo(context.Background())
	ce, ok := AsError(err)
	if !ok {
		t.Fatalf("err is %T (%v), want a *couch.Error", err, err)
	}
	if ce.Status != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 so the exit code is 3", ce.Status)
	}
	if ce.Name != IAMExchangeFailed {
		t.Errorf("name = %q, want %q", ce.Name, IAMExchangeFailed)
	}
	if ce.Reason != "Provided API key could not be found." {
		t.Errorf("reason = %q, want IBM's errorMessage", ce.Reason)
	}
	if strings.Contains(ce.Error(), "an-api-key") {
		t.Errorf("the error leaked the API key: %s", ce.Error())
	}
}

// Case 5: a 200 with no access_token is as much a failure as a 400.
func TestIAMMalformedAnswerIsAnAuthError(t *testing.T) {
	iam := newFakeIAM(t)
	iam.malformed = true
	cl := newCloudant(t)
	c := iamClient(t, iam, cl.srv.URL, nil)
	_, err := c.ServerInfo(context.Background())
	ce, ok := AsError(err)
	if !ok {
		t.Fatalf("err is %T (%v), want a *couch.Error", err, err)
	}
	if ce.Status != http.StatusUnauthorized || ce.Name != IAMExchangeFailed {
		t.Errorf("status %d name %q, want 401 and %q", ce.Status, ce.Name, IAMExchangeFailed)
	}
}

// A 307 or 308 on the token endpoint would, with net/http's default client,
// re-POST the form -- API key and all -- to whatever host Location names. The
// exchange must stop at the redirect and report a failure instead.
func TestIAMExchangeDoesNotFollowARedirect(t *testing.T) {
	var followed atomic.Int64
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		followed.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"stolen","token_type":"Bearer","expires_in":3600}`)
	}))
	t.Cleanup(sink.Close)
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, &http.Request{}, sink.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirector.Close)

	cl := newCloudant(t)
	c, err := New(Config{URL: cl.srv.URL, Auth: AuthIAM, Secret: "an-api-key", IAMURL: redirector.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.ServerInfo(context.Background())
	if err == nil {
		t.Fatal("the exchange followed the redirect and connected")
	}
	ce, ok := AsError(err)
	if !ok || ce.Name != IAMExchangeFailed {
		t.Fatalf("err = %v (%T), want %s", err, err, IAMExchangeFailed)
	}
	if n := followed.Load(); n != 0 {
		t.Errorf("the API key was re-posted to the redirect target %d time(s)", n)
	}
	if strings.Contains(ce.Reason, "an-api-key") {
		t.Error("the failure reason carried the API key")
	}
}

// The refresh a 401 triggers can itself fail. That is the exchange sentence,
// and there is no retry to make: there is no token to retry with.
func TestIAMRefreshFailureAfterA401IsTheExchangeError(t *testing.T) {
	iam := newFakeIAM(t)
	cl := newCloudant(t)
	c := iamClient(t, iam, cl.srv.URL, nil)
	if _, err := c.ServerInfo(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The held token is now stale in Cloudant's eyes, and IAM will not mint
	// another.
	cl.failures = 1
	iam.fail, iam.failBody = http.StatusBadRequest, `{"errorMessage":"Provided API key could not be found"}`
	_, err := c.ServerInfo(context.Background())
	if err == nil {
		t.Fatal("the failed refresh reported success")
	}
	ce, ok := AsError(err)
	if !ok || ce.Name != IAMExchangeFailed {
		t.Fatalf("err = %v (%T), want %s", err, err, IAMExchangeFailed)
	}
	if ce.Status != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 so the exit code is 3", ce.Status)
	}
	if n := iam.exchanges.Load(); n != 2 {
		t.Errorf("exchanges = %d, want 2: one mint and one failed refresh", n)
	}
}

// IBM answers either JSON or a form-encoded error depending on what is asked
// for, so the exchange asks for JSON explicitly.
func TestIAMExchangeAsksForJSON(t *testing.T) {
	iam := newFakeIAM(t)
	cl := newCloudant(t)
	c := iamClient(t, iam, cl.srv.URL, nil)
	if _, err := c.ServerInfo(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := iam.accept; got != "application/json" {
		t.Errorf("Accept on the exchange = %q, want application/json", got)
	}
}
