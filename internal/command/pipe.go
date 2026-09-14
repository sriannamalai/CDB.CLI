package command

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// PipeKind says what a command reads from a pipeline when it is not the first
// stage of a line. A command that leaves it at PipeNone cannot be piped into
// at all, and the shell refuses the line rather than silently ignoring the
// values upstream produced.
type PipeKind int

const (
	// PipeNone is a command that does not read a pipeline.
	PipeNone PipeKind = iota
	// PipeDocuments is a command whose input values are whole JSON documents.
	PipeDocuments
	// PipeReferences is a command whose input values name documents: an id, an
	// object carrying one, or an absolute "/db/id" path.
	PipeReferences
)

// String is the word help and the reference pages print after "Reads a
// pipeline: ". PipeNone has no word, because it prints no line.
func (k PipeKind) String() string {
	switch k {
	case PipeDocuments:
		return "documents"
	case PipeReferences:
		return "references"
	default:
		return ""
	}
}

// Pipe is the previous stage's output, read one value at a time. It is backed
// by the channel the shell's executor joins two stages with; the channel type
// is in the signature rather than a producer function because a consumer that
// batches needs to ask whether a value is waiting, which only a channel can
// answer without blocking.
type Pipe struct {
	src <-chan json.RawMessage
}

// NewPipe reads from src. A closed channel is a finished pipeline.
func NewPipe(src <-chan json.RawMessage) *Pipe { return &Pipe{src: src} }

// Next reads the next value, waiting for it. ok is false when the stage above
// has finished; an error is the line's cancellation.
func (p *Pipe) Next(ctx context.Context) (json.RawMessage, bool, error) {
	if p == nil || p.src == nil {
		return nil, false, nil
	}
	select {
	case v, ok := <-p.src:
		return v, ok, nil
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
}

// TryNext reads a value only if one is waiting. waiting is false when nothing
// is queued right now; it is true both for a value and for a finished
// pipeline, so a caller batching for a bulk request can send a short batch
// rather than hold a live feed's change back until ninety-nine more arrive.
func (p *Pipe) TryNext() (v json.RawMessage, ok, waiting bool) {
	if p == nil || p.src == nil {
		return nil, false, false
	}
	select {
	case v, ok := <-p.src:
		return v, ok, true
	default:
		return nil, false, false
	}
}

// ErrNotReference is what ParseReference returns for a value that names no
// document. The stage wraps it in a sentence naming the value's position.
var ErrNotReference = errors.New("not a document reference")

// ParseReference reads one value of a PipeReferences stage. db is empty for a
// reference that names no database, and the stage supplies its own; it is set
// only for the absolute "/db/id" form, which overrides the stage's database.
func ParseReference(v json.RawMessage) (db, id string, err error) {
	var s string
	if json.Unmarshal(v, &s) == nil {
		return splitReference(s)
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(v, &obj) != nil {
		return "", "", ErrNotReference
	}
	for _, key := range []string{"_id", "id"} {
		raw, ok := obj[key]
		if !ok {
			continue
		}
		if json.Unmarshal(raw, &s) != nil {
			return "", "", ErrNotReference
		}
		return splitReference(s)
	}
	return "", "", ErrNotReference
}

// splitReference separates "/db/id" from a bare id. A "/db" with no document
// after it names a database, not a document, and is refused.
func splitReference(s string) (db, id string, err error) {
	if s == "" {
		return "", "", ErrNotReference
	}
	if !strings.HasPrefix(s, "/") {
		return "", s, nil
	}
	rest := strings.TrimPrefix(s, "/")
	slash := strings.Index(rest, "/")
	if slash <= 0 || slash == len(rest)-1 {
		return "", "", ErrNotReference
	}
	return rest[:slash], rest[slash+1:], nil
}

// NoPipeError is the usage error for a command reached in a later stage that
// reads no pipeline. The sentence carries the command name itself, so the
// UsageError is built with an empty Command: "put: put does not read a
// pipeline." would name it twice.
func NoPipeError(name string) *UsageError {
	return &UsageError{Reason: name + " does not read a pipeline."}
}
