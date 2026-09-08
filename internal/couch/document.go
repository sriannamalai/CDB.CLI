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

// dbBase builds /db, or /db/_partition/key for a partitioned request. It is
// the single place the partition prefix is spelled out.
func dbBase(db, partition string) string {
	base := "/" + path.Encode(db)
	if partition != "" {
		base += "/_partition/" + path.Encode(partition)
	}
	return base
}

// encodeDocID escapes a document id for a URL path. A "_design/" or "_local/"
// prefix keeps its slash, which is meaningful, and the remainder is escaped.
// Every design-document URL — GetDocument, DesignDoc, Query — must go through
// here: an unescaped "#" truncates the path at the fragment and a "?" at the
// query string, which is how "cat" and "ls" came to disagree about the same
// document.
func encodeDocID(docID string) string {
	for _, prefix := range []string{"_design/", "_local/"} {
		if strings.HasPrefix(docID, prefix) {
			return prefix + path.Encode(strings.TrimPrefix(docID, prefix))
		}
	}
	return path.Encode(docID)
}

// docPath builds /db/docid.
func docPath(db, docID string) string {
	return dbBase(db, "") + "/" + encodeDocID(docID)
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
		if res.StatusCode == http.StatusUnauthorized {
			// put, rm, cp and attach all call GetRev for optimistic concurrency,
			// and under jwt/none auth there is no session transport to retry a
			// 401 first, so this is reachable directly. It needs the same
			// user-and-host target as doDecode's 401, not the document target,
			// so the rendered sentence names the real user rather than the
			// document being read.
			return "", NewError(res.StatusCode, "unauthorized", http.StatusText(res.StatusCode), "read", unauthorizedTarget(c.cfg.Username, c.host))
		}
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
