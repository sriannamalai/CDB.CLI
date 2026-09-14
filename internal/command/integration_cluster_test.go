package command

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/couch"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

func TestClusterStatusAgainstALiveServer(t *testing.T) {
	if os.Getenv("CDB_TEST_URL") == "" {
		t.Skip("CDB_TEST_URL is not set")
	}
	s := integrationSession(t)
	res, err := invoke(t, Cluster(), s, "status")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, item := range res.(Rows).Items {
		got[item.Cells[0]] = item.Cells[1]
		t.Logf("%s = %s", item.Cells[0], item.Cells[1])
	}
	if got["state"] == "" {
		t.Fatal("no state row")
	}
	if got["all_nodes[0]"] == "" {
		t.Fatal("no node rows; _membership always names at least this node")
	}

	// The node the server named is readable under its own name, not only
	// under the _local alias.
	named, err := invoke(t, Cluster(), s, "status", "--node", got["all_nodes[0]"])
	if err != nil {
		t.Fatalf("--node %s: %v", got["all_nodes[0]"], err)
	}
	for _, item := range named.(Rows).Items {
		if item.Cells[0] == "cluster n" {
			t.Logf("--node %s: cluster n = %s", got["all_nodes[0]"], item.Cells[1])
		}
	}

	// A node that is not in the cluster is a mistake, not an empty section.
	if _, err := invoke(t, Cluster(), s, "status", "--node", "bogus@nohost"); err == nil {
		t.Error("a node that is not in the cluster was reported as if it were")
	} else {
		t.Logf("--node bogus@nohost: %v", err)
		if err.Error() != "Node bogus@nohost is not in this cluster." {
			t.Errorf("error = %q", err.Error())
		}
	}
}

// TestClusterSetupAgainstAFreshServer is the one test in the suite that
// reconfigures a server, so it runs only when CDB_TEST_FRESH_URL names a node
// the operator has said may be reconfigured. It is unset in CI and in the two
// local runs; the implementer verifies it once by hand against a container
// started for the purpose.
func TestClusterSetupAgainstAFreshServer(t *testing.T) {
	url := os.Getenv("CDB_TEST_FRESH_URL")
	if url == "" {
		t.Skip("CDB_TEST_FRESH_URL is not set")
	}
	s := freshSession(t, url)
	s.Prefs.Yes = true
	ctx := context.Background()

	before, err := s.Client.ClusterSetupState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	dbsBefore, err := s.Client.ListDatabases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("before: state=%s databases=%v", before, dbsBefore)

	res, err := invoke(t, Cluster(), s, "setup", "--single-node")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("result: %s", res.(Message).Text)

	after, err := s.Client.ClusterSetupState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	dbsAfter, err := s.Client.ListDatabases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("after: state=%s databases=%v", after, dbsAfter)

	if after != "single_node_enabled" && after != "cluster_finished" {
		t.Fatalf("after setup the state is %q; setup did not take", after)
	}
	for _, want := range []string{"_users", "_replicator"} {
		found := false
		for _, db := range dbsAfter {
			if db == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s was not created; databases are %v", want, dbsAfter)
		}
	}
	if !strings.Contains(res.(Message).Text, after) {
		t.Errorf("the message %q does not report the state the server ended in (%s)", res.(Message).Text, after)
	}

	// A second run must be a no-op.
	again, err := invoke(t, Cluster(), s, "setup", "--single-node")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(again.(Message).Text, "already set up") {
		t.Errorf("the second run said %q", again.(Message).Text)
	}
}

// freshSession connects to the server CDB_TEST_FRESH_URL names, with the
// credentials in CDB_TEST_FRESH_USER and CDB_TEST_FRESH_PASSWORD (defaulting
// to admin/password, which is what the container is started with).
func freshSession(t *testing.T, url string) *session.Session {
	t.Helper()
	user := os.Getenv("CDB_TEST_FRESH_USER")
	if user == "" {
		user = "admin"
	}
	password := os.Getenv("CDB_TEST_FRESH_PASSWORD")
	if password == "" {
		password = "password"
	}
	c, err := couch.New(couch.Config{URL: url, Auth: couch.AuthSession, Username: user, Secret: password, UserAgent: "cdb"})
	if err != nil {
		t.Fatal(err)
	}
	s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	s.Attach(c, "fresh")
	t.Cleanup(func() { _ = s.Detach() })
	return s
}
