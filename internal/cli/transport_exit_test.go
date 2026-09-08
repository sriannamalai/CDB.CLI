package cli

import (
	"crypto/x509"
	"errors"
	"net"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/couch"
	"github.com/sriannamalai/CDB.CLI/internal/render"
)

// TestExitCodeForTransportFailures pins spec section 11's exit codes for the
// connection failures that are neither ECONNREFUSED nor DNS: a script
// branching on exit 3 must catch "https against an http port" too, and the
// message must not blame the server.
func TestExitCodeForTransportFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"tls handshake timeout", &url.Error{Op: "Get", URL: "https://localhost:15984/", Err: errors.New("net/http: TLS handshake timeout")}},
		{"unknown certificate authority", &url.Error{Op: "Get", URL: "https://localhost:15984/", Err: x509.UnknownAuthorityError{}}},
		{"connection reset", &url.Error{Op: "Get", URL: "http://localhost:15984/", Err: &net.OpError{Op: "read", Net: "tcp", Err: os.NewSyscallError("read", syscall.ECONNRESET)}}},
		{"unsupported scheme", &url.Error{Op: "Get", URL: "gopher://localhost:15984/", Err: errors.New(`unsupported protocol scheme "gopher"`)}},
		{"i/o timeout", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("i/o timeout")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := couch.Wrap(tc.err, "list", "databases on localhost:15984")
			if got := ExitCode(err); got != ExitConnection {
				t.Errorf("ExitCode = %d, want %d (%v)", got, ExitConnection, err)
			}
			msg := render.ErrorMessage(err, false)
			if msg == "" || strings.Contains(msg, "The server had a problem") {
				t.Errorf("message blames the server: %q", msg)
			}
		})
	}
}
