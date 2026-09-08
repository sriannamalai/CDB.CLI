// Package path implements the virtual filesystem grammar cdb presents over a
// CouchDB server. It is the only place path parsing lives.
package path

import (
	"fmt"
	"net/url"
	"strings"
)

// Kind classifies what a resolved path points at.
type Kind int

const (
	KindServer Kind = iota
	KindDatabase
	KindDocument
	KindDesignDoc
	KindView
	KindAttachment
	KindPartition
)

func (k Kind) String() string {
	switch k {
	case KindServer:
		return "server"
	case KindDatabase:
		return "database"
	case KindDocument:
		return "document"
	case KindDesignDoc:
		return "design document"
	case KindView:
		return "view"
	case KindAttachment:
		return "attachment"
	case KindPartition:
		return "partition"
	}
	return "unknown"
}

// Article returns the indefinite article for the kind's name, so the messages
// that say "%s is a %s" do not read "is a attachment". It is a method rather
// than a helper in one command package because every package that renders a
// kind needs it.
func (k Kind) Article() string {
	if name := k.String(); name != "" && strings.ContainsRune("aeiou", rune(name[0])) {
		return "an"
	}
	return "a"
}

// Target is a resolved virtual path.
type Target struct {
	Kind Kind
	// Path is the canonical, absolute, still-encoded path.
	Path string
	// Database is the decoded database name. Empty at the server root.
	Database string
	// DocID is the decoded document id, including a "_design/" prefix for
	// design documents.
	DocID string
	// View is the view name. Set only when Kind is KindView.
	View string
	// Attachment is the attachment file name. Set only when Kind is
	// KindAttachment.
	Attachment string
	// Partition is the partition key. Set on a KindPartition target and on
	// every document, attachment and view addressed through one.
	Partition string
}

// Error describes a path that could not be resolved.
type Error struct {
	Input  string
	Reason string
}

func (e *Error) Error() string {
	return fmt.Sprintf("invalid path %q: %s", e.Input, e.Reason)
}

// Clean resolves input against base and returns a canonical absolute path.
// It understands "." and ".." and leaves each segment percent-encoded.
func Clean(base, input string) (string, error) {
	if base == "" {
		base = "/"
	}
	if !strings.HasPrefix(base, "/") {
		return "", &Error{Input: base, Reason: "base path must be absolute"}
	}
	var segs []string
	if !strings.HasPrefix(input, "/") {
		segs = split(base)
	}
	for _, s := range split(input) {
		switch s {
		case ".":
		case "..":
			if len(segs) > 0 {
				segs = segs[:len(segs)-1]
			}
		default:
			segs = append(segs, s)
		}
	}
	return "/" + strings.Join(segs, "/"), nil
}

func split(p string) []string {
	var out []string
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// Parent returns the canonical parent of an absolute path.
func Parent(p string) string {
	segs := split(p)
	if len(segs) == 0 {
		return "/"
	}
	return "/" + strings.Join(segs[:len(segs)-1], "/")
}

// Encode escapes a name so it survives as one path segment. CouchDB permits
// "/" in database names, so it must be percent-encoded.
func Encode(name string) string { return url.PathEscape(name) }

// Decode reverses Encode.
func Decode(seg string) (string, error) {
	s, err := url.PathUnescape(seg)
	if err != nil {
		return "", &Error{Input: seg, Reason: "bad percent-encoding"}
	}
	return s, nil
}

// Resolve resolves input against base and classifies the result.
func Resolve(base, input string) (Target, error) {
	clean, err := Clean(base, input)
	if err != nil {
		return Target{}, err
	}
	segs := split(clean)
	dec := make([]string, len(segs))
	for i, s := range segs {
		d, err := Decode(s)
		if err != nil {
			return Target{}, &Error{Input: input, Reason: "bad percent-encoding in " + s}
		}
		dec[i] = d
	}
	t := Target{Path: clean}
	switch {
	case len(segs) == 0:
		t.Kind = KindServer
		return t, nil
	case len(segs) == 1:
		t.Kind = KindDatabase
		t.Database = dec[0]
		return t, nil
	}
	t.Database = dec[0]

	if dec[1] == "_partition" {
		if len(segs) < 3 {
			return Target{}, &Error{Input: input, Reason: "a partition path looks like /db/_partition/<key>"}
		}
		t.Partition = dec[2]
		// After the key, the remaining segments follow the same rules as
		// after /db, with two differences: a document id gets the partition
		// prefix, and a design document alone is refused.
		rest := dec[3:]
		if len(rest) == 0 {
			t.Kind = KindPartition
			return t, nil
		}
		if rest[0] == "_design" {
			if len(rest) < 2 {
				return Target{}, &Error{Input: input, Reason: "a design document path needs a name, as in /db/_design/app"}
			}
			t.DocID = "_design/" + rest[1]
			switch {
			case len(rest) == 4 && rest[2] == "_view":
				t.Kind = KindView
				t.View = rest[3]
				return t, nil
			case len(rest) == 4:
				return Target{}, &Error{Input: input, Reason: "expected _view after a design document name"}
			case len(rest) > 4:
				return Target{}, &Error{Input: input, Reason: "path has too many segments"}
			default:
				// The design document itself, or the document plus one
				// segment that is not a complete view path. A design document
				// is not partition-scoped — CouchDB answers 404 for
				// /db/_partition/p1/_design/app — so say where it does live
				// and how to run its view against this partition.
				return Target{}, &Error{Input: input, Reason: fmt.Sprintf(
					"a design document is not partition-scoped; use /%s/_design/%s, or add /_view/<name> to run the view against the partition",
					segs[0], segs[4])}
			}
		}
		// CouchDB's own convention: a document in partition p1 has an id of
		// the form "p1:<rest>". There is no partition-scoped document
		// endpoint, so the partition lives in the id and the request base
		// does not carry it.
		t.DocID = partitionDocID(t.Partition, rest[0])
		switch len(rest) {
		case 1:
			t.Kind = KindDocument
			return t, nil
		case 2:
			t.Kind = KindAttachment
			t.Attachment = rest[1]
			return t, nil
		default:
			return Target{}, &Error{Input: input, Reason: "path has too many segments"}
		}
	}

	if dec[1] == "_design" {
		if len(segs) < 3 {
			return Target{}, &Error{Input: input, Reason: "a design document path needs a name, as in /db/_design/app"}
		}
		t.DocID = "_design/" + dec[2]
		switch len(segs) {
		case 3:
			t.Kind = KindDesignDoc
			return t, nil
		case 4:
			t.Kind = KindAttachment
			t.Attachment = dec[3]
			return t, nil
		case 5:
			if dec[3] != "_view" {
				return Target{}, &Error{Input: input, Reason: "expected _view after a design document name"}
			}
			t.Kind = KindView
			t.View = dec[4]
			return t, nil
		default:
			return Target{}, &Error{Input: input, Reason: "path has too many segments"}
		}
	}

	switch len(segs) {
	case 2:
		t.Kind = KindDocument
		t.DocID = dec[1]
		return t, nil
	case 3:
		t.Kind = KindAttachment
		t.DocID = dec[1]
		t.Attachment = dec[2]
		return t, nil
	default:
		return Target{}, &Error{Input: input, Reason: "path has too many segments"}
	}
}

// partitionDocID applies CouchDB's partitioned-document convention. A segment
// that already begins with "<key>:" is used as it is, so that
// /db/_partition/p1/doc1 and /db/_partition/p1/p1:doc1 name the same document
// and a path built by pasting an id straight out of "ls" works.
func partitionDocID(key, seg string) string {
	if strings.HasPrefix(seg, key+":") {
		return seg
	}
	return key + ":" + seg
}
