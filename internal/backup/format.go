// Package backup implements the cdb dump file format: a gzip stream of
// newline-delimited JSON records, with attachment payloads written as raw bytes
// immediately after their "att" record. Every checkpoint closes the current
// gzip member, so an interrupted dump can be resumed at a member boundary.
package backup

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Record kinds.
const (
	KindHeader     = "header"
	KindDoc        = "doc"
	KindAtt        = "att"
	KindCheckpoint = "checkpoint"
	KindFooter     = "footer"
)

// FormatVersion is the dump format this cdb writes. Every 1.1 backup records
// it, whether or not --tombstones was given.
const FormatVersion = "1.1"

// Header is the first record in a dump.
type Header struct {
	Kind string `json:"kind"`
	// Version is the dump format. A dump written by cdb 1.0 has none, which
	// means "1.0"; a 1.1 dump always carries "1.1".
	Version     string `json:"version,omitempty"`
	DB          string `json:"db"`
	Server      string `json:"server"`
	Started     string `json:"started"`
	Partitioned bool   `json:"partitioned"`
	// Tombstones records whether deletions were dumped. It is what tells a
	// reader whether the absence of a tombstone means "never deleted" or
	// "deletions were not recorded".
	Tombstones bool `json:"tombstones,omitempty"`
}

// Att describes an attachment payload that follows the record.
type Att struct {
	Kind        string `json:"kind"`
	ID          string `json:"id"`
	Rev         string `json:"rev"`
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	Length      int64  `json:"length"`
}

// Footer is the last record in a complete dump.
type Footer struct {
	Kind string `json:"kind"`
	// Docs counts every doc record, tombstones included, so Docs - Deleted is
	// the number of live revisions.
	Docs int64 `json:"docs"`
	// Deleted counts the doc records carrying _deleted:true. Absent means zero.
	Deleted     int64  `json:"deleted,omitempty"`
	Attachments int64  `json:"attachments"`
	LastSeq     string `json:"last_seq"`
}

// VersionError reports a dump whose header declares a format version this cdb
// does not read. Callers match on it with errors.As to report the mistake as
// their own usage error, naming the file the operator typed.
type VersionError struct{ Version string }

func (e *VersionError) Error() string {
	return fmt.Sprintf("dump format %q is not supported", e.Version)
}

// supportedVersion reports whether a header's version can be read. An empty
// version means 1.0, the format cdb 1.0 wrote. Anything newer is refused
// rather than read on a guess: a 1.2 dump may hold record kinds this binary
// would silently skip.
func supportedVersion(v string) bool {
	switch v {
	case "", "1.0", FormatVersion:
		return true
	}
	return false
}

// IsTombstone reports whether a dump document records a deletion. A tombstone
// is an ordinary doc record carrying _deleted:true — there is no separate
// kind, so every reader that walks doc records already handles it.
func IsTombstone(doc json.RawMessage) bool {
	var d struct {
		Deleted bool `json:"_deleted"`
	}
	if err := json.Unmarshal(doc, &d); err != nil {
		return false
	}
	return d.Deleted
}

// Writer writes dump records.
//
// Any error returned by a Write method is terminal; the record stream is
// desynchronised and the Writer must not be used again. The dump remains
// resumable from its last complete checkpoint. Once a method has failed, every
// later method returns that same error and writes nothing.
type Writer struct {
	w   io.Writer
	gz  *gzip.Writer
	err error
}

// NewWriter returns a Writer appending to w.
func NewWriter(w io.Writer) *Writer {
	return &Writer{w: w, gz: gzip.NewWriter(w)}
}

// fail latches err as the Writer's terminal error and returns it.
func (w *Writer) fail(err error) error {
	if w.err == nil {
		w.err = err
	}
	return w.err
}

func (w *Writer) writeJSON(v any) error {
	if w.err != nil {
		return w.err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return w.fail(err)
	}
	if _, err := w.gz.Write(append(b, '\n')); err != nil {
		return w.fail(err)
	}
	return nil
}

// WriteHeader writes the header record.
func (w *Writer) WriteHeader(h Header) error {
	h.Kind = KindHeader
	return w.writeJSON(h)
}

// WriteDoc writes one document record.
func (w *Writer) WriteDoc(doc json.RawMessage) error {
	return w.writeJSON(struct {
		Kind string          `json:"kind"`
		Doc  json.RawMessage `json:"doc"`
	}{KindDoc, doc})
}

// WriteAttachment writes an att record followed by exactly a.Length bytes read
// from r, then a newline.
//
// A reader that ends before a.Length bytes is an error, and like any error from
// a Write method it is terminal: the att record has already been written with
// its declared length, so the stream is desynchronised and the Writer must not
// be used again. The dump remains resumable from its last complete checkpoint.
func (w *Writer) WriteAttachment(a Att, r io.Reader) error {
	if w.err != nil {
		return w.err
	}
	a.Kind = KindAtt
	if err := w.writeJSON(a); err != nil {
		return err
	}
	n, err := io.Copy(w.gz, io.LimitReader(r, a.Length))
	if err != nil {
		return w.fail(err)
	}
	if n != a.Length {
		return w.fail(fmt.Errorf("attachment %q of %q: wrote %d bytes, declared %d", a.Name, a.ID, n, a.Length))
	}
	if _, err := w.gz.Write([]byte{'\n'}); err != nil {
		return w.fail(err)
	}
	return nil
}

// WriteCheckpoint writes a checkpoint record and closes the current gzip member
// so the file can be resumed here.
func (w *Writer) WriteCheckpoint(seq string) error {
	if w.err != nil {
		return w.err
	}
	if err := w.writeJSON(struct {
		Kind string `json:"kind"`
		Seq  string `json:"seq"`
	}{KindCheckpoint, seq}); err != nil {
		return err
	}
	if err := w.gz.Close(); err != nil {
		return w.fail(err)
	}
	w.gz = gzip.NewWriter(w.w)
	return nil
}

// WriteFooter writes the footer record.
func (w *Writer) WriteFooter(f Footer) error {
	f.Kind = KindFooter
	return w.writeJSON(f)
}

// Close flushes and closes the current gzip member. It reports the Writer's
// terminal error, if any, without writing.
func (w *Writer) Close() error {
	if w.err != nil {
		return w.err
	}
	if err := w.gz.Close(); err != nil {
		return w.fail(err)
	}
	return nil
}

// Record is one decoded dump record. Content is valid only until the next call
// to Reader.Next, which drains anything left unread.
type Record struct {
	Kind       string
	Header     *Header
	Doc        json.RawMessage
	Att        *Att
	Content    io.Reader
	Checkpoint string
	Footer     *Footer
}

// Reader reads dump records.
type Reader struct {
	gz      *gzip.Reader
	br      *bufio.Reader
	pending io.Reader
}

// NewReader returns a Reader over the gzip stream r. Concatenated gzip members
// are read transparently.
func NewReader(r io.Reader) (*Reader, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, err
	}
	return &Reader{gz: gz, br: newBufReader(gz)}, nil
}

func newBufReader(r io.Reader) *bufio.Reader { return bufio.NewReaderSize(r, 64*1024) }

// Next returns the next record, or io.EOF at the end of the stream.
func (r *Reader) Next() (*Record, error) {
	if r.pending != nil {
		if _, err := io.Copy(io.Discard, r.pending); err != nil {
			return nil, err
		}
		if _, err := r.br.ReadByte(); err != nil { // the trailing newline
			return nil, err
		}
		r.pending = nil
	}
	lineBytes, err := r.br.ReadBytes('\n')
	if err != nil {
		if errors.Is(err, io.EOF) && len(lineBytes) == 0 {
			return nil, io.EOF
		}
		if !errors.Is(err, io.EOF) {
			return nil, err
		}
	}
	var probe struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(lineBytes, &probe); err != nil {
		return nil, fmt.Errorf("backup: malformed record: %w", err)
	}
	rec := &Record{Kind: probe.Kind}
	switch probe.Kind {
	case KindHeader:
		var h Header
		if err := json.Unmarshal(lineBytes, &h); err != nil {
			return nil, err
		}
		if !supportedVersion(h.Version) {
			return nil, &VersionError{Version: h.Version}
		}
		rec.Header = &h
	case KindDoc:
		var d struct {
			Doc json.RawMessage `json:"doc"`
		}
		if err := json.Unmarshal(lineBytes, &d); err != nil {
			return nil, err
		}
		rec.Doc = d.Doc
	case KindAtt:
		var a Att
		if err := json.Unmarshal(lineBytes, &a); err != nil {
			return nil, err
		}
		rec.Att = &a
		r.pending = io.LimitReader(r.br, a.Length)
		rec.Content = r.pending
	case KindCheckpoint:
		var c struct {
			Seq string `json:"seq"`
		}
		if err := json.Unmarshal(lineBytes, &c); err != nil {
			return nil, err
		}
		rec.Checkpoint = c.Seq
	case KindFooter:
		var f Footer
		if err := json.Unmarshal(lineBytes, &f); err != nil {
			return nil, err
		}
		rec.Footer = &f
	default:
		return nil, fmt.Errorf("backup: unknown record kind %q", probe.Kind)
	}
	return rec, nil
}

// Close closes the underlying gzip reader.
func (r *Reader) Close() error { return r.gz.Close() }
