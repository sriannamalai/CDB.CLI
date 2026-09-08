package couch

import (
	"context"
	"os"
	"testing"
)

// testURL returns the integration server URL, or skips the test.
func testURL(t *testing.T) string {
	t.Helper()
	u := os.Getenv("CDB_TEST_URL")
	if u == "" {
		t.Skip("set CDB_TEST_URL (e.g. http://localhost:15984/) to run integration tests")
	}
	return u
}

func TestIntegrationSessionAuth(t *testing.T) {
	base := testURL(t)
	c, err := New(Config{URL: base, Auth: AuthSession, Username: envOr("CDB_TEST_USER", "admin"), Secret: envOr("CDB_TEST_PASSWORD", "password")})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	info, err := c.ServerInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Version == "" {
		t.Fatal("server reported no version")
	}
	sess, err := c.Session(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sess.Name == "" {
		t.Fatal("session reported no user name")
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
