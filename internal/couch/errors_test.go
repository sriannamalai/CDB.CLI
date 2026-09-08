package couch

import (
	"context"
	"fmt"
	"testing"
)

// TestWrapClassifiesContextErrors guards against context errors being reported
// as a server failure, which would make an interrupted or timed-out request
// look like a real 500 and pick the wrong exit code.
func TestWrapClassifiesContextErrors(t *testing.T) {
	for _, tc := range []struct {
		name              string
		err               error
		wantName, wantWhy string
	}{
		{"canceled", context.Canceled, "canceled", "canceled"},
		{"deadline exceeded", context.DeadlineExceeded, "timeout", "timed out"},
		{"wrapped canceled", fmt.Errorf(`Get "http://localhost:5984/": %w`, context.Canceled), "canceled", "canceled"},
		{"wrapped deadline", fmt.Errorf(`Get "http://localhost:5984/": %w`, context.DeadlineExceeded), "timeout", "timed out"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, ok := AsError(Wrap(tc.err, "read", "server localhost:5984"))
			if !ok {
				t.Fatalf("Wrap(%v) did not produce an *Error", tc.err)
			}
			if e.Status != StatusUnreachable {
				t.Errorf("Status = %d, want StatusUnreachable (%d)", e.Status, StatusUnreachable)
			}
			if e.Name != tc.wantName {
				t.Errorf("Name = %q, want %q", e.Name, tc.wantName)
			}
			if e.Reason != tc.wantWhy {
				t.Errorf("Reason = %q, want %q", e.Reason, tc.wantWhy)
			}
		})
	}
}

func TestTrimPrefixes(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Not Found: missing", "missing"},
		{"Not Found: Database does not exist.", "Database does not exist."},
		{"Conflict: Document update conflict.", "Document update conflict."},
		{"Not Found", "not found"},
		{`Get "http://localhost:5984/_all_dbs": Unauthorized: Name or password is incorrect.`, "Name or password is incorrect."},
		{`Get "http://localhost:1/_all_dbs": dial tcp [::1]:1: connect: connection refused`, "dial tcp [::1]:1: connect: connection refused"},
	} {
		if got := trimPrefixes(tc.in); got != tc.want {
			t.Errorf("trimPrefixes(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
