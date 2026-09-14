package command

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/couch"
	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

func TestClusterStatusReportsStateNodesAndShape(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_cluster_setup", 200, `{"state":"cluster_finished"}`)
	srv.JSON("GET", "/_membership", 200,
		`{"all_nodes":["node1@127.0.0.1","node2@127.0.0.1"],"cluster_nodes":["node1@127.0.0.1"]}`)
	srv.JSON("GET", "/_node/_local/_config/cluster", 200, `{"n":"3","q":"2"}`)
	s := connected(t, srv)
	res, err := invoke(t, Cluster(), s, "status")
	if err != nil {
		t.Fatal(err)
	}
	rows, ok := res.(Rows)
	if !ok {
		t.Fatalf("result is %T, want Rows", res)
	}
	got := map[string]string{}
	var order []string
	for _, item := range rows.Items {
		got[item.Cells[0]] = item.Cells[1]
		order = append(order, item.Cells[0])
	}
	if got["state"] != "cluster_finished" {
		t.Errorf("state = %q", got["state"])
	}
	if got["cluster n"] != "3" || got["cluster q"] != "2" {
		t.Errorf("shape = %v", got)
	}
	if got["all_nodes[0]"] != "node1@127.0.0.1" || got["all_nodes[1]"] != "node2@127.0.0.1" {
		t.Errorf("all_nodes = %v", got)
	}
	if got["cluster_nodes[0]"] != "node1@127.0.0.1" {
		t.Errorf("cluster_nodes = %v", got)
	}
	if order[0] != "state" {
		t.Errorf("the first row is %q, want state", order[0])
	}
}

func TestClusterStatusWithoutTheSetupEndpointSaysUnavailable(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_cluster_setup", 404, `{"error":"not_found","reason":"missing"}`)
	srv.JSON("GET", "/_membership", 200, `{"all_nodes":["nonode@nohost"],"cluster_nodes":["nonode@nohost"]}`)
	srv.JSON("GET", "/_node/_local/_config/cluster", 200, `{}`)
	s := connected(t, srv)
	res, err := invoke(t, Cluster(), s, "status")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, item := range res.(Rows).Items {
		got[item.Cells[0]] = item.Cells[1]
	}
	if got["state"] != "unavailable" {
		t.Errorf("state = %q, want unavailable", got["state"])
	}
	if got["cluster n"] != "(server default)" || got["cluster q"] != "(server default)" {
		t.Errorf("an unset [cluster] section rendered as %v", got)
	}
}

func TestClusterStatusIsTheDefaultSubcommand(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_cluster_setup", 200, `{"state":"single_node_enabled"}`)
	srv.JSON("GET", "/_membership", 200, `{"all_nodes":["nonode@nohost"],"cluster_nodes":["nonode@nohost"]}`)
	srv.JSON("GET", "/_node/_local/_config/cluster", 200, `{"n":"1","q":"2"}`)
	s := connected(t, srv)
	if _, err := invoke(t, Cluster(), s); err != nil {
		t.Fatal(err)
	}
}

func TestClusterStatusNodeFlagReachesTheConfigURL(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_cluster_setup", 200, `{"state":"cluster_finished"}`)
	srv.JSON("GET", "/_membership", 200, `{"all_nodes":["node1@127.0.0.1"],"cluster_nodes":["node1@127.0.0.1"]}`)
	srv.JSON("GET", "/_node/node1@127.0.0.1/_config/cluster", 200, `{"n":"3","q":"8"}`)
	s := connected(t, srv)
	if _, err := invoke(t, Cluster(), s, "status", "--node", "node1@127.0.0.1"); err != nil {
		t.Fatal(err)
	}
}

func TestClusterSetupSingleNodeConfirmsAndPosts(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSONSeq("GET", "/_cluster_setup", 200,
		`{"state":"cluster_enabled"}`,     // before: the state of a fresh image
		`{"state":"single_node_enabled"}`, // after
	)
	srv.JSON("POST", "/_cluster_setup", 201, `{"ok":true}`)
	s := sessionAuthenticated(t, srv, "admin", "password")
	s.Prefs.Interactive = true
	s.SetStdin(strings.NewReader("y\n"))

	res, err := invoke(t, Cluster(), s, "setup", "--single-node")
	if err != nil {
		t.Fatal(err)
	}
	out := s.Stdout.(*bytes.Buffer).String()
	if !strings.Contains(out, "Configure "+s.Client.Host()+" as a single node?") {
		t.Errorf("prompt = %q", out)
	}
	if strings.Contains(out, "password") {
		t.Fatal("the password reached the prompt")
	}
	if msg := res.(Message).Text; msg != "Single-node setup finished; state is single_node_enabled." {
		t.Errorf("message = %q", msg)
	}
	body := string(srv.Last("POST", "/_cluster_setup").Body)
	for _, want := range []string{
		`"action":"enable_single_node"`, `"username":"admin"`, `"password":"password"`,
		`"bind_address":"0.0.0.0"`, `"port":5984`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body %s is missing %s", body, want)
		}
	}
}

func TestClusterSetupOnAConfiguredNodeDoesNothing(t *testing.T) {
	for _, state := range []string{"single_node_enabled", "cluster_finished"} {
		srv := couchtest.New(t)
		srv.JSON("GET", "/_cluster_setup", 200, `{"state":"`+state+`"}`)
		s := sessionAuthenticated(t, srv, "admin", "password")
		s.Prefs.Yes = true
		res, err := invoke(t, Cluster(), s, "setup", "--single-node")
		if err != nil {
			t.Fatalf("%s: %v", state, err)
		}
		want := "This node is already set up; its state is " + state + "."
		if msg := res.(Message).Text; msg != want {
			t.Errorf("%s: message = %q, want %q", state, msg, want)
		}
		if srv.Last("POST", "/_cluster_setup") != nil {
			t.Errorf("%s: a configured node was set up again", state)
		}
	}
}

func TestClusterSetupRefusesAnUnexpectedState(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_cluster_setup", 200, `{"state":"something_new"}`)
	s := sessionAuthenticated(t, srv, "admin", "password")
	s.Prefs.Yes = true
	_, err := invoke(t, Cluster(), s, "setup", "--single-node")
	if err == nil || !strings.Contains(err.Error(), "something_new") {
		t.Fatalf("error = %v", err)
	}
	if srv.Last("POST", "/_cluster_setup") != nil {
		t.Error("a node in an unknown state was set up anyway")
	}
}

func TestClusterSetupNeedsSingleNode(t *testing.T) {
	srv := couchtest.New(t)
	s := connected(t, srv)
	_, err := invoke(t, Cluster(), s, "setup")
	var ue *UsageError
	if !errors.As(err, &ue) || !strings.Contains(ue.Reason, "--single-node") {
		t.Fatalf("error = %v", err)
	}
}

func TestClusterSetupPromptsWhenTheSessionHasNoPassword(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSONSeq("GET", "/_cluster_setup", 200,
		`{"state":"cluster_disabled"}`, `{"state":"single_node_enabled"}`)
	srv.JSON("POST", "/_cluster_setup", 201, `{"ok":true}`)
	// connected() builds an AuthNone client, which has no password to reuse.
	s := connected(t, srv)
	s.Prefs.Interactive = true
	s.SetStdin(strings.NewReader("setupadmin\nsetuppass\ny\n"))
	if _, err := invoke(t, Cluster(), s, "setup", "--single-node"); err != nil {
		t.Fatal(err)
	}
	body := string(srv.Last("POST", "/_cluster_setup").Body)
	if !strings.Contains(body, `"username":"setupadmin"`) || !strings.Contains(body, `"password":"setuppass"`) {
		t.Errorf("body = %s", body)
	}
	if out := s.Stdout.(*bytes.Buffer).String(); strings.Contains(out, "setuppass") {
		t.Fatal("the password was echoed")
	}
}

// sessionAuthenticated returns a session whose client logs in with a user name
// and password, which is the only kind "cluster setup" can take credentials
// from without asking. connected() (navigate_test.go) builds an AuthNone
// client, which cannot.
func sessionAuthenticated(t *testing.T, srv *couchtest.Server, user, password string) *session.Session {
	t.Helper()
	// A session client logs in lazily, on its first request, so every stub
	// server it is pointed at needs the login route.
	srv.JSON("POST", "/_session", 200, `{"ok":true,"name":"`+user+`","roles":["_admin"]}`)
	c, err := couch.New(couch.Config{URL: srv.URL(), Auth: couch.AuthSession, Username: user, Secret: password})
	if err != nil {
		t.Fatal(err)
	}
	s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	s.Attach(c, "test")
	t.Cleanup(func() { _ = s.Detach() })
	return s
}

func TestClusterStatusRefusesANodeTheClusterDoesNotHave(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_cluster_setup", 200, `{"state":"cluster_finished"}`)
	srv.JSON("GET", "/_membership", 200, `{"all_nodes":["nonode@nohost"],"cluster_nodes":["nonode@nohost"]}`)
	s := connected(t, srv)
	_, err := invoke(t, Cluster(), s, "status", "--node", "bogus@nohost")
	if err == nil {
		t.Fatal("a node that is not in the cluster was reported as if it were")
	}
	if err.Error() != "Node bogus@nohost is not in this cluster." {
		t.Errorf("error = %q", err.Error())
	}
	// The [cluster] read must not have happened: a missing node's _config is a
	// 404 like an empty section's, which is what hid this.
	if srv.Last("GET", "/_node/bogus@nohost/_config/cluster") != nil {
		t.Error("the config of a node that is not in the cluster was read anyway")
	}
}

func TestClusterStatusReadsAKnownNonLocalNode(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_cluster_setup", 200, `{"state":"cluster_finished"}`)
	srv.JSON("GET", "/_membership", 200,
		`{"all_nodes":["node1@127.0.0.1","node2@127.0.0.1"],"cluster_nodes":["node1@127.0.0.1"]}`)
	srv.JSON("GET", "/_node/node2@127.0.0.1/_config/cluster", 200, `{"n":"3","q":"8"}`)
	s := connected(t, srv)
	res, err := invoke(t, Cluster(), s, "status", "--node", "node2@127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, item := range res.(Rows).Items {
		got[item.Cells[0]] = item.Cells[1]
	}
	if got["cluster n"] != "3" || got["cluster q"] != "8" {
		t.Errorf("the named node's shape = %v", got)
	}
}
