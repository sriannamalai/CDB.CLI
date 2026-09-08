// Package couchtest provides a route-stub CouchDB server for tests.
//
// The Kivik in-memory driver is not usable as a test double for cdb: as of
// kivik v4.5.2 its Query, Changes and attachment methods are unimplemented,
// AllDocs ignores every option and returns rows in map order, and BulkDocs
// silently ignores new_edits=false. Tests therefore stub HTTP routes here and
// assert on the exact requests cdb makes.
package couchtest

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// Request is a request the server received.
type Request struct {
	Method   string
	Path     string
	RawQuery string
	Header   http.Header
	Body     []byte
}

// Query returns the decoded value of a single query parameter.
func (r *Request) Query(key string) string {
	v, err := url.ParseQuery(r.RawQuery)
	if err != nil {
		return ""
	}
	return v.Get(key)
}

// Server is a stub CouchDB server.
type Server struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	routes   map[string]http.HandlerFunc
	requests []*Request
}

// New starts a stub server with default handlers for GET /, POST /_session and
// GET /_session. It is closed automatically when the test ends.
func New(t *testing.T) *Server {
	t.Helper()
	s := &Server{t: t, routes: map[string]http.HandlerFunc{}}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	s.JSON("GET", "/", 200, `{"couchdb":"Welcome","version":"3.5.2","features":["scheduler","partitioned"],"vendor":{"name":"The Apache Software Foundation"}}`)
	s.On("POST", "/_session", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "AuthSession", Value: "test-cookie", Path: "/"})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, `{"ok":true,"name":"admin","roles":["_admin"]}`)
	})
	s.JSON("GET", "/_session", 200, `{"ok":true,"userCtx":{"name":"admin","roles":["_admin"]},"info":{"authenticated":"cookie","authentication_handlers":["cookie","default"]}}`)
	return s
}

// URL is the base URL of the stub server, with no trailing slash.
func (s *Server) URL() string { return s.srv.URL }

// On registers a handler for an exact method and path.
func (s *Server) On(method, path string, h http.HandlerFunc) *Server {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes[method+" "+path] = h
	return s
}

// JSON registers a handler that always answers with the given status and body.
func (s *Server) JSON(method, path string, status int, body string) *Server {
	return s.On(method, path, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	})
}

// JSONSeq registers a handler that answers each successive call with the next
// body in bodies, repeating the last one once exhausted.
func (s *Server) JSONSeq(method, path string, status int, bodies ...string) *Server {
	var n int
	var mu sync.Mutex
	return s.On(method, path, func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		body := bodies[n]
		if n < len(bodies)-1 {
			n++
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	})
}

// Requests returns every request the server received, in order.
func (s *Server) Requests() []*Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Request, len(s.requests))
	copy(out, s.requests)
	return out
}

// Last returns the most recent request for a method and path, or nil.
func (s *Server) Last(method, path string) *Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.requests) - 1; i >= 0; i-- {
		if s.requests[i].Method == method && s.requests[i].Path == path {
			return s.requests[i]
		}
	}
	return nil
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	rec := &Request{Method: r.Method, Path: r.URL.Path, RawQuery: r.URL.RawQuery, Header: r.Header.Clone(), Body: body}
	s.mu.Lock()
	s.requests = append(s.requests, rec)
	h, ok := s.routes[r.Method+" "+r.URL.Path]
	s.mu.Unlock()
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":"not_found","reason":"missing"}`)
		return
	}
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	h(w, r)
}

// Encode is a helper for building JSON bodies in tests.
func Encode(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
