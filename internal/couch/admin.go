package couch

import (
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"strings"
)

// ActiveTask is one entry of GET /_active_tasks, decoded into the fields cdb
// renders plus the object as the server sent it.
//
// https://docs.couchdb.org/en/stable/api/server/common.html#active-tasks
type ActiveTask struct {
	// Type is "replication", "database_compaction", "view_compaction",
	// "indexer" or "search_indexer". CouchDB may add others; nothing here
	// depends on the set being closed.
	Type string
	Node string
	// Database, DesignDocument are set on compaction and indexing tasks.
	Database       string
	DesignDocument string
	// DocID, Source, Target and Continuous are set on replication tasks.
	// Source and Target are redacted: a replication endpoint routinely carries
	// a password, and _active_tasks hands it back in full.
	DocID      string
	Source     string
	Target     string
	Continuous bool
	// Progress is the percentage, and HasProgress says whether the server sent
	// one at all. Zero is a real progress — a just-started indexer reports it —
	// so the two cannot be collapsed into one field.
	Progress    int
	HasProgress bool
	// StartedOn and UpdatedOn are unix timestamps.
	StartedOn    int64
	UpdatedOn    int64
	ChangesDone  int64
	TotalChanges int64
	// Raw is the whole task object as returned, with source and target
	// replaced by their redacted forms. It is what --json prints, so it must
	// never carry a credential.
	Raw json.RawMessage
}

// ActiveTasks reads GET /_active_tasks.
func (c *Client) ActiveTasks(ctx context.Context) ([]ActiveTask, error) {
	var raw []json.RawMessage
	if err := c.DoJSON(ctx, "GET", "/_active_tasks", nil, &raw, "list", "active tasks on "+c.host); err != nil {
		return nil, AsAdmin(err, "listing active tasks")
	}
	out := make([]ActiveTask, 0, len(raw))
	for _, item := range raw {
		t, err := decodeActiveTask(item)
		if err != nil {
			return nil, Wrap(err, "list", "active tasks on "+c.host)
		}
		out = append(out, t)
	}
	return out, nil
}

// decodeActiveTask decodes one task and rewrites its endpoints in place, so
// that the redacted form is the only one that leaves this function.
func decodeActiveTask(item json.RawMessage) (ActiveTask, error) {
	var body struct {
		Type           string `json:"type"`
		Node           string `json:"node"`
		Database       string `json:"database"`
		DesignDocument string `json:"design_document"`
		DocID          string `json:"doc_id"`
		Source         string `json:"source"`
		Target         string `json:"target"`
		Continuous     bool   `json:"continuous"`
		Progress       *int   `json:"progress"`
		StartedOn      int64  `json:"started_on"`
		UpdatedOn      int64  `json:"updated_on"`
		ChangesDone    int64  `json:"changes_done"`
		TotalChanges   int64  `json:"total_changes"`
	}
	if err := json.Unmarshal(item, &body); err != nil {
		return ActiveTask{}, err
	}
	t := ActiveTask{
		Type: body.Type, Node: body.Node,
		Database: body.Database, DesignDocument: body.DesignDocument,
		DocID: body.DocID, Continuous: body.Continuous,
		Source: RedactURL(body.Source), Target: RedactURL(body.Target),
		StartedOn: body.StartedOn, UpdatedOn: body.UpdatedOn,
		ChangesDone: body.ChangesDone, TotalChanges: body.TotalChanges,
	}
	if body.Progress != nil {
		t.Progress, t.HasProgress = *body.Progress, true
	}
	raw, err := redactTaskObject(item, t.Source, t.Target)
	if err != nil {
		return ActiveTask{}, err
	}
	t.Raw = raw
	return t, nil
}

// redactTaskObject replaces the source and target members of a task object
// with their already-redacted forms, leaving every other member alone. The
// object is re-marshalled from a map, so a member cdb does not decode still
// reaches --json.
func redactTaskObject(item json.RawMessage, source, target string) (json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(item, &m); err != nil {
		return nil, err
	}
	for key, val := range map[string]string{"source": source, "target": target} {
		if _, present := m[key]; !present {
			continue
		}
		b, err := json.Marshal(val)
		if err != nil {
			return nil, err
		}
		m[key] = b
	}
	return json.Marshal(m)
}

// ConfigEntry is one configuration triple. Value is the string CouchDB stores;
// a value that is not a JSON string (a hand-edited ini can leave a number
// there) is carried as its compact JSON text.
type ConfigEntry struct{ Section, Key, Value string }

// configPath builds the _config URL for a node, section and key. The node name
// is a CouchDB node ("couchdb@127.0.0.1") or the alias "_local"; a section or
// key may legitimately contain a slash — an admin named "o/brien" — so both are
// escaped.
//
// https://docs.couchdb.org/en/stable/api/server/configuration.html
func configPath(node, section, key string) string {
	p := "/_node/" + url.PathEscape(node) + "/_config"
	if section != "" {
		p += "/" + url.PathEscape(section)
	}
	if key != "" {
		p += "/" + url.PathEscape(key)
	}
	return p
}

// Config reads the whole configuration of a node, one section of it, or one
// key. An empty section means the whole configuration; an empty key means the
// whole section. Entries come back sorted by section then key, so that every
// caller renders them in the same order without sorting again.
func (c *Client) Config(ctx context.Context, node, section, key string) ([]ConfigEntry, error) {
	var body json.RawMessage
	if err := c.DoJSON(ctx, "GET", configPath(node, section, key), nil, &body, "read", "configuration of "+node); err != nil {
		return nil, AsAdmin(err, "reading configuration")
	}
	switch {
	case key != "":
		v, err := configString(body)
		if err != nil {
			return nil, Wrap(err, "read", "configuration of "+node)
		}
		return []ConfigEntry{{Section: section, Key: key, Value: v}}, nil
	case section != "":
		return configSectionEntries(section, body)
	default:
		var whole map[string]json.RawMessage
		if err := json.Unmarshal(body, &whole); err != nil {
			return nil, Wrap(err, "read", "configuration of "+node)
		}
		var out []ConfigEntry
		for name, raw := range whole {
			entries, err := configSectionEntries(name, raw)
			if err != nil {
				return nil, err
			}
			out = append(out, entries...)
		}
		sortConfigEntries(out)
		return out, nil
	}
}

// configSectionEntries decodes one {"key":"value"} object.
func configSectionEntries(section string, body json.RawMessage) ([]ConfigEntry, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, Wrap(err, "read", "configuration section "+section)
	}
	out := make([]ConfigEntry, 0, len(m))
	for k, raw := range m {
		v, err := configString(raw)
		if err != nil {
			return nil, Wrap(err, "read", "configuration section "+section)
		}
		out = append(out, ConfigEntry{Section: section, Key: k, Value: v})
	}
	sortConfigEntries(out)
	return out, nil
}

func sortConfigEntries(e []ConfigEntry) {
	sort.Slice(e, func(i, j int) bool {
		if e[i].Section != e[j].Section {
			return e[i].Section < e[j].Section
		}
		return e[i].Key < e[j].Key
	})
}

// configString renders a configuration value. CouchDB 3.x writes every value
// as a JSON string, and that is the case worth unquoting; the documentation
// promises only "JSON-formatted", so anything else is handed back as its own
// compact JSON text rather than refused.
func configString(raw json.RawMessage) (string, error) {
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, `"`) {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", err
		}
		return s, nil
	}
	return trimmed, nil
}

// SetConfig writes one configuration key and returns the value it replaced,
// which CouchDB sends back in the response body. The old value is "" when the
// key had none.
func (c *Client) SetConfig(ctx context.Context, node, section, key, value string) (string, error) {
	var body json.RawMessage
	if err := c.DoJSON(ctx, "PUT", configPath(node, section, key), value, &body, "write", "configuration of "+node); err != nil {
		return "", AsAdmin(err, "changing configuration")
	}
	return configString(body)
}

// DeleteConfig removes one configuration key and returns the value it held.
func (c *Client) DeleteConfig(ctx context.Context, node, section, key string) (string, error) {
	var body json.RawMessage
	if err := c.DoJSON(ctx, "DELETE", configPath(node, section, key), nil, &body, "delete", "configuration of "+node); err != nil {
		return "", AsAdmin(err, "changing configuration")
	}
	return configString(body)
}

// ReloadConfig re-reads the node's ini files, discarding in-memory changes that
// were never written to disk.
func (c *Client) ReloadConfig(ctx context.Context, node string) error {
	err := c.DoJSON(ctx, "POST", configPath(node, "", "")+"/_reload", struct{}{}, nil, "write", "configuration of "+node)
	return AsAdmin(err, "changing configuration")
}

// Membership is the response of GET /_membership: every node the cluster knows
// about, and the subset of them that is in the cluster.
type Membership struct {
	AllNodes     []string
	ClusterNodes []string
}

// Membership reads GET /_membership.
func (c *Client) Membership(ctx context.Context) (Membership, error) {
	var body struct {
		AllNodes     []string `json:"all_nodes"`
		ClusterNodes []string `json:"cluster_nodes"`
	}
	if err := c.DoJSON(ctx, "GET", "/_membership", nil, &body, "read", "cluster membership of "+c.host); err != nil {
		return Membership{}, AsAdmin(err, "reading cluster membership")
	}
	return Membership{AllNodes: body.AllNodes, ClusterNodes: body.ClusterNodes}, nil
}

// ClusterSetupState reads GET /_cluster_setup, which answers one of
// cluster_disabled, single_node_disabled, single_node_enabled, cluster_enabled
// or cluster_finished.
//
// A 404 is returned as it stands rather than translated: whether "this server
// has no setup endpoint" is a failure or a row reading "unavailable" is the
// command's decision, not the client's.
func (c *Client) ClusterSetupState(ctx context.Context) (string, error) {
	var body struct {
		State string `json:"state"`
	}
	if err := c.DoJSON(ctx, "GET", "/_cluster_setup", nil, &body, "read", "cluster setup of "+c.host); err != nil {
		return "", AsAdmin(err, "reading cluster setup")
	}
	return body.State, nil
}

// SingleNodeSetup is the POST /_cluster_setup body for the enable_single_node
// action. Password is a credential: it is in a request body, never in a URL,
// never in a log line, and never in an error message.
type SingleNodeSetup struct {
	Username    string
	Password    string
	BindAddress string
	Port        int
}

// EnableSingleNode posts action=enable_single_node. CouchDB sets [cluster] n to
// 1, binds the given address, and creates the system databases (_users,
// _replicator, _global_changes), which is what turns a fresh node into a
// working single-node install.
//
// https://docs.couchdb.org/en/stable/api/server/common.html#cluster-setup
func (c *Client) EnableSingleNode(ctx context.Context, req SingleNodeSetup) error {
	body := map[string]any{
		"action":       "enable_single_node",
		"username":     req.Username,
		"password":     req.Password,
		"bind_address": req.BindAddress,
		"port":         req.Port,
	}
	err := c.DoJSON(ctx, "POST", "/_cluster_setup", body, nil, "write", "cluster setup of "+c.host)
	return AsAdmin(err, "cluster setup")
}
