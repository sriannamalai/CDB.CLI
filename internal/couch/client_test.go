package couch

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
)

func TestSessionAuthLogsInOnceAndSendsCookie(t *testing.T) {
	srv := couchtest.New(t)
	c, err := New(Config{URL: srv.URL(), Auth: AuthSession, Username: "admin", Secret: "password"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.ServerInfo(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Session(context.Background()); err != nil {
		t.Fatal(err)
	}
	logins, cookies := 0, 0
	for _, r := range srv.Requests() {
		if r.Method == "POST" && r.Path == "/_session" {
			logins++
		}
		if r.Method == "GET" && r.Path == "/_session" && r.Header.Get("Cookie") != "" {
			cookies++
		}
	}
	if logins != 1 {
		t.Errorf("POST /_session happened %d times, want 1", logins)
	}
	if cookies != 1 {
		t.Errorf("GET /_session carried a cookie %d times, want 1", cookies)
	}
}

func TestSessionAuthRetriesOnce401(t *testing.T) {
	srv := couchtest.New(t)
	n := 0
	srv.On("GET", "/_all_dbs", func(w http.ResponseWriter, _ *http.Request) {
		n++
		if n == 1 {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":"unauthorized","reason":"expired"}`))
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`["mydb"]`))
	})
	c, _ := New(Config{URL: srv.URL(), Auth: AuthSession, Username: "admin", Secret: "password"})
	defer c.Close()
	var out []string
	if err := c.DoJSON(context.Background(), "GET", "/_all_dbs", nil, &out, "list", "databases"); err != nil {
		t.Fatalf("DoJSON after a 401 retry: %v", err)
	}
	if len(out) != 1 || out[0] != "mydb" {
		t.Fatalf("out = %v", out)
	}
	logins := 0
	for _, r := range srv.Requests() {
		if r.Method == "POST" && r.Path == "/_session" {
			logins++
		}
	}
	if logins != 2 {
		t.Errorf("POST /_session happened %d times, want 2", logins)
	}
}

func TestSessionAuthReplaysBodyOn401(t *testing.T) {
	srv := couchtest.New(t)
	var bodies []string
	srv.On("POST", "/mydb/_bulk_docs", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if len(bodies) == 1 {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":"unauthorized","reason":"expired"}`))
			return
		}
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`[{"ok":true,"id":"a","rev":"1-x"}]`))
	})
	c, _ := New(Config{URL: srv.URL(), Auth: AuthSession, Username: "admin", Secret: "password"})
	defer c.Close()
	// The filler makes the body larger than any single write buffer, so a replay
	// that silently truncated the stream would show up here.
	req := map[string]any{"docs": []map[string]string{{"_id": "a", "filler": strings.Repeat("x", 64<<10)}}}
	var out []map[string]any
	if err := c.DoJSON(context.Background(), "POST", "/mydb/_bulk_docs", req, &out, "write", `database "mydb"`); err != nil {
		t.Fatalf("DoJSON after a 401 retry: %v", err)
	}
	if len(bodies) != 2 {
		t.Fatalf("server saw %d requests, want 2", len(bodies))
	}
	if bodies[1] != bodies[0] {
		t.Errorf("replayed body = %q, want the original %q", bodies[1], bodies[0])
	}
}

func TestSessionAuthLogsInOnceUnderConcurrency(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_all_dbs", 200, `["mydb"]`)
	c, err := New(Config{URL: srv.URL(), Auth: AuthSession, Username: "admin", Secret: "password"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// The goroutines wait on a barrier so they really do reach the transport
	// together; without it the first one finishes logging in before the rest
	// start, and a stampede never gets a chance to happen.
	const n = 8
	var wg, ready sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		ready.Add(1)
		go func() {
			defer wg.Done()
			ready.Done()
			<-start
			var out []string
			errs[i] = c.DoJSON(context.Background(), "GET", "/_all_dbs", nil, &out, "list", "databases")
		}()
	}
	ready.Wait()
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}

	logins := 0
	for _, r := range srv.Requests() {
		if r.Method == "POST" && r.Path == "/_session" {
			logins++
		}
	}
	if logins != 1 {
		t.Errorf("%d parallel requests on a cold client caused %d POST /_session, want 1", n, logins)
	}
}

func TestURLAndErrorsRedactCredentials(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/nope", 404, `{"error":"not_found","reason":"Database does not exist."}`)
	u, err := url.Parse(srv.URL())
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword("admin", "secret")

	c, err := New(Config{URL: u.String(), Auth: AuthNone})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if got := c.URL(); strings.Contains(got, "secret") {
		t.Errorf("URL() = %q, must not contain the password", got)
	}
	if got := c.URL(); strings.Contains(got, "admin") {
		t.Errorf("URL() = %q, must not contain the user name either", got)
	}
	// The credentials must still reach the wire, or basic auth would break.
	if _, err := c.ServerInfo(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := srv.Last("GET", "/").Header.Get("Authorization"); got == "" {
		t.Error("no Authorization header sent; the userinfo was dropped from the request URL too")
	}

	// A server error must not carry the password.
	err = c.DoJSON(context.Background(), "GET", "/nope", nil, nil, "read", `database "nope"`)
	if err == nil {
		t.Fatal("GET /nope returned no error")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Errorf("server error %q leaks the password", err)
	}

	// A connect failure must not carry the password either.
	dead, err := New(Config{URL: "http://admin:secret@127.0.0.1:1/", Auth: AuthNone})
	if err != nil {
		t.Fatal(err)
	}
	defer dead.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = dead.ServerInfo(ctx)
	if err == nil {
		t.Fatal("connecting to 127.0.0.1:1 returned no error")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Errorf("connect error %q leaks the password", err)
	}
}

func TestJWTAuthSetsBearerHeader(t *testing.T) {
	srv := couchtest.New(t)
	c, _ := New(Config{URL: srv.URL(), Auth: AuthJWT, Secret: "tok.en.sig"})
	defer c.Close()
	if _, err := c.ServerInfo(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := srv.Last("GET", "/").Header.Get("Authorization"); got != "Bearer tok.en.sig" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer tok.en.sig")
	}
}

func TestDoJSONWrapsServerError(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/nope", 404, `{"error":"not_found","reason":"Database does not exist."}`)
	c, _ := New(Config{URL: srv.URL(), Auth: AuthNone})
	defer c.Close()
	err := c.DoJSON(context.Background(), "GET", "/nope", nil, nil, "read", `database "nope"`)
	e, ok := AsError(err)
	if !ok {
		t.Fatalf("err is %T, want *couch.Error", err)
	}
	if e.Status != 404 || e.Name != "not_found" || e.Reason != "Database does not exist." {
		t.Errorf("error = %+v", e)
	}
	if e.Target != `database "nope"` {
		t.Errorf("Target = %q", e.Target)
	}
}

func TestSessionInfo(t *testing.T) {
	srv := couchtest.New(t)
	c, _ := New(Config{URL: srv.URL(), Auth: AuthNone})
	defer c.Close()
	s, err := c.Session(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "admin" || len(s.Roles) != 1 || s.Roles[0] != "_admin" || s.Method != "cookie" {
		t.Errorf("Session() = %+v", s)
	}
}

func TestServerInfo(t *testing.T) {
	srv := couchtest.New(t)
	c, _ := New(Config{URL: srv.URL(), Auth: AuthNone})
	defer c.Close()
	info, err := c.ServerInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Version != "3.5.2" || info.Vendor != "The Apache Software Foundation" {
		t.Errorf("ServerInfo() = %+v", info)
	}
}

func TestNewRejectsBadURL(t *testing.T) {
	for _, u := range []string{"", "localhost:5984", "ftp://example.com"} {
		if _, err := New(Config{URL: u}); err == nil {
			t.Errorf("New(%q) = nil error, want error", u)
		}
	}
}
