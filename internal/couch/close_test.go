package couch

import (
	"net/http"
	"sync/atomic"
	"testing"
)

// closeSpy is a base transport that records whether http.Client reached its
// CloseIdleConnections through whatever is wrapped around it.
type closeSpy struct{ closed atomic.Int32 }

func (s *closeSpy) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, http.ErrUseLastResponse
}
func (s *closeSpy) CloseIdleConnections() { s.closed.Add(1) }

// http.Client.CloseIdleConnections only reaches the pool when its transport
// exposes CloseIdleConnections. Neither auth transport did, so Client.Close was
// a no-op for every authenticated client — which is every real one — and the
// sockets stayed open until the process ended.
func TestCloseReachesTheBaseTransport(t *testing.T) {
	for _, tc := range []struct {
		name string
		wrap func(http.RoundTripper) http.RoundTripper
	}{
		{"session", func(base http.RoundTripper) http.RoundTripper {
			return &sessionTransport{base: base, baseURL: "http://localhost:5984", username: "admin", password: "x"}
		}},
		{"jwt", func(base http.RoundTripper) http.RoundTripper {
			return &jwtTransport{base: base, token: "t"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy := &closeSpy{}
			hc := &http.Client{Transport: tc.wrap(spy)}
			hc.CloseIdleConnections()
			if got := spy.closed.Load(); got != 1 {
				t.Errorf("base CloseIdleConnections called %d times, want 1", got)
			}
		})
	}
}

// And through the real Client, which is what callers use.
func TestClientCloseReachesTheBaseTransport(t *testing.T) {
	spy := &closeSpy{}
	jar, err := newJar()
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{hc: &http.Client{Transport: &sessionTransport{base: spy, jar: jar, baseURL: "http://localhost:5984"}}}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if got := spy.closed.Load(); got != 1 {
		t.Errorf("base CloseIdleConnections called %d times, want 1", got)
	}
}
