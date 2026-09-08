// Package replicate writes _replicator documents and reads the replication
// scheduler.
package replicate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/sriannamalai/CDB.CLI/internal/couch"
	"github.com/sriannamalai/CDB.CLI/internal/path"
)

// Endpoint is one side of a replication, in the two forms cdb needs it: a URL
// that is safe to show, and the wire object CouchDB is given.
//
// CouchDB 3.x has no local endpoints — a bare database name in "source" or
// "target" is rejected with 403 local_endpoints_not_supported — so even a
// same-server replication has to name a full URL, and that URL needs
// credentials. They travel in CouchDB's per-endpoint auth object inside the
// request body rather than as userinfo in the URL, which keeps the password
// out of the stored document, out of the scheduler's view of it, and out of
// anything cdb renders.
type Endpoint struct {
	// URL is the endpoint address with any userinfo removed. It is the only
	// part of an Endpoint that may be shown, logged or put in a Result.
	URL string
	// doc is the value written into the _replicator document. It may carry a
	// plaintext password or a bearer token, so it is unexported and reachable
	// only through wire(), whose one caller is Create.
	doc map[string]any
}

// String returns the redacted URL, so that printing an Endpoint by accident
// cannot leak a credential.
func (e Endpoint) String() string { return e.URL }

// wire returns the "source" or "target" value for the replicator document.
func (e Endpoint) wire() map[string]any {
	if e.doc != nil {
		return e.doc
	}
	return map[string]any{"url": e.URL}
}

// Request describes a replication to create.
type Request struct {
	ID           string
	Source       Endpoint
	Target       Endpoint
	Continuous   bool
	CreateTarget bool
	Filter       string
}

// Status is one entry of _scheduler/docs. It is only ever marshalled (the wire
// shape is schedulerDoc below), and it goes straight into command.Row.JSON, so
// the json tags are load-bearing: "replications --json" must emit lower-case
// keys like every other command. Source and Target are redacted URLs for the
// same reason: nothing that renders a scheduler entry may print a credential.
type Status struct {
	DocID       string          `json:"doc_id"`
	ID          string          `json:"id"`
	Source      string          `json:"source"`
	Target      string          `json:"target"`
	State       string          `json:"state"`
	Node        string          `json:"node"`
	Error       string          `json:"error,omitempty"`
	ErrorCount  int64           `json:"error_count"`
	LastUpdated string          `json:"last_updated"`
	Info        json.RawMessage `json:"info,omitempty"`
}

// Create writes a document into _replicator and returns its id.
func Create(ctx context.Context, cl *couch.Client, req Request) (string, error) {
	doc := map[string]any{"source": req.Source.wire(), "target": req.Target.wire()}
	if req.ID != "" {
		doc["_id"] = req.ID
	}
	if req.Continuous {
		doc["continuous"] = true
	}
	if req.CreateTarget {
		doc["create_target"] = true
	}
	if req.Filter != "" {
		doc["filter"] = req.Filter
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := cl.DoJSON(ctx, "POST", "/_replicator", doc, &out, "create", "replication"); err != nil {
		return "", err
	}
	return out.ID, nil
}

// schedulerDoc is the wire shape of a _scheduler/docs entry. Source and Target
// are raw because CouchDB answers with either a URL string or the endpoint
// object the _replicator document carried.
type schedulerDoc struct {
	Database    string          `json:"database"`
	DocID       string          `json:"doc_id"`
	ID          *string         `json:"id"`
	Source      json.RawMessage `json:"source"`
	Target      json.RawMessage `json:"target"`
	State       string          `json:"state"`
	Node        string          `json:"node"`
	ErrorCount  int64           `json:"error_count"`
	LastUpdated string          `json:"last_updated"`
	Info        json.RawMessage `json:"info"`
}

func (d schedulerDoc) toStatus() Status {
	st := Status{
		DocID:       d.DocID,
		Source:      redactEndpoint(d.Source),
		Target:      redactEndpoint(d.Target),
		State:       d.State,
		Node:        d.Node,
		ErrorCount:  d.ErrorCount,
		LastUpdated: d.LastUpdated,
		Info:        d.Info,
	}
	if d.ID != nil {
		st.ID = *d.ID
	}
	if len(d.Info) > 0 {
		var info struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(d.Info, &info); err == nil {
			st.Error = info.Error
		}
	}
	return st
}

// redactEndpoint renders a scheduler entry's "source" or "target" as a URL
// nobody has to be careful with. CouchDB 3.5.2 answers with a plain string and
// strips credentials itself, but a hand-written _replicator document can put
// userinfo in the URL and an endpoint object carries "auth" or "headers"
// outright, so this drops all three rather than trusting the server.
func redactEndpoint(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return safeURL(s)
	}
	var obj struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		return safeURL(obj.URL)
	}
	return ""
}

// safeURL strips userinfo from a URL.
func safeURL(s string) string {
	u, err := url.Parse(s)
	if err != nil {
		// Unparseable, so it cannot be shown to be credential-free: drop
		// everything up to the last "@", which is where userinfo would be.
		if i := strings.LastIndex(s, "@"); i >= 0 {
			return s[i+1:]
		}
		return s
	}
	u.User = nil
	return u.String()
}

// List reads every scheduler entry.
func List(ctx context.Context, cl *couch.Client) ([]Status, error) {
	var body struct {
		Docs []schedulerDoc `json:"docs"`
	}
	if err := cl.DoJSON(ctx, "GET", "/_scheduler/docs", nil, &body, "list", "replications"); err != nil {
		return nil, err
	}
	out := make([]Status, 0, len(body.Docs))
	for _, d := range body.Docs {
		out = append(out, d.toStatus())
	}
	return out, nil
}

// Show reads one scheduler entry by its _replicator document id.
func Show(ctx context.Context, cl *couch.Client, docID string) (Status, error) {
	var d schedulerDoc
	apiPath := "/_scheduler/docs/_replicator/" + path.Encode(docID)
	if err := cl.DoJSON(ctx, "GET", apiPath, nil, &d, "read", fmt.Sprintf("replication %q", docID)); err != nil {
		return Status{}, err
	}
	return d.toStatus(), nil
}

// Cancel deletes the _replicator document, which stops the job.
func Cancel(ctx context.Context, cl *couch.Client, docID string) error {
	rev, err := cl.GetRev(ctx, "_replicator", docID)
	if err != nil {
		return err
	}
	_, err = cl.DeleteDocument(ctx, "_replicator", docID, rev)
	return err
}

// ResolveEndpoint turns a virtual database path into an endpoint against the
// connected server, and an http(s) URL into a remote endpoint. Userinfo in a
// remote URL moves into the per-endpoint auth object, so that the credential
// never leaves the request body.
func ResolveEndpoint(cl *couch.Client, base, s string) (Endpoint, error) {
	// A URL may carry a password, so neither branch below ever echoes s.
	if scheme, _, ok := strings.Cut(s, "://"); ok {
		if scheme != "http" && scheme != "https" {
			return Endpoint{}, fmt.Errorf("%q is not a replication scheme; use a database path or an http(s) URL", scheme)
		}
		u, err := url.Parse(s)
		if err != nil {
			return Endpoint{}, fmt.Errorf("the %s endpoint is not a valid URL", scheme)
		}
		user := u.User
		u.User = nil
		e := Endpoint{URL: u.String(), doc: map[string]any{"url": u.String()}}
		if user != nil {
			password, _ := user.Password()
			e.doc["auth"] = map[string]any{"basic": map[string]any{
				"username": user.Username(),
				"password": password,
			}}
		}
		return e, nil
	}
	t, err := path.Resolve(base, s)
	if err != nil {
		return Endpoint{}, err
	}
	if t.Kind != path.KindDatabase {
		return Endpoint{}, fmt.Errorf("%s is a %s; replication endpoints are databases or full URLs", t.Path, t.Kind)
	}
	doc := cl.ReplicationEndpoint(t.Database)
	safe, _ := doc["url"].(string)
	return Endpoint{URL: safe, doc: doc}, nil
}
