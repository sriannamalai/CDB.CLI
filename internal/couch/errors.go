package couch

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"syscall"

	kivik "github.com/go-kivik/kivik/v4"
)

// StatusUnreachable marks an error that never reached the server.
const StatusUnreachable = 0

// Error is the single error type every couch.Client method returns.
type Error struct {
	// Status is the HTTP status code, or StatusUnreachable when the request
	// never reached a server.
	Status int
	// Name is the CouchDB error name ("not_found", "conflict", ...) when the
	// server sent one, "connection_refused", "no_such_host" or "tls" for
	// transport failures, otherwise "".
	Name string
	// Reason is the server's reason string, or the transport error text.
	Reason string
	// Op is the operation in plain words: "read", "write", "delete", "list".
	Op string
	// Target is what the operation was about, e.g. `document "doc1" in "mydb"`.
	Target string

	err error
}

func (e *Error) Error() string {
	switch {
	case e.Target != "" && e.Reason != "":
		return fmt.Sprintf("%s %s: %s", e.Op, e.Target, e.Reason)
	case e.Target != "":
		return fmt.Sprintf("%s %s failed", e.Op, e.Target)
	default:
		return e.Reason
	}
}

func (e *Error) Unwrap() error { return e.err }

// Wrap converts any error from Kivik or net/http into an *Error. It returns
// nil for a nil error and never double-wraps.
func Wrap(err error, op, target string) error {
	if err == nil {
		return nil
	}
	var already *Error
	if errors.As(err, &already) {
		if already.Op != "" && already.Target != "" {
			return already
		}
		// Fill in the blanks on a copy. Mutating the error in place would race
		// with any other goroutine holding it, and would let the first caller's
		// Op and Target silently win over every later one.
		filled := *already
		if filled.Op == "" {
			filled.Op = op
		}
		if filled.Target == "" {
			filled.Target = target
		}
		return &filled
	}
	e := &Error{Op: op, Target: target, err: err}
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		e.Status = StatusUnreachable
		e.Name = "connection_refused"
		e.Reason = "connection refused"
	case isDNSError(err):
		e.Status = StatusUnreachable
		e.Name = "no_such_host"
		e.Reason = "host not found"
	case isTLSError(err):
		e.Status = StatusUnreachable
		e.Name = "tls"
		e.Reason = trimPrefixes(err.Error())
	case errors.Is(err, context.Canceled):
		// Must come before the default branch: kivik.HTTPStatus reports 500 for
		// any error it does not recognise, which would make an interrupted
		// request indistinguishable from a real server failure.
		e.Status = StatusUnreachable
		e.Name = "canceled"
		e.Reason = "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		e.Status = StatusUnreachable
		e.Name = "timeout"
		e.Reason = "timed out"
	default:
		e.Status = kivik.HTTPStatus(err)
		e.Reason = trimPrefixes(err.Error())
		e.Name = nameForStatus(e.Status)
	}
	return e
}

// AsError extracts an *Error from err, if there is one.
func AsError(err error) (*Error, bool) {
	var e *Error
	ok := errors.As(err, &e)
	return e, ok
}

// NewError builds an *Error from a decoded CouchDB error body.
func NewError(status int, name, reason, op, target string) *Error {
	return &Error{Status: status, Name: name, Reason: reason, Op: op, Target: target}
}

// unauthorizedTarget builds the target every 401 carries: enough for the
// operator-facing message (internal/render's plainSentence) to name the real
// user and host, instead of whatever the caller's own operation was about.
func unauthorizedTarget(username, host string) string {
	return fmt.Sprintf("user %q at %s", username, host)
}

func isDNSError(err error) bool {
	var d *net.DNSError
	return errors.As(err, &d)
}

func isTLSError(err error) bool {
	return strings.Contains(err.Error(), "x509:") || strings.Contains(err.Error(), "tls:")
}

// statusTexts is the set of canonical HTTP status texts, built once so
// trimPrefixes does not walk 500 status codes on every error.
var statusTexts = func() map[string]struct{} {
	m := make(map[string]struct{}, 64)
	for status := 100; status < 600; status++ {
		if text := http.StatusText(status); text != "" {
			m[text] = struct{}{}
		}
	}
	return m
}()

// trimPrefixes strips the `Get "url": ` and `<Status Text>: ` decorations Kivik
// puts in front of the server's reason string.
func trimPrefixes(msg string) string {
	for _, verb := range []string{"Get ", "Post ", "Put ", "Delete ", "Head ", "Patch "} {
		if strings.HasPrefix(msg, verb+`"`) {
			if j := strings.Index(msg, `": `); j >= 0 {
				msg = msg[j+3:]
			}
			break
		}
	}
	if i := strings.Index(msg, ": "); i > 0 {
		if _, ok := statusTexts[msg[:i]]; ok {
			return msg[i+2:]
		}
	}
	if _, ok := statusTexts[msg]; ok {
		return strings.ToLower(msg)
	}
	return msg
}

func nameForStatus(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "unauthorized"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusConflict:
		return "conflict"
	case http.StatusPreconditionFailed:
		return "file_exists"
	case http.StatusBadRequest:
		return "bad_request"
	}
	return ""
}
