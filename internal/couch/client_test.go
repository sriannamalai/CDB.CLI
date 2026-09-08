package couch

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

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
