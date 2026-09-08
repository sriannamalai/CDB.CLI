package couch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	"github.com/sriannamalai/CDB.CLI/internal/path"
)

// ChangeRow is one entry of the changes feed.
type ChangeRow struct {
	ID      string
	Seq     string
	Deleted bool
	// Revs holds every leaf revision, because the feed is read with
	// style=all_docs so conflicts are not lost.
	Revs []string
}

// ChangesPage is one batch of the changes feed.
type ChangesPage struct {
	Rows    []ChangeRow
	LastSeq string
	Pending int64
}

// Changes reads one batch of the normal changes feed with style=all_docs.
//
// CouchDB does not honour revs=true on _changes (verified on 3.5.2), so this
// returns ids and leaf revisions only; use BulkGet to fetch full documents
// including _revisions.
func (c *Client) Changes(ctx context.Context, db, since string, limit int) (ChangesPage, error) {
	q := url.Values{}
	q.Set("feed", "normal")
	q.Set("style", "all_docs")
	if since == "" {
		since = "0"
	}
	q.Set("since", since)
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var body struct {
		Results []struct {
			Seq     json.RawMessage `json:"seq"`
			ID      string          `json:"id"`
			Deleted bool            `json:"deleted"`
			Changes []struct {
				Rev string `json:"rev"`
			} `json:"changes"`
		} `json:"results"`
		LastSeq json.RawMessage `json:"last_seq"`
		Pending int64           `json:"pending"`
	}
	apiPath := "/" + path.Encode(db) + "/_changes?" + q.Encode()
	if err := c.DoJSON(ctx, "GET", apiPath, nil, &body, "read", fmt.Sprintf("changes for %q", db)); err != nil {
		return ChangesPage{}, err
	}
	page := ChangesPage{LastSeq: seqString(body.LastSeq), Pending: body.Pending}
	for _, r := range body.Results {
		row := ChangeRow{ID: r.ID, Seq: seqString(r.Seq), Deleted: r.Deleted}
		for _, ch := range r.Changes {
			row.Revs = append(row.Revs, ch.Rev)
		}
		page.Rows = append(page.Rows, row)
	}
	return page, nil
}

// seqString renders a sequence, which CouchDB 3.x sends as a string but older
// or clustered responses may send as a number.
func seqString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return string(raw)
}

// BulkRef names one document revision for BulkGet.
type BulkRef struct {
	ID  string `json:"id"`
	Rev string `json:"rev,omitempty"`
}

// BulkGet fetches full documents. With revs set, each document carries its
// _revisions history, which restore needs for new_edits=false. Entries the
// server reports as errors are skipped.
func (c *Client) BulkGet(ctx context.Context, db string, refs []BulkRef, revs bool) ([]json.RawMessage, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	apiPath := "/" + path.Encode(db) + "/_bulk_get"
	if revs {
		apiPath += "?revs=true"
	}
	var body struct {
		Results []struct {
			ID   string `json:"id"`
			Docs []struct {
				OK    json.RawMessage `json:"ok"`
				Error json.RawMessage `json:"error"`
			} `json:"docs"`
		} `json:"results"`
	}
	req := map[string]any{"docs": refs}
	if err := c.DoJSON(ctx, "POST", apiPath, req, &body, "read", fmt.Sprintf("documents in %q", db)); err != nil {
		return nil, err
	}
	var out []json.RawMessage
	for _, r := range body.Results {
		for _, d := range r.Docs {
			if len(d.OK) > 0 {
				out = append(out, d.OK)
			}
		}
	}
	return out, nil
}

// BulkError is one per-document failure from _bulk_docs.
type BulkError struct {
	ID     string `json:"id"`
	Rev    string `json:"rev"`
	Error  string `json:"error"`
	Reason string `json:"reason"`
}

// BulkDocs writes a batch. With newEdits false, CouchDB keeps each document's
// supplied _rev and _revisions, which is how restore preserves revisions;
// documents must not carry attachment stubs in that mode.
func (c *Client) BulkDocs(ctx context.Context, db string, docs []json.RawMessage, newEdits bool) ([]BulkError, error) {
	if len(docs) == 0 {
		return nil, nil
	}
	req := map[string]any{"docs": docs}
	if !newEdits {
		req["new_edits"] = false
	}
	var results []BulkError
	apiPath := "/" + path.Encode(db) + "/_bulk_docs"
	if err := c.DoJSON(ctx, "POST", apiPath, req, &results, "write", fmt.Sprintf("documents in %q", db)); err != nil {
		return nil, err
	}
	var failures []BulkError
	for _, r := range results {
		if r.Error != "" {
			failures = append(failures, r)
		}
	}
	return failures, nil
}
