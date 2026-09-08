package couch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/sriannamalai/CDB.CLI/internal/path"
)

// GetOptions configure a document read.
type GetOptions struct {
	Rev       string
	Revs      bool
	Conflicts bool
	RevsInfo  bool
	Latest    bool
}

// docPath builds /db/docid. The database name is escaped; a document id keeps
// its "_design/" or "_local/" prefix unescaped because the slash is meaningful,
// and the remainder is escaped.
func docPath(db, docID string) string {
	base := "/" + path.Encode(db) + "/"
	for _, prefix := range []string{"_design/", "_local/"} {
		if strings.HasPrefix(docID, prefix) {
			return base + prefix + path.Encode(strings.TrimPrefix(docID, prefix))
		}
	}
	return base + path.Encode(docID)
}

func docTarget(db, docID string) string {
	return fmt.Sprintf("document %q in %q", docID, db)
}

// GetDocument reads a document and returns its raw body and _rev.
func (c *Client) GetDocument(ctx context.Context, db, docID string, opts GetOptions) (json.RawMessage, string, error) {
	q := url.Values{}
	if opts.Rev != "" {
		q.Set("rev", opts.Rev)
	}
	if opts.Revs {
		q.Set("revs", "true")
	}
	if opts.Conflicts {
		q.Set("conflicts", "true")
	}
	if opts.RevsInfo {
		q.Set("revs_info", "true")
	}
	if opts.Latest {
		q.Set("latest", "true")
	}
	apiPath := docPath(db, docID)
	if enc := q.Encode(); enc != "" {
		apiPath += "?" + enc
	}
	var raw json.RawMessage
	if err := c.DoJSON(ctx, http.MethodGet, apiPath, nil, &raw, "read", docTarget(db, docID)); err != nil {
		return nil, "", err
	}
	var meta struct {
		Rev string `json:"_rev"`
	}
	// A document without a _rev is not an error here: an old revision read with
	// ?rev= still has one, and a caller that does not need the rev ignores it.
	_ = json.Unmarshal(raw, &meta)
	return raw, meta.Rev, nil
}

// GetRev reads the current revision with HEAD.
func (c *Client) GetRev(ctx context.Context, db, docID string) (string, error) {
	req, err := c.NewRequest(ctx, http.MethodHead, docPath(db, docID), nil)
	if err != nil {
		return "", Wrap(err, "read", docTarget(db, docID))
	}
	res, err := c.HTTP().Do(req)
	if err != nil {
		return "", Wrap(err, "read", docTarget(db, docID))
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		// A HEAD has no body to read a reason out of, so the status text stands
		// in for one.
		return "", NewError(res.StatusCode, nameForStatus(res.StatusCode), http.StatusText(res.StatusCode), "read", docTarget(db, docID))
	}
	return strings.Trim(res.Header.Get("ETag"), `"`), nil
}

// writeResult is the shape CouchDB returns from PUT, DELETE and COPY.
type writeResult struct {
	OK  bool   `json:"ok"`
	ID  string `json:"id"`
	Rev string `json:"rev"`
}

// PutDocument writes a document and returns the new revision. rev may be empty
// for a create.
func (c *Client) PutDocument(ctx context.Context, db, docID string, doc json.RawMessage, rev string) (string, error) {
	apiPath := docPath(db, docID)
	if rev != "" {
		apiPath += "?rev=" + url.QueryEscape(rev)
	}
	req, err := c.NewRequest(ctx, http.MethodPut, apiPath, bytes.NewReader(doc))
	if err != nil {
		return "", Wrap(err, "write", docTarget(db, docID))
	}
	req.Header.Set("Content-Type", "application/json")
	var out writeResult
	if err := c.doDecode(req, &out, "write", docTarget(db, docID)); err != nil {
		return "", err
	}
	return out.Rev, nil
}

// DeleteDocument removes a document and returns the tombstone revision.
func (c *Client) DeleteDocument(ctx context.Context, db, docID, rev string) (string, error) {
	apiPath := docPath(db, docID) + "?rev=" + url.QueryEscape(rev)
	req, err := c.NewRequest(ctx, http.MethodDelete, apiPath, nil)
	if err != nil {
		return "", Wrap(err, "delete", docTarget(db, docID))
	}
	var out writeResult
	if err := c.doDecode(req, &out, "delete", docTarget(db, docID)); err != nil {
		return "", err
	}
	return out.Rev, nil
}

// CopyDocument issues a CouchDB COPY. dstRev must be set when overwriting.
func (c *Client) CopyDocument(ctx context.Context, db, srcID, dstID, dstRev string) (string, error) {
	req, err := c.NewRequest(ctx, "COPY", docPath(db, srcID), nil)
	if err != nil {
		return "", Wrap(err, "copy", docTarget(db, srcID))
	}
	dest := dstID
	if dstRev != "" {
		dest += "?rev=" + dstRev
	}
	req.Header.Set("Destination", dest)
	var out writeResult
	if err := c.doDecode(req, &out, "copy", docTarget(db, srcID)); err != nil {
		return "", err
	}
	return out.Rev, nil
}
