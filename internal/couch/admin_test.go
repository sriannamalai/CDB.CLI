package couch

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
)

func TestAsAdminRetargetsOnlyA403(t *testing.T) {
	forbidden := NewError(403, "unauthorized", "You are not a server admin.", "read", "server localhost:5984")
	got, ok := AsError(AsAdmin(forbidden, "listing active tasks"))
	if !ok || got.Op != AdminOp || got.Target != "listing active tasks" {
		t.Fatalf("403 became %#v", got)
	}
	if forbidden.Op != "read" || forbidden.Target != "server localhost:5984" {
		t.Error("AsAdmin mutated the error it was given instead of copying it")
	}
	notFound := NewError(404, "not_found", "missing", "read", `database "mydb"`)
	if AsAdmin(notFound, "listing active tasks") != error(notFound) {
		t.Error("a 404 was rewritten")
	}
	if AsAdmin(nil, "listing active tasks") != nil {
		t.Error("nil was rewritten")
	}
}

// mustClient is the unauthenticated client every case in this file uses.
func mustClient(t *testing.T, srv *couchtest.Server) *Client {
	t.Helper()
	c, err := New(Config{URL: srv.URL(), Auth: AuthNone})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestActiveTasksDecodesEachTypeAndRedactsEndpoints(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_active_tasks", 200, `[
		{"node":"nonode@nohost","pid":"<0.1.0>","type":"replication","continuous":true,
		 "doc_id":"movies-job","source":"http://alice:s3cret@remote:5984/movies/",
		 "target":"http://localhost:5984/movies-backup/","started_on":1757836800,
		 "updated_on":1757836860,"changes_done":12,"total_changes":100},
		{"node":"nonode@nohost","pid":"<0.2.0>","type":"database_compaction",
		 "database":"movies","progress":84,"started_on":1757836800,"updated_on":1757836870,
		 "changes_done":840,"total_changes":1000},
		{"node":"nonode@nohost","pid":"<0.3.0>","type":"indexer","database":"movies",
		 "design_document":"_design/by_year","progress":0,"started_on":1757836800,
		 "updated_on":1757836880}]`)
	c := mustClient(t, srv)

	tasks, err := c.ActiveTasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 3 {
		t.Fatalf("got %d tasks, want 3", len(tasks))
	}

	rep := tasks[0]
	if rep.Type != "replication" || rep.DocID != "movies-job" || !rep.Continuous {
		t.Errorf("replication task = %#v", rep)
	}
	if rep.Source != "http://remote:5984/movies/" {
		t.Errorf("source = %q; the password was not redacted", rep.Source)
	}
	if rep.HasProgress {
		t.Error("a task with no progress member reported one")
	}
	if rep.StartedOn != 1757836800 || rep.UpdatedOn != 1757836860 {
		t.Errorf("timestamps = %d/%d", rep.StartedOn, rep.UpdatedOn)
	}
	if strings.Contains(string(rep.Raw), "s3cret") {
		t.Fatal("the raw task still carries the password")
	}

	comp := tasks[1]
	if comp.Database != "movies" || !comp.HasProgress || comp.Progress != 84 {
		t.Errorf("compaction task = %#v", comp)
	}

	idx := tasks[2]
	if idx.DesignDocument != "_design/by_year" || !idx.HasProgress || idx.Progress != 0 {
		t.Errorf("indexer task = %#v", idx)
	}
	var back map[string]any
	if err := json.Unmarshal(idx.Raw, &back); err != nil {
		t.Fatal(err)
	}
	if back["pid"] != "<0.3.0>" {
		t.Errorf("Raw lost fields cdb does not decode: %s", idx.Raw)
	}
}

func TestActiveTasksForbiddenNamesTheAction(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_active_tasks", 403, `{"error":"unauthorized","reason":"You are not a server admin."}`)
	c := mustClient(t, srv)
	_, err := c.ActiveTasks(context.Background())
	e, ok := AsError(err)
	if !ok || e.Op != AdminOp || e.Target != "listing active tasks" {
		t.Fatalf("error = %#v", e)
	}
}

func TestConfigReadsWholeSectionAndKey(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_node/_local/_config", 200,
		`{"log":{"level":"info","writer":"stderr"},"chttpd":{"port":"5984"}}`)
	srv.JSON("GET", "/_node/_local/_config/log", 200, `{"level":"info","writer":"stderr"}`)
	srv.JSON("GET", "/_node/_local/_config/log/level", 200, `"info"`)
	c := mustClient(t, srv)
	ctx := context.Background()

	whole, err := c.Config(ctx, "_local", "", "")
	if err != nil {
		t.Fatal(err)
	}
	want := []ConfigEntry{
		{"chttpd", "port", "5984"},
		{"log", "level", "info"},
		{"log", "writer", "stderr"},
	}
	if len(whole) != len(want) {
		t.Fatalf("got %d entries, want %d: %#v", len(whole), len(want), whole)
	}
	for i := range want {
		if whole[i] != want[i] {
			t.Errorf("entry %d = %#v, want %#v", i, whole[i], want[i])
		}
	}

	section, err := c.Config(ctx, "_local", "log", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(section) != 2 || section[0] != (ConfigEntry{"log", "level", "info"}) {
		t.Errorf("section = %#v", section)
	}

	one, err := c.Config(ctx, "_local", "log", "level")
	if err != nil {
		t.Fatal(err)
	}
	if len(one) != 1 || one[0] != (ConfigEntry{"log", "level", "info"}) {
		t.Errorf("key = %#v", one)
	}
}

func TestConfigKeyIsPathEscaped(t *testing.T) {
	// couchtest routes and records on r.URL.Path, which net/http has already
	// unescaped, so the route is registered under the decoded path and the
	// escaping is asserted on the wire form inside the handler. Registering
	// "/...o%2Fbrien" would simply never match.
	srv := couchtest.New(t)
	var escaped string
	srv.On("GET", "/_node/_local/_config/admins/o/brien", func(w http.ResponseWriter, r *http.Request) {
		escaped = r.URL.EscapedPath()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`"-pbkdf2-deadbeef"`))
	})
	c := mustClient(t, srv)
	if _, err := c.Config(context.Background(), "_local", "admins", "o/brien"); err != nil {
		t.Fatal(err)
	}
	if escaped != "/_node/_local/_config/admins/o%2Fbrien" {
		t.Errorf("request path = %q; the key was not escaped", escaped)
	}
}

func TestConfigDecodesANonStringValue(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_node/_local/_config/cluster/q", 200, `2`)
	c := mustClient(t, srv)
	got, err := c.Config(context.Background(), "_local", "cluster", "q")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Value != "2" {
		t.Errorf("entries = %#v", got)
	}
}

func TestSetAndDeleteConfigReturnTheOldValue(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("PUT", "/_node/_local/_config/log/level", 200, `"info"`)
	srv.JSON("DELETE", "/_node/_local/_config/log/level", 200, `"debug"`)
	c := mustClient(t, srv)
	ctx := context.Background()

	old, err := c.SetConfig(ctx, "_local", "log", "level", "debug")
	if err != nil {
		t.Fatal(err)
	}
	if old != "info" {
		t.Errorf("old = %q, want %q", old, "info")
	}
	if body := string(srv.Last("PUT", "/_node/_local/_config/log/level").Body); strings.TrimSpace(body) != `"debug"` {
		t.Errorf("PUT body = %s, want a bare JSON string", body)
	}

	old, err = c.DeleteConfig(ctx, "_local", "log", "level")
	if err != nil {
		t.Fatal(err)
	}
	if old != "debug" {
		t.Errorf("old = %q", old)
	}
}

func TestReloadConfigPostsToReload(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/_node/_local/_config/_reload", 200, `{"ok":true}`)
	c := mustClient(t, srv)
	if err := c.ReloadConfig(context.Background(), "_local"); err != nil {
		t.Fatal(err)
	}
	if srv.Last("POST", "/_node/_local/_config/_reload") == nil {
		t.Fatal("nothing was posted")
	}
}

func TestConfigForbiddenNamesTheAction(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_node/_local/_config", 403, `{"error":"unauthorized","reason":"You are not a server admin."}`)
	srv.JSON("PUT", "/_node/_local/_config/log/level", 403, `{"error":"unauthorized","reason":"You are not a server admin."}`)
	c := mustClient(t, srv)
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		call func() error
		want string
	}{
		{"read", func() error { _, err := c.Config(ctx, "_local", "", ""); return err }, "reading configuration"},
		{"write", func() error { _, err := c.SetConfig(ctx, "_local", "log", "level", "debug"); return err }, "changing configuration"},
	} {
		e, ok := AsError(tc.call())
		if !ok || e.Op != AdminOp || e.Target != tc.want {
			t.Errorf("%s: error = %#v, want target %q", tc.name, e, tc.want)
		}
	}
}

func TestMembershipListsNodes(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_membership", 200,
		`{"all_nodes":["node1@127.0.0.1","node2@127.0.0.1"],"cluster_nodes":["node1@127.0.0.1"]}`)
	c := mustClient(t, srv)
	m, err := c.Membership(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(m.AllNodes) != 2 || m.AllNodes[1] != "node2@127.0.0.1" || len(m.ClusterNodes) != 1 {
		t.Errorf("membership = %#v", m)
	}
}

func TestClusterSetupStateAndItsAbsence(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_cluster_setup", 200, `{"state":"cluster_enabled"}`)
	c := mustClient(t, srv)
	state, err := c.ClusterSetupState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state != "cluster_enabled" {
		t.Errorf("state = %q", state)
	}

	missing := couchtest.New(t)
	missing.JSON("GET", "/_cluster_setup", 404, `{"error":"not_found","reason":"missing"}`)
	c2 := mustClient(t, missing)
	if _, err := c2.ClusterSetupState(context.Background()); err == nil {
		t.Fatal("a 404 was swallowed; the command decides what unavailable means")
	} else if e, ok := AsError(err); !ok || e.Status != 404 {
		t.Fatalf("error = %#v", e)
	}
}

func TestEnableSingleNodePostsTheDocumentedBody(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/_cluster_setup", 201, `{"ok":true}`)
	c := mustClient(t, srv)
	err := c.EnableSingleNode(context.Background(), SingleNodeSetup{
		Username: "admin", Password: "password", BindAddress: "0.0.0.0", Port: 5984,
	})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(srv.Last("POST", "/_cluster_setup").Body, &body); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"action": "enable_single_node", "username": "admin", "password": "password",
		"bind_address": "0.0.0.0", "port": float64(5984),
	}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("body[%q] = %v, want %v", k, body[k], v)
		}
	}
	if len(body) != len(want) {
		t.Errorf("body has %d members, want %d: %v", len(body), len(want), body)
	}
}

func TestCompactionEndpoints(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/movies/_compact", 202, `{"ok":true}`)
	srv.JSON("POST", "/movies/_compact/by_year", 202, `{"ok":true}`)
	srv.JSON("POST", "/movies/_view_cleanup", 202, `{"ok":true}`)
	c := mustClient(t, srv)
	ctx := context.Background()
	if err := c.Compact(ctx, "movies"); err != nil {
		t.Fatal(err)
	}
	if ct := srv.Last("POST", "/movies/_compact").Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q; CouchDB refuses _compact without it", ct)
	}
	if err := c.CompactView(ctx, "movies", "_design/by_year"); err != nil {
		t.Fatal(err)
	}
	if err := c.ViewCleanup(ctx, "movies"); err != nil {
		t.Fatal(err)
	}
}

func TestCompactForbiddenNamesTheDatabase(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/movies/_compact", 403, `{"error":"unauthorized","reason":"You are not a server admin."}`)
	c := mustClient(t, srv)
	e, ok := AsError(c.Compact(context.Background(), "movies"))
	if !ok || e.Op != AdminOp || e.Target != `compacting "movies"` {
		t.Fatalf("error = %#v", e)
	}
}

// TestAsAdminRetargetsTheNonAdminUnauthorized covers what the live servers
// actually answer: every server-level administrative endpoint refuses a
// perfectly good non-admin login with 401, not 403, and only the database-level
// compaction endpoints answer 403. A 401 that is a real credential failure must
// still read as one.
func TestAsAdminRetargetsTheNonAdminUnauthorized(t *testing.T) {
	notAdmin := NewError(401, "unauthorized", "You are not a server admin.", "read", `user "bob" at localhost:5984`)
	got, ok := AsError(AsAdmin(notAdmin, "listing active tasks"))
	if !ok || got.Op != AdminOp || got.Target != "listing active tasks" {
		t.Fatalf("401 became %#v", got)
	}
	if notAdmin.Op != "read" {
		t.Error("AsAdmin mutated the error it was given instead of copying it")
	}
	badPassword := NewError(401, "unauthorized", "Name or password is incorrect.", "read", `user "bob" at localhost:5984`)
	if AsAdmin(badPassword, "listing active tasks") != error(badPassword) {
		t.Error("a real login failure was rewritten as an administrative refusal")
	}
}

// TestAdminEndpointsNameTheActionOnBothRefusals asserts every endpoint group's
// refusal, in both the shapes CouchDB uses.
func TestAdminEndpointsNameTheActionOnBothRefusals(t *testing.T) {
	const notAdmin = `{"error":"unauthorized","reason":"You are not a server admin."}`
	for _, status := range []int{401, 403} {
		srv := couchtest.New(t)
		srv.JSON("GET", "/_active_tasks", status, notAdmin)
		srv.JSON("GET", "/_node/_local/_config", status, notAdmin)
		srv.JSON("PUT", "/_node/_local/_config/log/level", status, notAdmin)
		srv.JSON("GET", "/_membership", status, notAdmin)
		srv.JSON("GET", "/_cluster_setup", status, notAdmin)
		srv.JSON("POST", "/_cluster_setup", status, notAdmin)
		srv.JSON("POST", "/movies/_compact", status, notAdmin)
		srv.JSON("POST", "/movies/_compact/by_year", status, notAdmin)
		srv.JSON("POST", "/movies/_view_cleanup", status, notAdmin)
		c := mustClient(t, srv)
		ctx := context.Background()
		for _, tc := range []struct {
			name string
			call func() error
			want string
		}{
			{"active tasks", func() error { _, err := c.ActiveTasks(ctx); return err }, "listing active tasks"},
			{"config read", func() error { _, err := c.Config(ctx, "_local", "", ""); return err }, "reading configuration"},
			{"config write", func() error { _, err := c.SetConfig(ctx, "_local", "log", "level", "debug"); return err }, "changing configuration"},
			{"membership", func() error { _, err := c.Membership(ctx); return err }, "reading cluster membership"},
			{"cluster setup", func() error { _, err := c.ClusterSetupState(ctx); return err }, "reading cluster setup"},
			{"enable single node", func() error {
				return c.EnableSingleNode(ctx, SingleNodeSetup{Username: "admin", Password: "password", BindAddress: "0.0.0.0", Port: 5984})
			}, "cluster setup"},
			{"compact", func() error { return c.Compact(ctx, "movies") }, `compacting "movies"`},
			{"compact view", func() error { return c.CompactView(ctx, "movies", "by_year") }, `compacting "movies"`},
			{"view cleanup", func() error { return c.ViewCleanup(ctx, "movies") }, `cleaning up views in "movies"`},
		} {
			e, ok := AsError(tc.call())
			if !ok || e.Op != AdminOp || e.Target != tc.want {
				t.Errorf("status %d, %s: error = %#v, want op %q target %q", status, tc.name, e, AdminOp, tc.want)
			}
		}
	}
}

func TestConfigRefusesAKeyWithoutASection(t *testing.T) {
	srv := couchtest.New(t)
	c := mustClient(t, srv)
	if _, err := c.Config(context.Background(), "_local", "", "level"); err == nil {
		t.Fatal("a key with no section was accepted; it would read a section named \"level\"")
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("a request was sent anyway: %v", srv.Requests())
	}
}

func TestSecurityReadsAndWritesTheWholeDocument(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/movies/_security", 200,
		`{"admins":{"names":["alice"],"roles":["ops"]},"members":{"names":[],"roles":["reader"]},"cloudant":{"x":1}}`)
	srv.JSON("PUT", "/movies/_security", 200, `{"ok":true}`)
	c := mustClient(t, srv)
	ctx := context.Background()

	sec, err := c.Security(ctx, "movies")
	if err != nil {
		t.Fatal(err)
	}
	if len(sec.Admins.Names) != 1 || sec.Admins.Names[0] != "alice" || sec.Admins.Roles[0] != "ops" {
		t.Errorf("admins = %#v", sec.Admins)
	}
	if len(sec.Members.Names) != 0 || sec.Members.Roles[0] != "reader" {
		t.Errorf("members = %#v", sec.Members)
	}

	sec.Members.Names = append(sec.Members.Names, "bob")
	if err := c.SetSecurity(ctx, "movies", sec); err != nil {
		t.Fatal(err)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(srv.Last("PUT", "/movies/_security").Body, &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["cloudant"]; !ok {
		t.Error("a member of the document cdb does not understand was dropped")
	}
	var members SecurityLevel
	if err := json.Unmarshal(body["members"], &members); err != nil {
		t.Fatal(err)
	}
	if len(members.Names) != 1 || members.Names[0] != "bob" {
		t.Errorf("members = %#v", members)
	}
	if members.Roles == nil {
		t.Error("an empty list was written as null; CouchDB wants an array")
	}
}

func TestSessionCredentialsOnlyAnswerForASessionLogin(t *testing.T) {
	srv := couchtest.New(t)
	sessionClient, err := New(Config{URL: srv.URL(), Auth: AuthSession, Username: "admin", Secret: "password"})
	if err != nil {
		t.Fatal(err)
	}
	user, pass, ok := sessionClient.SessionCredentials()
	if !ok || user != "admin" || pass != "password" {
		t.Errorf("session credentials = %q, %t (the password is deliberately not printed)", user, ok)
	}
	for _, cfg := range []Config{
		{URL: srv.URL(), Auth: AuthNone},
		{URL: srv.URL(), Auth: AuthJWT, Secret: "token"},
		{URL: srv.URL(), Auth: AuthProxy, Username: "ops", Secret: "proxysecret"},
	} {
		c, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, ok := c.SessionCredentials(); ok {
			t.Errorf("%s handed out credentials it does not have", cfg.Auth)
		}
	}
}
