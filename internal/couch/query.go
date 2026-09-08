package couch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	"github.com/sriannamalai/CDB.CLI/internal/path"
)

// FindOptions configure a Mango query. Paging uses Bookmark, never skip.
type FindOptions struct {
	Selector  json.RawMessage
	Fields    []string
	Sort      []json.RawMessage
	Limit     int
	Bookmark  string
	Partition string
	UseIndex  []string
}

// FindPage is one page of Mango results.
type FindPage struct {
	Docs     []json.RawMessage
	Bookmark string
	Warning  string
}

func (o FindOptions) body() map[string]any {
	sel := o.Selector
	if len(sel) == 0 {
		sel = json.RawMessage(`{}`)
	}
	m := map[string]any{"selector": sel}
	if len(o.Fields) > 0 {
		m["fields"] = o.Fields
	}
	if len(o.Sort) > 0 {
		m["sort"] = o.Sort
	}
	if o.Limit > 0 {
		m["limit"] = o.Limit
	}
	if o.Bookmark != "" {
		m["bookmark"] = o.Bookmark
	}
	if len(o.UseIndex) == 1 {
		m["use_index"] = o.UseIndex[0]
	} else if len(o.UseIndex) > 1 {
		m["use_index"] = o.UseIndex
	}
	return m
}

func mangoPath(db, partition, endpoint string) string {
	base := "/" + path.Encode(db)
	if partition != "" {
		base += "/_partition/" + path.Encode(partition)
	}
	return base + endpoint
}

// Find runs POST /db/_find.
func (c *Client) Find(ctx context.Context, db string, opts FindOptions) (FindPage, error) {
	var body struct {
		Docs     []json.RawMessage `json:"docs"`
		Bookmark string            `json:"bookmark"`
		Warning  string            `json:"warning"`
	}
	target := fmt.Sprintf("documents in %q", db)
	if err := c.DoJSON(ctx, "POST", mangoPath(db, opts.Partition, "/_find"), opts.body(), &body, "query", target); err != nil {
		return FindPage{}, err
	}
	return FindPage{Docs: body.Docs, Bookmark: body.Bookmark, Warning: body.Warning}, nil
}

// Explain runs POST /db/_explain.
func (c *Client) Explain(ctx context.Context, db string, opts FindOptions) (json.RawMessage, error) {
	var raw json.RawMessage
	target := fmt.Sprintf("query plan for %q", db)
	if err := c.DoJSON(ctx, "POST", mangoPath(db, opts.Partition, "/_explain"), opts.body(), &raw, "query", target); err != nil {
		return nil, err
	}
	return raw, nil
}

// ViewOptions configure a view query. Paging uses StartKey plus StartKeyDocID,
// never skip.
type ViewOptions struct {
	Key           json.RawMessage
	StartKey      json.RawMessage
	EndKey        json.RawMessage
	StartKeyDocID string
	Limit         int
	Descending    bool
	IncludeDocs   bool
	Reduce        *bool
	GroupLevel    *int
	Partition     string
}

// ViewPage is one page of view rows.
type ViewPage struct {
	Rows              []DocRow
	TotalRows         int64
	NextStartKey      json.RawMessage
	NextStartKeyDocID string
}

// query builds the query string. The key parameters are already JSON text, and
// url.Values escapes them: they are never concatenated into the path by hand.
func (o ViewOptions) query() url.Values {
	q := url.Values{}
	if o.Limit > 0 {
		// One extra row tells us whether there is a next page and where it
		// starts. CouchDB's skip is never used.
		q.Set("limit", strconv.Itoa(o.Limit+1))
	}
	if len(o.Key) > 0 {
		q.Set("key", string(o.Key))
	}
	if len(o.StartKey) > 0 {
		q.Set("start_key", string(o.StartKey))
	}
	if len(o.EndKey) > 0 {
		q.Set("end_key", string(o.EndKey))
	}
	if o.StartKeyDocID != "" {
		q.Set("startkey_docid", o.StartKeyDocID)
	}
	if o.Descending {
		q.Set("descending", "true")
	}
	if o.IncludeDocs {
		q.Set("include_docs", "true")
	}
	if o.Reduce != nil {
		q.Set("reduce", strconv.FormatBool(*o.Reduce))
	}
	if o.GroupLevel != nil {
		q.Set("group_level", strconv.Itoa(*o.GroupLevel))
	}
	return q
}

// Query runs a map/reduce view. ddocID includes the "_design/" prefix.
func (c *Client) Query(ctx context.Context, db, ddocID, view string, opts ViewOptions) (ViewPage, error) {
	base := "/" + path.Encode(db)
	if opts.Partition != "" {
		base += "/_partition/" + path.Encode(opts.Partition)
	}
	apiPath := base + "/" + ddocID + "/_view/" + path.Encode(view)
	if enc := opts.query().Encode(); enc != "" {
		apiPath += "?" + enc
	}
	var body allDocsBody
	target := fmt.Sprintf("view %q in %q", view, db)
	if err := c.DoJSON(ctx, "GET", apiPath, nil, &body, "query", target); err != nil {
		return ViewPage{}, err
	}
	page := ViewPage{TotalRows: body.TotalRows}
	for _, r := range body.Rows {
		page.Rows = append(page.Rows, DocRow{ID: r.ID, Key: r.Key, Value: r.Value, Doc: r.Doc, Error: r.Error, Rev: revFromValue(r.Value)})
	}
	if opts.Limit > 0 && len(page.Rows) > opts.Limit {
		page.NextStartKey = page.Rows[opts.Limit].Key
		page.NextStartKeyDocID = page.Rows[opts.Limit].ID
		page.Rows = page.Rows[:opts.Limit]
	}
	return page, nil
}
