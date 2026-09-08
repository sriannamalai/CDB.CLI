package couch

import (
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"syscall"
	"testing"
)

// TestWrapClassifiesTransportFailures pins that any failure which never got an
// HTTP response is reported as unreachable. These errors used to fall into
// Wrap's default branch, which trusted kivik.HTTPStatus — it answered 500 for
// anything it did not recognise, so "https against an http port" printed "The
// server had a problem handling this request" and exited 1 instead of 3.
func TestWrapClassifiesTransportFailures(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		wantName string
	}{
		{
			name:     "tls handshake timeout",
			err:      &url.Error{Op: "Get", URL: "https://localhost:15984/", Err: errors.New("net/http: TLS handshake timeout")},
			wantName: "tls",
		},
		{
			name:     "unknown certificate authority",
			err:      &url.Error{Op: "Get", URL: "https://localhost:15984/", Err: x509.UnknownAuthorityError{}},
			wantName: "tls",
		},
		{
			name:     "connection reset",
			err:      &url.Error{Op: "Get", URL: "http://localhost:15984/", Err: &net.OpError{Op: "read", Net: "tcp", Err: os.NewSyscallError("read", syscall.ECONNRESET)}},
			wantName: "connection_reset",
		},
		{
			name:     "unsupported scheme",
			err:      &url.Error{Op: "Get", URL: "gopher://localhost:15984/", Err: errors.New(`unsupported protocol scheme "gopher"`)},
			wantName: "unsupported_scheme",
		},
		{
			name:     "unexpected eof",
			err:      &url.Error{Op: "Get", URL: "http://localhost:15984/", Err: io.ErrUnexpectedEOF},
			wantName: "network",
		},
		{
			name:     "bare net.OpError",
			err:      &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("i/o timeout")},
			wantName: "network",
		},
		{
			name:     "decoding a truncated body",
			err:      fmt.Errorf("unexpected end of JSON input"),
			wantName: "network",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, ok := AsError(Wrap(tc.err, "read", "server localhost:15984"))
			if !ok {
				t.Fatalf("Wrap(%v) did not produce an *Error", tc.err)
			}
			if e.Status != StatusUnreachable {
				t.Errorf("Status = %d, want StatusUnreachable (%d)", e.Status, StatusUnreachable)
			}
			if e.Name != tc.wantName {
				t.Errorf("Name = %q, want %q", e.Name, tc.wantName)
			}
			if e.Reason == "" {
				t.Error("Reason is empty")
			}
		})
	}
}

// TestWrapKeepsAGenuineHTTPStatus proves the classification above does not
// swallow a real server answer: those arrive as an *Error from doDecode and
// must pass through Wrap untouched.
func TestWrapKeepsAGenuineHTTPStatus(t *testing.T) {
	in := NewError(500, "server_error", "internal error", "", "")
	e, ok := AsError(Wrap(in, "read", `database "mydb"`))
	if !ok {
		t.Fatal("Wrap did not produce an *Error")
	}
	if e.Status != 500 || e.Name != "server_error" {
		t.Errorf("Status/Name = %d/%q, want 500/server_error", e.Status, e.Name)
	}
	if e.Op != "read" || e.Target != `database "mydb"` {
		t.Errorf("Op/Target = %q/%q", e.Op, e.Target)
	}
}
