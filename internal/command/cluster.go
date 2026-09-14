package command

import (
	"context"
	"fmt"
	"strconv"

	"github.com/spf13/pflag"
	"github.com/sriannamalai/CDB.CLI/internal/couch"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// Defaults for "cluster setup --single-node", which are CouchDB's own.
const (
	defaultBindAddress = "0.0.0.0"
	defaultPort        = 5984
)

// setupDoneStates are the states that mean the node is already configured.
// "cluster setup --single-node" reports them and does nothing.
var setupDoneStates = map[string]bool{
	"single_node_enabled": true,
	"cluster_finished":    true,
}

// setupStartStates are the states "cluster setup --single-node" will act from.
//
//	cluster_disabled      no admin yet, or the node is bound to 127.0.0.1
//	single_node_disabled  [cluster] n is 1 and the system databases are missing
//	cluster_enabled       an admin exists, [cluster] n is not 1, and the system
//	                      databases are missing — which is exactly what the
//	                      official couchdb:3.5 image gives you when it is
//	                      started with COUCHDB_USER and COUCHDB_PASSWORD and
//	                      nothing else (checked 2026-09-14)
var setupStartStates = map[string]bool{
	"cluster_disabled":     true,
	"single_node_disabled": true,
	"cluster_enabled":      true,
}

// clusterDetails is the long help for cluster.
const clusterDetails = `"cluster status" reports what _cluster_setup and _membership say: the setup
state, the [cluster] n and q of the node named by --node, and every node the
server knows about. A server whose setup endpoint is switched off reports the
state as unavailable; everything else still works.

"cluster setup --single-node" turns a fresh node into a working single-node
install: CouchDB sets [cluster] n to 1 and creates the _users and _replicator
databases. A node that is already set up is reported and left alone. Multi-node setup — enable_cluster, add_node, finish_cluster — is not
supported by cdb; use Fauxton or curl for that.`

// Cluster returns the cluster command.
func Cluster() Command {
	return Command{
		Name:    "cluster",
		Summary: "Report the cluster state and configure a single node",
		Example: `$ cdb cluster status
 FIELD            | VALUE
------------------+-----------------
 state            | cluster_finished
 cluster n        | 3
 cluster q        | 2
 all_nodes[0]     | node1@127.0.0.1
 cluster_nodes[0] | node1@127.0.0.1

$ cdb cluster setup --single-node --yes
Single-node setup finished; state is single_node_enabled.`,
		Usage:       "[status | setup --single-node]",
		Details:     clusterDetails,
		MinArgs:     0,
		MaxArgs:     1,
		NeedsClient: true,
		Flags: func(fs *pflag.FlagSet) {
			fs.String("node", defaultNode, "the node whose [cluster] settings status reports")
			fs.Bool("single-node", false, "configure this server as a single node")
			fs.String("bind-address", defaultBindAddress, "the address the node should listen on")
			fs.Int("port", defaultPort, "the port the node should listen on")
		},
		Run: func(ctx context.Context, s *session.Session, inv Invocation) (Result, error) {
			sub := inv.Arg(0)
			if sub == "" {
				sub = "status"
			}
			node := inv.String("node")
			if node == "" {
				node = defaultNode
			}
			switch sub {
			case "status":
				return clusterStatus(ctx, s, node)
			case "setup":
				return clusterSetup(ctx, s, inv)
			default:
				return nil, Usagef("cluster", "unknown subcommand %q; expected status or setup", sub)
			}
		},
	}
}

// clusterStatus reports the setup state, the node's cluster shape, and the
// membership. A server with no setup endpoint still has the other two, so the
// 404 becomes a value rather than a failure.
func clusterStatus(ctx context.Context, s *session.Session, node string) (Result, error) {
	rows := Rows{Columns: []Column{{Title: "field"}, {Title: "value"}}}
	add := func(k, v string) {
		rows.Items = append(rows.Items, Row{Cells: []string{k, v}, JSON: jsonObject("field", k, "value", v)})
	}

	state, err := s.Client.ClusterSetupState(ctx)
	if err != nil {
		if e, ok := couch.AsError(err); ok && e.Status == 404 {
			// _cluster_setup is not enabled on this server. That is a fact
			// about the server, not a failure of the command.
			state = "unavailable"
		} else {
			return nil, err
		}
	}
	add("state", state)

	shape, err := clusterShape(ctx, s, node)
	if err != nil {
		return nil, err
	}
	add("cluster n", shape["n"])
	add("cluster q", shape["q"])

	m, err := s.Client.Membership(ctx)
	if err != nil {
		return nil, err
	}
	for i, n := range m.AllNodes {
		add("all_nodes["+strconv.Itoa(i)+"]", n)
	}
	for i, n := range m.ClusterNodes {
		add("cluster_nodes["+strconv.Itoa(i)+"]", n)
	}
	return rows, nil
}

// clusterShape reads [cluster] n and q from a node. Both are missing on a
// stock 3.5 install, where the running values are the compiled-in defaults and
// the section is simply empty; saying so is better than printing a blank cell
// that reads like a failed read.
func clusterShape(ctx context.Context, s *session.Session, node string) (map[string]string, error) {
	shape := map[string]string{"n": "(server default)", "q": "(server default)"}
	entries, err := s.Client.Config(ctx, node, "cluster", "")
	if err != nil {
		if e, ok := couch.AsError(err); ok && e.Status == 404 {
			return shape, nil
		}
		return nil, err
	}
	for _, e := range entries {
		if e.Key == "n" || e.Key == "q" {
			shape[e.Key] = e.Value
		}
	}
	return shape, nil
}

// clusterSetup configures this server as a single node. It reads the state
// first, because a node that is already set up must not be set up again and
// because a state nobody has seen before is a reason to stop rather than to
// post and hope.
func clusterSetup(ctx context.Context, s *session.Session, inv Invocation) (Result, error) {
	if !inv.Bool("single-node") {
		return nil, Usagef("cluster", "cluster setup needs --single-node; cdb does not offer multi-node setup (enable_cluster, add_node, finish_cluster)")
	}
	state, err := s.Client.ClusterSetupState(ctx)
	if err != nil {
		if e, ok := couch.AsError(err); ok && e.Status == 404 {
			return nil, Errorf(err, "This server has no _cluster_setup endpoint, so cdb cannot configure it.")
		}
		return nil, err
	}
	if setupDoneStates[state] {
		return Message{Text: fmt.Sprintf("This node is already set up; its state is %s.", state)}, nil
	}
	if !setupStartStates[state] {
		return nil, Errorf(nil, "This node reports the setup state %q, which cdb does not know how to configure. \"cluster status\" shows what the server says.", state)
	}

	username, password, ok := s.Client.SessionCredentials()
	if !ok {
		// A JWT, proxy or IAM connection has no CouchDB password, and neither
		// has an anonymous one. The endpoint takes a user name and a password,
		// so they have to be asked for.
		if !s.Prefs.Interactive {
			return nil, Usagef("cluster", "this connection has no user name and password for the setup endpoint; connect with --auth session, or run this on a terminal so cdb can ask")
		}
		username, err = askLine(s, "Server admin user name", s.Client.Username())
		if err != nil {
			return nil, err
		}
		if username == "" {
			return nil, Errorf(nil, "A user name is required to configure a node.")
		}
		password, err = readSecret(s, "Password for "+username)
		if err != nil {
			return nil, err
		}
		if password == "" {
			return nil, Errorf(nil, "A password is required to configure a node.")
		}
	}

	if err := Confirm(ctx, s, fmt.Sprintf("Configure %s as a single node?", s.Client.Host())); err != nil {
		return nil, err
	}
	bind := inv.String("bind-address")
	if bind == "" {
		bind = defaultBindAddress
	}
	port := inv.Int("port")
	if port == 0 {
		port = defaultPort
	}
	if err := s.Client.EnableSingleNode(ctx, couch.SingleNodeSetup{
		Username: username, Password: password, BindAddress: bind, Port: port,
	}); err != nil {
		return nil, err
	}
	after, err := s.Client.ClusterSetupState(ctx)
	if err != nil {
		return nil, err
	}
	return Message{Text: fmt.Sprintf("Single-node setup finished; state is %s.", after)}, nil
}
