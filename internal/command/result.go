package command

import (
	"encoding/json"
	"io"
)

// Result is a renderable command outcome. Commands never print; they return a
// Result and the front-end renders it.
type Result interface {
	// ResultKind names the variant, for renderers that switch on it.
	ResultKind() string
}

// Empty is a successful command with nothing to show.
type Empty struct{}

// Message is a single line of plain prose.
type Message struct{ Text string }

// Document is one JSON document.
type Document struct{ JSON json.RawMessage }

// Align is a column alignment.
type Align int

const (
	AlignLeft Align = iota
	AlignRight
)

// Column is table column metadata.
type Column struct {
	Title string
	Align Align
}

// Row is one rendered row. Cells feeds the table renderer; JSON feeds the JSON
// and raw renderers and the gojq filter.
type Row struct {
	Cells []string
	JSON  json.RawMessage
}

// Rows is a materialised result set.
type Rows struct {
	Columns []Column
	Items   []Row
	// Hint is a trailing line such as a paging suggestion.
	Hint string
}

// Stream is a lazily produced result set. Next returns the next row; ok is
// false at the end.
type Stream struct {
	Columns []Column
	Next    func() (row Row, ok bool, err error)
}

// Raw is an opaque byte stream, such as an attachment.
type Raw struct {
	Reader      io.Reader
	ContentType string
	Name        string
}

func (Empty) ResultKind() string    { return "empty" }
func (Message) ResultKind() string  { return "message" }
func (Document) ResultKind() string { return "document" }
func (Rows) ResultKind() string     { return "rows" }
func (Stream) ResultKind() string   { return "stream" }
func (Raw) ResultKind() string      { return "raw" }

// jsonObject builds a Row.JSON payload from alternating string keys and values.
// Every command that builds Rows out of plain strings uses it, so that the
// --json output has lower-case keys throughout.
func jsonObject(kv ...string) json.RawMessage {
	m := make(map[string]string, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	b, err := json.Marshal(m)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}
