package couch

import (
	"strings"
	"testing"
)

// TestNewKeepsURLCredentialsOutOfItsErrors pins the global constraint that a
// secret never reaches an error message. Both malformed-URL branches of New
// format the operator-supplied URL, which may carry userinfo.
func TestNewKeepsURLCredentialsOutOfItsErrors(t *testing.T) {
	for _, tc := range []struct{ name, url string }{
		{"unparseable", "http://admin:hunter2@local host:5984/"},
		{"wrong scheme", "ftp://admin:hunter2@localhost:5984/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(Config{URL: tc.url})
			if err == nil {
				t.Fatalf("New(%q) succeeded, want an error", tc.url)
			}
			if strings.Contains(err.Error(), "hunter2") {
				t.Errorf("error leaks the password: %q", err.Error())
			}
			if !strings.Contains(err.Error(), "host:5984") {
				t.Errorf("error does not name the host: %q", err.Error())
			}
		})
	}
}

func TestRedactURL(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"http://localhost:5984/", "http://localhost:5984/"},
		{"http://admin:hunter2@localhost:5984/", "http://localhost:5984/"},
		{"ftp://admin:hunter2@localhost:5984/", "ftp://localhost:5984/"},
		{"http://admin:hunter2@local host:5984/", "http://local host:5984/"},
		// No scheme: url.Parse reads "admin" as the scheme and never sets
		// User, so the userinfo has to be cut textually.
		{"admin:hunter2@localhost:5984", "localhost:5984"},
		{"admin@localhost:5984", "localhost:5984"},
		{"//admin:hunter2@localhost:5984/db", "//localhost:5984/db"},
		// An "@" past the authority belongs to the path, not to a credential.
		{"http://localhost:5984/mail@host", "http://localhost:5984/mail@host"},
		{"not a url at all", "not a url at all"},
		{"", ""},
	} {
		if got := RedactURL(tc.in); got != tc.want {
			t.Errorf("RedactURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
