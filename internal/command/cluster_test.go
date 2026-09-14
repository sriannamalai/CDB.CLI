package command

import (
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
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
