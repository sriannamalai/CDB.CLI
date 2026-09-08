package couch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/sriannamalai/CDB.CLI/internal/path"
)

// AttachmentMeta describes one attachment from a document's _attachments map.
type AttachmentMeta struct {
	Name        string
	ContentType string
	Digest      string
	// Length is CouchDB's reported length. Verified on CouchDB 3.5.2: when the
	// server stored the attachment gzip-compressed this is the compressed size,
	// so a caller that needs the exact byte count must count what it reads.
	Length int64
	RevPos int64
}

// AttachmentStream is a streamed attachment read. The bytes are never buffered
// in memory: Content is the live response body, and the caller must close it.
type AttachmentStream struct {
	ContentType string
	// Length is -1 when the response had no Content-Length, which is normal:
	// CouchDB answers a streamed attachment read chunked.
	Length  int64
	Content io.ReadCloser
}

// attPath builds /db/docid/name. The name is one path segment, escaped exactly
// once, the same way docPath escapes the database and document id.
func attPath(db, docID, name string) string {
	return docPath(db, docID) + "/" + path.Encode(name)
}

// AttachmentTarget names an attachment for an error sentence. A design document
// carries attachments like any other document, so /db/_design/app/logo.png is a
// real address; naming the parent `"_design/app"` made the message read as
// though the operator had mistyped a view path, so a design-document parent is
// spelled out as one.
func AttachmentTarget(db, docID, name string) string {
	if ddoc, ok := strings.CutPrefix(docID, "_design/"); ok {
		return fmt.Sprintf("attachment %q of design document %q in %q", name, ddoc, db)
	}
	return fmt.Sprintf("attachment %q of %q in %q", name, docID, db)
}

func attTarget(db, docID, name string) string { return AttachmentTarget(db, docID, name) }

// ListAttachments reads a document's _attachments map, sorted by name.
func (c *Client) ListAttachments(ctx context.Context, db, docID string) ([]AttachmentMeta, error) {
	var body struct {
		Attachments map[string]struct {
			ContentType string `json:"content_type"`
			Length      int64  `json:"length"`
			Digest      string `json:"digest"`
			RevPos      int64  `json:"revpos"`
		} `json:"_attachments"`
	}
	if err := c.DoJSON(ctx, http.MethodGet, docPath(db, docID), nil, &body, "read", docTarget(db, docID)); err != nil {
		return nil, err
	}
	out := make([]AttachmentMeta, 0, len(body.Attachments))
	for name, a := range body.Attachments {
		out = append(out, AttachmentMeta{
			Name:        name,
			ContentType: a.ContentType,
			Digest:      a.Digest,
			Length:      a.Length,
			RevPos:      a.RevPos,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// GetAttachment streams an attachment. The caller must close Content.
//
// This is the one request cdb makes that does not go through doDecode: the
// response is opaque bytes of unknown size, so the body is handed back
// unread rather than decoded, and only an error response is consumed here.
func (c *Client) GetAttachment(ctx context.Context, db, docID, name, rev string) (AttachmentStream, error) {
	apiPath := attPath(db, docID, name)
	if rev != "" {
		apiPath += "?rev=" + url.QueryEscape(rev)
	}
	req, err := c.NewRequest(ctx, http.MethodGet, apiPath, nil)
	if err != nil {
		return AttachmentStream{}, Wrap(err, "read", attTarget(db, docID, name))
	}
	req.Header.Set("Accept", "*/*")
	// Ask for the raw bytes, not CouchDB's stored gzip encoding. Without this
	// net/http would offer gzip and transparently decode it, which works, but
	// setting it explicitly keeps the byte count on the wire equal to the byte
	// count the caller sees.
	req.Header.Set("Accept-Encoding", "identity")
	res, err := c.HTTP().Do(req)
	if err != nil {
		return AttachmentStream{}, Wrap(err, "read", attTarget(db, docID, name))
	}
	if res.StatusCode >= 400 {
		defer res.Body.Close()
		var e struct {
			Error  string `json:"error"`
			Reason string `json:"reason"`
		}
		_ = json.NewDecoder(res.Body).Decode(&e)
		if e.Error == "" {
			e.Error = nameForStatus(res.StatusCode)
		}
		return AttachmentStream{}, NewError(res.StatusCode, e.Error, e.Reason, "read", attTarget(db, docID, name))
	}
	return AttachmentStream{
		ContentType: res.Header.Get("Content-Type"),
		Length:      res.ContentLength,
		Content:     res.Body,
	}, nil
}

// PutAttachment uploads an attachment, streaming body straight into the request
// rather than reading it into memory. size may be -1 when the length is
// unknown, in which case the request is chunked.
func (c *Client) PutAttachment(ctx context.Context, db, docID, name, contentType string, size int64, body io.Reader, rev string) (string, error) {
	apiPath := attPath(db, docID, name)
	if rev != "" {
		apiPath += "?rev=" + url.QueryEscape(rev)
	}
	req, err := c.NewRequest(ctx, http.MethodPut, apiPath, body)
	if err != nil {
		return "", Wrap(err, "write", attTarget(db, docID, name))
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	req.Header.Set("Content-Type", contentType)
	if size >= 0 {
		req.ContentLength = size
	}
	var out writeResult
	if err := c.doDecode(req, &out, "write", attTarget(db, docID, name)); err != nil {
		return "", err
	}
	return out.Rev, nil
}

// DeleteAttachment removes an attachment and returns the document's new rev.
func (c *Client) DeleteAttachment(ctx context.Context, db, docID, name, rev string) (string, error) {
	apiPath := attPath(db, docID, name) + "?rev=" + url.QueryEscape(rev)
	req, err := c.NewRequest(ctx, http.MethodDelete, apiPath, nil)
	if err != nil {
		return "", Wrap(err, "delete", attTarget(db, docID, name))
	}
	var out writeResult
	if err := c.doDecode(req, &out, "delete", attTarget(db, docID, name)); err != nil {
		return "", err
	}
	return out.Rev, nil
}
