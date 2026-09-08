package couch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// integrationClient returns a session-authenticated client against the
// CDB_TEST_URL server, or skips.
func integrationClient(t *testing.T) *Client {
	t.Helper()
	c, err := New(Config{
		URL:      testURL(t),
		Auth:     AuthSession,
		Username: envOr("CDB_TEST_USER", "admin"),
		Secret:   envOr("CDB_TEST_PASSWORD", "password"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// integrationDB creates a throwaway database and removes it when the test
// ends, whether it passed or failed.
func integrationDB(t *testing.T, c *Client, name string) string {
	t.Helper()
	ctx := context.Background()
	_ = c.DestroyDatabase(ctx, name)
	if err := c.CreateDatabase(ctx, name, false, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.DestroyDatabase(context.Background(), name) })
	return name
}

// A CouchDB session cookie expires, and the server restarts. sessionTransport
// is meant to notice the 401, log in again, and replay the request — including
// its body. Only a real server exercises the whole path: its cookie format,
// its 401 shape, and its acceptance of the replay.
func TestIntegrationSessionReauthenticatesAfterAStaleCookie(t *testing.T) {
	c := integrationClient(t)
	db := integrationDB(t, c, "cdb_it_reauth")
	ctx := context.Background()

	tr, ok := c.hc.Transport.(*sessionTransport)
	if !ok {
		t.Fatalf("transport is %T, want *sessionTransport", c.hc.Transport)
	}
	// Prime the session, then poison it. The cookie must stay well formed —
	// CouchDB answers 400 "Malformed AuthSession cookie" for a garbage one,
	// which is not the case being tested — so the signature is broken instead,
	// which is what a server restarted with a new secret leaves behind, and
	// which the server answers with a 401.
	if _, err := c.DatabaseInfo(ctx, db); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(strings.TrimRight(testURL(t), "/"))
	if err != nil {
		t.Fatal(err)
	}
	var live *http.Cookie
	for _, ck := range tr.jar.Cookies(u) {
		if ck.Name == "AuthSession" {
			live = ck
		}
	}
	if live == nil {
		t.Fatal("the server set no AuthSession cookie")
	}
	tr.jar.SetCookies(u, []*http.Cookie{{Name: "AuthSession", Value: breakSignature(live.Value), Path: "/"}})
	tr.mu.Lock()
	tr.authed = true
	tr.mu.Unlock()

	// A request with a body, so the replay has to rebuild one.
	doc := json.RawMessage(`{"after":"reauth"}`)
	if _, err := c.PutDocument(ctx, db, "replayed", doc, ""); err != nil {
		t.Fatalf("the request was not retried after the stale cookie: %v", err)
	}
	if !tr.authenticated() {
		t.Error("the transport did not log in again")
	}
	back, _, err := c.GetDocument(ctx, db, "replayed", GetOptions{})
	if err != nil {
		t.Fatalf("the replayed request did not reach the server: %v", err)
	}
	if !strings.Contains(string(back), `"after":"reauth"`) {
		t.Errorf("stored document = %s, want the replayed body", back)
	}
}

// breakSignature alters the last character of a base64url AuthSession cookie,
// leaving its shape intact and its HMAC wrong.
func breakSignature(v string) string {
	if v == "" {
		return v
	}
	last := v[len(v)-1]
	repl := byte('A')
	if last == 'A' {
		repl = 'B'
	}
	return v[:len(v)-1] + string(repl)
}

// Paging uses startkey_docid and never skip, and asks for one row more than
// the page so it knows where the next page begins. Against the real server
// that has to walk every document exactly once, with no gap and no repeat.
func TestIntegrationAllDocsPagesWithoutGapsOrRepeats(t *testing.T) {
	c := integrationClient(t)
	db := integrationDB(t, c, "cdb_it_paging")
	ctx := context.Background()

	const total = 25
	docs := make([]json.RawMessage, 0, total)
	for i := 0; i < total; i++ {
		docs = append(docs, json.RawMessage(fmt.Sprintf(`{"_id":"doc%03d","n":%d}`, i, i)))
	}
	if failures, err := c.BulkDocs(ctx, db, docs, true); err != nil || len(failures) > 0 {
		t.Fatalf("seeding: %v %v", err, failures)
	}

	seen := map[string]int{}
	var order []string
	opts := AllDocsOptions{Limit: 7}
	for pages := 0; ; pages++ {
		if pages > total {
			t.Fatal("paging did not terminate")
		}
		page, err := c.AllDocs(ctx, db, opts)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Rows) > opts.Limit {
			t.Fatalf("page held %d rows, want at most the limit of %d", len(page.Rows), opts.Limit)
		}
		for _, row := range page.Rows {
			seen[row.ID]++
			order = append(order, row.ID)
		}
		if page.NextStartKeyDocID == "" {
			break
		}
		opts.StartKeyDocID = page.NextStartKeyDocID
	}
	if len(order) != total {
		t.Errorf("saw %d rows over all pages, want %d", len(order), total)
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("%s appeared %d times across the pages", id, n)
		}
	}
	for i := 0; i < total; i++ {
		if _, ok := seen[fmt.Sprintf("doc%03d", i)]; !ok {
			t.Errorf("doc%03d was skipped by paging", i)
		}
	}
}

// restore writes with new_edits=false so a dump keeps the revisions it
// recorded. The Kivik memory driver silently dropped that flag, which is one
// reason it was rejected as a test double — so the behaviour is pinned against
// a real server.
func TestIntegrationBulkDocsWithoutNewEditsKeepsTheRevision(t *testing.T) {
	c := integrationClient(t)
	db := integrationDB(t, c, "cdb_it_newedits")
	ctx := context.Background()

	const rev = "3-0123456789abcdef0123456789abcdef"
	doc := json.RawMessage(`{"_id":"kept","_rev":"` + rev + `","_revisions":{"start":3,"ids":["0123456789abcdef0123456789abcdef","11111111111111111111111111111111","22222222222222222222222222222222"]},"v":1}`)
	failures, err := c.BulkDocs(ctx, db, []json.RawMessage{doc}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(failures) > 0 {
		t.Fatalf("the server rejected the document: %+v", failures)
	}
	got, gotRev, err := c.GetDocument(ctx, db, "kept", GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if gotRev != rev {
		t.Errorf("stored revision = %q, want the one from the dump (%q)", gotRev, rev)
	}
	if !strings.Contains(string(got), `"v":1`) {
		t.Errorf("stored document = %s", got)
	}
}
