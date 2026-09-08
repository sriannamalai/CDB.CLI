package couch

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/sriannamalai/CDB.CLI/internal/path"
)

// Feed styles for ChangesOptions.Style.
const (
	// StyleMainOnly returns one change per document — the winning revision.
	// It is the zero value, and what "tail" wants.
	StyleMainOnly = "main_only"
	// StyleAllDocs returns every leaf revision of a changed document, so a
	// conflicted document is not silently reduced to its winner. It is what
	// "backup" wants.
	StyleAllDocs = "all_docs"
)

// ChangesOptions configure a read of the changes feed. The zero value asks for
// the whole feed from the beginning, one row per document.
type ChangesOptions struct {
	// Since is the update sequence to start from. "" means "0"; the server
	// also accepts "now", which is where a follow starts by default.
	Since string
	// Limit bounds the normal feed. 0 reads to the end of it. The continuous
	// feed ignores this: it has no end to bound.
	Limit int
	// Style is StyleMainOnly (the zero value) or StyleAllDocs.
	Style string
	// IncludeDocs asks the server for each changed document's body, which
	// arrives in ChangeRow.Doc.
	IncludeDocs bool
	// Filter names a design-document filter as "ddoc/name", or is empty.
	Filter string
	// HeartbeatMS is how often the server sends a blank keep-alive line on the
	// continuous feed. 0 sends none. The normal feed ignores it.
	HeartbeatMS int
}

// changesQuery builds the query string for either feed. It is shared by
// Changes and ChangesFollow so the two cannot drift on defaults.
func (o ChangesOptions) changesQuery(continuous bool) url.Values {
	q := url.Values{}
	if continuous {
		q.Set("feed", "continuous")
	} else {
		q.Set("feed", "normal")
	}
	style := o.Style
	if style == "" {
		style = StyleMainOnly
	}
	q.Set("style", style)
	since := o.Since
	if since == "" {
		since = "0"
	}
	q.Set("since", since)
	if !continuous && o.Limit > 0 {
		q.Set("limit", strconv.Itoa(o.Limit))
	}
	if o.IncludeDocs {
		q.Set("include_docs", "true")
	}
	if o.Filter != "" {
		q.Set("filter", o.Filter)
	}
	if continuous && o.HeartbeatMS > 0 {
		q.Set("heartbeat", strconv.Itoa(o.HeartbeatMS))
	}
	return q
}

// ChangeRow is one entry of the changes feed.
type ChangeRow struct {
	ID      string
	Seq     string
	Deleted bool
	// Revs holds every revision the row named. Under StyleAllDocs that is
	// every leaf revision, so conflicts are not lost; under StyleMainOnly it
	// holds exactly one.
	Revs []string
	// Doc is the changed document's body, set only when
	// ChangesOptions.IncludeDocs is.
	Doc json.RawMessage
}

// ChangesPage is one batch of the changes feed.
type ChangesPage struct {
	Rows    []ChangeRow
	LastSeq string
	Pending int64
}

// changeLine is the wire shape of one entry, shared by both feeds: the normal
// feed nests them under "results", the continuous feed writes one per line.
type changeLine struct {
	Seq     json.RawMessage `json:"seq"`
	ID      string          `json:"id"`
	Deleted bool            `json:"deleted"`
	Changes []struct {
		Rev string `json:"rev"`
	} `json:"changes"`
	Doc json.RawMessage `json:"doc"`
}

func (l changeLine) toRow() ChangeRow {
	row := ChangeRow{ID: l.ID, Seq: seqString(l.Seq), Deleted: l.Deleted, Doc: l.Doc}
	for _, ch := range l.Changes {
		row.Revs = append(row.Revs, ch.Rev)
	}
	return row
}

// Changes reads one batch of the normal changes feed.
//
// CouchDB does not honour revs=true on _changes (verified on 3.5.2), so a row
// carries ids and revisions only; use BulkGet to fetch full documents
// including _revisions. IncludeDocs asks for bodies, but they arrive without
// _revisions for the same reason.
func (c *Client) Changes(ctx context.Context, db string, opts ChangesOptions) (ChangesPage, error) {
	var body struct {
		Results []changeLine    `json:"results"`
		LastSeq json.RawMessage `json:"last_seq"`
		Pending int64           `json:"pending"`
	}
	apiPath := "/" + path.Encode(db) + "/_changes?" + opts.changesQuery(false).Encode()
	if err := c.DoJSON(ctx, "GET", apiPath, nil, &body, "read", fmt.Sprintf("changes for %q", db)); err != nil {
		return ChangesPage{}, err
	}
	page := ChangesPage{LastSeq: seqString(body.LastSeq), Pending: body.Pending}
	for _, r := range body.Results {
		page.Rows = append(page.Rows, r.toRow())
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

// changesScanBuffer is the largest single line ChangesFollow will accept. One
// line is one change, and with include_docs that line carries a whole
// document; 4 MiB is well past any document CouchDB will hand back in a feed
// and still a bound, which bufio.Scanner requires.
const changesScanBuffer = 4 << 20

// ChangesFollow reads the continuous changes feed, calling fn for each change
// as it arrives. It returns when the body ends, when fn returns an error, when
// the server answers with a non-2xx status, or when ctx is cancelled.
//
// It never retries. A dropped feed is reported as a nil error (the body simply
// ended) or as a transport *Error, and the caller decides whether to reconnect
// — the backoff policy belongs to the command, not the client.
//
// Blank lines are the server's heartbeat and are ignored; so is the trailing
// {"last_seq":…} line, which carries no id.
func (c *Client) ChangesFollow(ctx context.Context, db string, opts ChangesOptions, fn func(ChangeRow) error) error {
	target := fmt.Sprintf("changes for %q", db)
	apiPath := "/" + path.Encode(db) + "/_changes?" + opts.changesQuery(true).Encode()
	req, err := c.NewRequest(ctx, http.MethodGet, apiPath, nil)
	if err != nil {
		return Wrap(err, "read", target)
	}
	res, err := c.HTTP().Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return Wrap(err, "read", target)
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		// The feed never started, so the error body is small and complete.
		// Decode it exactly as doDecode would, including the 401 retarget, so
		// a dead feed reads like every other failure.
		var e struct {
			Error  string `json:"error"`
			Reason string `json:"reason"`
		}
		_ = json.NewDecoder(res.Body).Decode(&e)
		if e.Error == "" {
			e.Error = nameForStatus(res.StatusCode)
		}
		if res.StatusCode == http.StatusUnauthorized {
			return c.unauthorized(e.Reason, "read", target)
		}
		return NewError(res.StatusCode, e.Error, e.Reason, "read", target)
	}

	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 0, 64*1024), changesScanBuffer)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue // heartbeat
		}
		var l changeLine
		if err := json.Unmarshal(line, &l); err != nil {
			// A line that will not decode is not worth ending a tail over:
			// the next one usually will. Skipping keeps a long-running follow
			// alive across anything unexpected the server writes.
			continue
		}
		if l.ID == "" {
			continue // the trailing {"last_seq":…} line
		}
		if err := fn(l.toRow()); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil {
		// A cancelled context surfaces here as a read error on the body. The
		// caller has to see context.Canceled itself, because that is what maps
		// to exit 130 and to "print nothing".
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if errors.Is(err, bufio.ErrTooLong) {
			// A change too large to read is terminal, not a dropped feed:
			// reconnecting from the same sequence would meet the same line
			// again, every time, forever. A 413 is not retried by the
			// command's reconnect rule (only StatusUnreachable and 5xx are),
			// and it says plainly what the limit was.
			return NewError(http.StatusRequestEntityTooLarge, "too_large",
				fmt.Sprintf("a change was larger than the %d MiB this feed can read; re-run without --include-docs", changesScanBuffer>>20),
				"read", target)
		}
		return Wrap(err, "read", target)
	}
	return nil
}

// BulkRef names one document revision for BulkGet.
type BulkRef struct {
	ID  string `json:"id"`
	Rev string `json:"rev,omitempty"`
}

// BulkError is one per-document failure from _bulk_get or _bulk_docs.
type BulkError struct {
	ID     string `json:"id"`
	Rev    string `json:"rev"`
	Error  string `json:"error"`
	Reason string `json:"reason"`
}

// BulkGet fetches full documents. With revs set, each document carries its
// _revisions history, which restore needs for new_edits=false.
//
// It returns the fetched documents and, separately, every entry the server
// reported as an error — a revision purged or compacted away between reading
// the changes feed and fetching it, say. The failures are returned rather than
// dropped: a caller that silently skipped them would produce a dump missing
// documents it claims to hold.
func (c *Client) BulkGet(ctx context.Context, db string, refs []BulkRef, revs bool) ([]json.RawMessage, []BulkError, error) {
	if len(refs) == 0 {
		return nil, nil, nil
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
				Error *BulkError      `json:"error"`
			} `json:"docs"`
		} `json:"results"`
	}
	req := map[string]any{"docs": refs}
	if err := c.DoJSON(ctx, "POST", apiPath, req, &body, "read", fmt.Sprintf("documents in %q", db)); err != nil {
		return nil, nil, err
	}
	var (
		out    []json.RawMessage
		failed []BulkError
	)
	for _, r := range body.Results {
		for _, d := range r.Docs {
			switch {
			case len(d.OK) > 0:
				out = append(out, d.OK)
			case d.Error != nil:
				e := *d.Error
				if e.ID == "" {
					e.ID = r.ID
				}
				failed = append(failed, e)
			}
		}
	}
	return out, failed, nil
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
