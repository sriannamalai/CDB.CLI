package couch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/sriannamalai/CDB.CLI/internal/path"
)

// SearchOptions configure a full-text query. Paging uses Bookmark, never skip.
//
// Sort and Ranges are passed to the server exactly as the operator gave them:
// both are backend-specific grammars (Clouseau's "-field<number>" and
// Nouveau's own), and rewriting them here would be cdb guessing at a syntax
// that is the server's to define.
type SearchOptions struct {
	// Query is the Lucene query string. It travels as "q", which both
	// backends accept; Nouveau also accepts "query", which cdb does not use so
	// that one request shape serves both.
	Query       string
	Limit       int
	Bookmark    string
	Sort        string
	IncludeDocs bool
	Counts      []string
	Ranges      json.RawMessage
	// Drilldown is one two-element [field, value] pair per entry, each sent as
	// its own "drilldown" parameter.
	Drilldown [][]string
}

// SearchRow is one hit, normalised across the two backends.
type SearchRow struct {
	ID string
	// Order holds the sort values, Clouseau's raw ones and Nouveau's unwrapped
	// from their {"value","@type"} objects, so a caller sees one shape.
	Order  []any
	Fields map[string]any
	// Doc is nil unless the query asked for the documents.
	Doc map[string]any
}

// SearchResult is one page of hits, normalised across the two backends:
// Clouseau's total_rows/rows and Nouveau's total_hits/hits arrive as the same
// two fields.
type SearchResult struct {
	Total    int64
	Bookmark string
	Rows     []SearchRow
	Counts   map[string]any
	Ranges   map[string]any
}

// SearchInfo is an index's metadata. Index is a map rather than named fields
// because the two backends do not report the same ones -- Clouseau sends
// committed_seq, pending_seq, doc_count, doc_del_count, disk_size and
// signature; Nouveau sends update_seq, purge_seq, num_docs, disk_size and
// signature -- and a fixed struct would drop half of each.
type SearchInfo struct {
	Name  string
	Index map[string]any
}

// SearchTarget names a search index for an error sentence, in the shape
// internal/render's describeTarget reads. The design document is named without
// its "_design/" prefix, which is noise in a sentence the operator reads.
func SearchTarget(db, ddocID, index string) string {
	return fmt.Sprintf("search index %q in %q", strings.TrimPrefix(ddocID, "_design/")+"/"+index, db)
}

// searchEndpoint is the path segment each backend answers on, for queries and
// for info.
func searchEndpoint(b path.Backend, info bool) string {
	switch {
	case b == path.BackendNouveau && info:
		return "_nouveau_info"
	case b == path.BackendNouveau:
		return "_nouveau"
	case info:
		return "_search_info"
	default:
		return "_search"
	}
}

// searchPath builds /db[/_partition/key]/_design/app/<endpoint>/<index>. It
// sits beside mangoPath and uses the same two helpers, so a database or index
// name with a slash or a "#" in it is escaped exactly once, in one place.
func searchPath(db, partition, ddocID, endpoint, index string) string {
	return dbBase(db, partition) + "/" + encodeDocID(ddocID) + "/" + endpoint + "/" + path.Encode(index)
}

func (o SearchOptions) query() url.Values {
	q := url.Values{}
	q.Set("q", o.Query)
	if o.Limit > 0 {
		// The limit is sent as asked for. Unlike a view, search pages by
		// bookmark, so there is no need to fetch one extra row to discover
		// whether a next page exists -- the bookmark says so.
		q.Set("limit", strconv.Itoa(o.Limit))
	}
	if o.Bookmark != "" {
		q.Set("bookmark", o.Bookmark)
	}
	if o.Sort != "" {
		q.Set("sort", o.Sort)
	}
	if o.IncludeDocs {
		q.Set("include_docs", "true")
	}
	if len(o.Counts) > 0 {
		if b, err := json.Marshal(o.Counts); err == nil {
			q.Set("counts", string(b))
		}
	}
	if len(o.Ranges) > 0 {
		q.Set("ranges", string(o.Ranges))
	}
	for _, d := range o.Drilldown {
		if b, err := json.Marshal(d); err == nil {
			q.Add("drilldown", string(b))
		}
	}
	return q
}

// searchBody decodes both backends at once: no field name collides, so the
// members that are absent stay at their zero values.
type searchBody struct {
	TotalRows *int64         `json:"total_rows"`
	TotalHits *int64         `json:"total_hits"`
	Bookmark  string         `json:"bookmark"`
	Rows      []searchHit    `json:"rows"`
	Hits      []searchHit    `json:"hits"`
	Counts    map[string]any `json:"counts"`
	Ranges    map[string]any `json:"ranges"`
}

type searchHit struct {
	ID     string            `json:"id"`
	Order  []json.RawMessage `json:"order"`
	Fields map[string]any    `json:"fields"`
	Doc    map[string]any    `json:"doc"`
}

// Search runs a full-text query against a KindSearch target.
func (c *Client) Search(ctx context.Context, t path.Target, opts SearchOptions) (SearchResult, error) {
	apiPath := searchPath(t.Database, t.Partition, t.DocID, searchEndpoint(t.Backend, false), t.Index)
	if enc := opts.query().Encode(); enc != "" {
		apiPath += "?" + enc
	}
	var body searchBody
	target := SearchTarget(t.Database, t.DocID, t.Index)
	if err := c.DoJSON(ctx, "GET", apiPath, nil, &body, "query", target); err != nil {
		return SearchResult{}, err
	}
	out := SearchResult{Bookmark: body.Bookmark, Counts: body.Counts, Ranges: body.Ranges}
	switch {
	case body.TotalRows != nil:
		out.Total = *body.TotalRows
	case body.TotalHits != nil:
		out.Total = *body.TotalHits
	}
	hits := body.Rows
	if hits == nil {
		hits = body.Hits
	}
	for _, h := range hits {
		out.Rows = append(out.Rows, SearchRow{ID: h.ID, Order: unwrapOrder(h.Order), Fields: h.Fields, Doc: h.Doc})
	}
	return out, nil
}

// unwrapOrder flattens Nouveau's sort values, which arrive as
// {"value":1.25,"@type":"float"} objects, to the bare values Clouseau sends.
// A value that is not one of those objects is decoded as it stands, which is
// every Clouseau order element.
func unwrapOrder(raw []json.RawMessage) []any {
	if len(raw) == 0 {
		return nil
	}
	out := make([]any, 0, len(raw))
	for _, r := range raw {
		var wrapped struct {
			Value *json.RawMessage `json:"value"`
		}
		if err := json.Unmarshal(r, &wrapped); err == nil && wrapped.Value != nil {
			var v any
			if json.Unmarshal(*wrapped.Value, &v) == nil {
				out = append(out, v)
				continue
			}
		}
		var v any
		if json.Unmarshal(r, &v) == nil {
			out = append(out, v)
		}
	}
	return out
}

// SearchInfo reads an index's metadata from _search_info or _nouveau_info.
func (c *Client) SearchInfo(ctx context.Context, t path.Target) (SearchInfo, error) {
	apiPath := searchPath(t.Database, "", t.DocID, searchEndpoint(t.Backend, true), t.Index)
	var body struct {
		Name  string         `json:"name"`
		Index map[string]any `json:"search_index"`
	}
	target := SearchTarget(t.Database, t.DocID, t.Index)
	if err := c.DoJSON(ctx, "GET", apiPath, nil, &body, "read", target); err != nil {
		return SearchInfo{}, err
	}
	return SearchInfo{Name: body.Name, Index: body.Index}, nil
}
