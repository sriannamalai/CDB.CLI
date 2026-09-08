package couch

import (
	"strings"
	"testing"
)

func TestReplicationURLDefaultsToTheClientURL(t *testing.T) {
	c, err := New(Config{URL: "http://localhost:15984/", Auth: AuthNone})
	if err != nil {
		t.Fatal(err)
	}
	if got := c.ReplicationURL(); got != "http://localhost:15984" {
		t.Errorf("ReplicationURL() = %q, want the client URL", got)
	}
	endpoint := c.ReplicationEndpoint("mydb")
	if got := endpoint["url"]; got != "http://localhost:15984/mydb" {
		t.Errorf("endpoint url = %v", got)
	}
}

func TestReplicationURLOverridesTheEndpointOnly(t *testing.T) {
	c, err := New(Config{
		URL:            "http://localhost:15984",
		Auth:           AuthSession,
		Username:       "admin",
		Secret:         "password",
		ReplicationURL: "http://couchdb:5984/",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := c.URL(); got != "http://localhost:15984" {
		t.Errorf("URL() = %q; the client URL must not move", got)
	}
	if got := c.ReplicationURL(); got != "http://couchdb:5984" {
		t.Errorf("ReplicationURL() = %q, want the override with its trailing slash trimmed", got)
	}
	endpoint := c.ReplicationEndpoint("my db")
	if got := endpoint["url"]; got != "http://couchdb:5984/my%20db" {
		t.Errorf("endpoint url = %v", got)
	}
	// The credential object is untouched by the override.
	auth, ok := endpoint["auth"].(map[string]any)
	if !ok {
		t.Fatalf("endpoint has no auth object: %v", endpoint)
	}
	basic := auth["basic"].(map[string]any)
	if basic["username"] != "admin" || basic["password"] != "password" {
		t.Errorf("auth.basic = %v", basic)
	}
}

func TestReplicationURLValidation(t *testing.T) {
	for _, tc := range []struct{ name, url, want string }{
		{"relative", "couchdb:5984", "The replication URL must be an absolute http or https URL."},
		{"wrong scheme", "ftp://couchdb:5984", "The replication URL must be an absolute http or https URL."},
		{"no host", "http://", "The replication URL must be an absolute http or https URL."},
		{"userinfo", "http://admin:pw@couchdb:5984", "The replication URL must not contain a user name or password; cdb sends the profile's credentials in the replication document instead."},
		{"query", "http://couchdb:5984?a=1", "The replication URL must be a bare server address, with no query string or fragment."},
		{"fragment", "http://couchdb:5984#f", "The replication URL must be a bare server address, with no query string or fragment."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(Config{URL: "http://localhost:15984", Auth: AuthNone, ReplicationURL: tc.url})
			if err == nil {
				t.Fatalf("New accepted %q", tc.url)
			}
			if err.Error() != tc.want {
				t.Errorf("error = %q, want %q", err.Error(), tc.want)
			}
			if strings.Contains(err.Error(), "pw") {
				t.Error("the error echoed the value, which may hold a password")
			}
		})
	}
}
