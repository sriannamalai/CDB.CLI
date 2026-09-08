package couch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/sriannamalai/CDB.CLI/internal/path"
)

// DatabaseInfo describes one database. The json tags are load-bearing: this
// struct is marshalled directly into command.Row.JSON, so "ls / --json" emits
// lower-case keys like every other command.
type DatabaseInfo struct {
	Name         string `json:"name"`
	DocCount     int64  `json:"doc_count"`
	DeletedCount int64  `json:"deleted_count"`
	DiskSize     int64  `json:"disk_size"`
	ExternalSize int64  `json:"external_size"`
	UpdateSeq    string `json:"update_seq"`
	Partitioned  bool   `json:"partitioned"`
	Q            int    `json:"q"`
	N            int    `json:"n"`
}

// dbInfoBody is the wire shape of GET /db and of the info member of _dbs_info.
type dbInfoBody struct {
	DBName      string `json:"db_name"`
	DocCount    int64  `json:"doc_count"`
	DocDelCount int64  `json:"doc_del_count"`
	UpdateSeq   string `json:"update_seq"`
	Sizes       struct {
		File     int64 `json:"file"`
		External int64 `json:"external"`
	} `json:"sizes"`
	Cluster struct {
		Q int `json:"q"`
		N int `json:"n"`
	} `json:"cluster"`
	Props struct {
		Partitioned bool `json:"partitioned"`
	} `json:"props"`
}

func (b dbInfoBody) toInfo() DatabaseInfo {
	return DatabaseInfo{
		Name:         b.DBName,
		DocCount:     b.DocCount,
		DeletedCount: b.DocDelCount,
		DiskSize:     b.Sizes.File,
		ExternalSize: b.Sizes.External,
		UpdateSeq:    b.UpdateSeq,
		Partitioned:  b.Props.Partitioned,
		Q:            b.Cluster.Q,
		N:            b.Cluster.N,
	}
}

// ListDatabases reads GET /_all_dbs.
func (c *Client) ListDatabases(ctx context.Context) ([]string, error) {
	var out []string
	if err := c.DoJSON(ctx, "GET", "/_all_dbs", nil, &out, "list", "databases"); err != nil {
		return nil, err
	}
	return out, nil
}

// DatabasesInfo reads POST /_dbs_info for a set of names.
func (c *Client) DatabasesInfo(ctx context.Context, names []string) ([]DatabaseInfo, error) {
	if len(names) == 0 {
		return nil, nil
	}
	var body []struct {
		Key  string      `json:"key"`
		Info *dbInfoBody `json:"info"`
	}
	req := map[string][]string{"keys": names}
	if err := c.DoJSON(ctx, "POST", "/_dbs_info", req, &body, "list", "databases"); err != nil {
		return nil, err
	}
	out := make([]DatabaseInfo, 0, len(body))
	for _, row := range body {
		if row.Info == nil {
			out = append(out, DatabaseInfo{Name: row.Key})
			continue
		}
		info := row.Info.toInfo()
		if info.Name == "" {
			info.Name = row.Key
		}
		out = append(out, info)
	}
	return out, nil
}

// DatabaseInfo reads GET /db.
func (c *Client) DatabaseInfo(ctx context.Context, db string) (DatabaseInfo, error) {
	var body dbInfoBody
	if err := c.DoJSON(ctx, "GET", "/"+path.Encode(db), nil, &body, "read", fmt.Sprintf("database %q", db)); err != nil {
		return DatabaseInfo{}, err
	}
	return body.toInfo(), nil
}

// DatabaseExists issues HEAD /db.
func (c *Client) DatabaseExists(ctx context.Context, db string) (bool, error) {
	req, err := c.NewRequest(ctx, http.MethodHead, "/"+path.Encode(db), nil)
	if err != nil {
		return false, Wrap(err, "read", fmt.Sprintf("database %q", db))
	}
	res, err := c.HTTP().Do(req)
	if err != nil {
		return false, Wrap(err, "read", fmt.Sprintf("database %q", db))
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode == http.StatusOK:
		return true, nil
	case res.StatusCode == http.StatusNotFound:
		return false, nil
	default:
		return false, NewError(res.StatusCode, nameForStatus(res.StatusCode), res.Status, "read", fmt.Sprintf("database %q", db))
	}
}

// DocRow is one row of _all_docs, a view, or _find.
type DocRow struct {
	ID    string
	Rev   string
	Key   json.RawMessage
	Value json.RawMessage
	Doc   json.RawMessage
	Error string
}

// AllDocsOptions configure a listing. Paging uses StartKeyDocID, never skip.
type AllDocsOptions struct {
	Partition     string
	StartKeyDocID string
	EndKeyDocID   string
	Limit         int
	IncludeDocs   bool
	Conflicts     bool
	Descending    bool
}

// DocPage is one page of rows plus the key that starts the next page.
type DocPage struct {
	Rows      []DocRow
	TotalRows int64
	// NextStartKeyDocID is the id to pass as StartKeyDocID for the next page,
	// or "" when this was the last page. Paging never uses skip.
	NextStartKeyDocID string
}

// jsonKey encodes a document id as a JSON string for use as a start_key or
// end_key query value. A plain `"`+id+`"` concatenation breaks for any id
// containing a quote, backslash, or control character; json.Marshal escapes
// those correctly.
func jsonKey(id string) string {
	b, err := json.Marshal(id)
	if err != nil {
		return `""`
	}
	return string(b)
}

func (o AllDocsOptions) query() url.Values {
	q := url.Values{}
	if o.Limit > 0 {
		// One extra row tells us whether there is a next page and what its
		// first id is. CouchDB's skip is never used.
		q.Set("limit", strconv.Itoa(o.Limit+1))
	}
	if o.StartKeyDocID != "" {
		q.Set("startkey_docid", o.StartKeyDocID)
		q.Set("start_key", jsonKey(o.StartKeyDocID))
	}
	if o.EndKeyDocID != "" {
		q.Set("endkey_docid", o.EndKeyDocID)
		q.Set("end_key", jsonKey(o.EndKeyDocID))
	}
	if o.IncludeDocs {
		q.Set("include_docs", "true")
	}
	if o.Conflicts {
		q.Set("conflicts", "true")
	}
	if o.Descending {
		q.Set("descending", "true")
	}
	return q
}

type allDocsBody struct {
	TotalRows int64 `json:"total_rows"`
	Rows      []struct {
		ID    string          `json:"id"`
		Key   json.RawMessage `json:"key"`
		Value json.RawMessage `json:"value"`
		Doc   json.RawMessage `json:"doc"`
		Error string          `json:"error"`
	} `json:"rows"`
}

// allDocsPath builds /db/_all_docs or /db/_partition/p/_all_docs.
func allDocsPath(db, partition string) string {
	return dbBase(db, partition) + "/_all_docs"
}

// AllDocs reads one page of _all_docs.
func (c *Client) AllDocs(ctx context.Context, db string, opts AllDocsOptions) (DocPage, error) {
	apiPath := allDocsPath(db, opts.Partition)
	if q := opts.query().Encode(); q != "" {
		apiPath += "?" + q
	}
	var body allDocsBody
	if err := c.DoJSON(ctx, "GET", apiPath, nil, &body, "list", fmt.Sprintf("documents in %q", db)); err != nil {
		return DocPage{}, err
	}
	page := DocPage{TotalRows: body.TotalRows}
	for _, r := range body.Rows {
		row := DocRow{ID: r.ID, Key: r.Key, Value: r.Value, Doc: r.Doc, Error: r.Error}
		row.Rev = revFromValue(r.Value)
		page.Rows = append(page.Rows, row)
	}
	if opts.Limit > 0 && len(page.Rows) > opts.Limit {
		page.NextStartKeyDocID = page.Rows[opts.Limit].ID
		page.Rows = page.Rows[:opts.Limit]
	}
	return page, nil
}

// AllDocsStream walks every row, page by page, calling fn for each.
func (c *Client) AllDocsStream(ctx context.Context, db string, opts AllDocsOptions, fn func(DocRow) error) error {
	if opts.Limit <= 0 {
		opts.Limit = 500
	}
	for {
		page, err := c.AllDocs(ctx, db, opts)
		if err != nil {
			return err
		}
		for _, row := range page.Rows {
			if err := fn(row); err != nil {
				return err
			}
		}
		if page.NextStartKeyDocID == "" {
			return nil
		}
		opts.StartKeyDocID = page.NextStartKeyDocID
	}
}

// DesignDoc reads a design document body.
func (c *Client) DesignDoc(ctx context.Context, db, ddocID string) (json.RawMessage, error) {
	var raw json.RawMessage
	target := fmt.Sprintf("design document %q in %q", ddocID, db)
	if err := c.DoJSON(ctx, "GET", docPath(db, ddocID), nil, &raw, "read", target); err != nil {
		return nil, err
	}
	return raw, nil
}

// CreateDatabase creates a database. q of 0 leaves the server default.
func (c *Client) CreateDatabase(ctx context.Context, db string, partitioned bool, q int) error {
	apiPath := "/" + path.Encode(db)
	query := url.Values{}
	if partitioned {
		query.Set("partitioned", "true")
	}
	if q > 0 {
		query.Set("q", strconv.Itoa(q))
	}
	if enc := query.Encode(); enc != "" {
		apiPath += "?" + enc
	}
	return c.DoJSON(ctx, "PUT", apiPath, nil, nil, "create", fmt.Sprintf("database %q", db))
}

// DestroyDatabase deletes a database and everything in it.
func (c *Client) DestroyDatabase(ctx context.Context, db string) error {
	return c.DoJSON(ctx, "DELETE", "/"+path.Encode(db), nil, nil, "delete", fmt.Sprintf("database %q", db))
}

// ReplicationEndpoint describes db as a "source" or "target" value for a
// CouchDB _replicator document: a full URL plus whatever credentials this
// client authenticates with. CouchDB 3.x rejects a bare database name with
// 403 "local_endpoints_not_supported", so even a same-server copy needs a
// full URL; CouchDB's per-endpoint auth object lets that URL stay free of an
// embedded password, unlike putting the credentials in the URL itself.
//
// The returned map is for request bodies only — session auth puts the
// plaintext password under "auth", and JWT puts the bearer token under
// "headers" — and must never be rendered, logged, or surfaced in a
// command.Result. It is handed straight to DoJSON, which marshals it as part
// of the replicator document and nowhere else.
func (c *Client) ReplicationEndpoint(db string) map[string]any {
	endpoint := map[string]any{"url": c.safe + "/" + path.Encode(db)}
	switch c.cfg.Auth {
	case AuthSession:
		endpoint["auth"] = map[string]any{"basic": map[string]any{
			"username": c.cfg.Username,
			"password": c.cfg.Secret,
		}}
	case AuthJWT:
		endpoint["headers"] = map[string]any{"Authorization": "Bearer " + c.cfg.Secret}
	default:
		// AuthNone still authenticates when the raw URL carried userinfo
		// (e.g. "cdb http://admin:pw@host"); c.base keeps that userinfo,
		// c.safe never does. Moving it into auth.basic here means it never
		// has to go back into a URL.
		if u, err := url.Parse(c.base); err == nil && u.User != nil {
			password, _ := u.User.Password()
			endpoint["auth"] = map[string]any{"basic": map[string]any{
				"username": u.User.Username(),
				"password": password,
			}}
		}
	}
	return endpoint
}

func revFromValue(v json.RawMessage) string {
	if len(v) == 0 {
		return ""
	}
	var out struct {
		Rev string `json:"rev"`
	}
	if err := json.Unmarshal(v, &out); err != nil {
		return ""
	}
	return out.Rev
}
