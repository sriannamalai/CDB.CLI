package shell

import (
	"encoding/json"
	"fmt"

	"github.com/itchyny/gojq"
)

// ApplyFilter runs a gojq expression over each input document and returns every
// value the expression produced.
func ApplyFilter(expr string, docs []json.RawMessage) ([]json.RawMessage, error) {
	f, err := compileFilter(expr)
	if err != nil {
		return nil, err
	}
	var out []json.RawMessage
	for _, doc := range docs {
		vals, err := f.apply(doc)
		if err != nil {
			return nil, err
		}
		out = append(out, vals...)
	}
	return out, nil
}

// filter is a parsed gojq expression, applied one document at a time. It is
// compiled once and reused, so a filter over a feed with no end does not
// re-parse the expression for every change that arrives.
type filter struct {
	expr string
	code *gojq.Code
}

// compileFilter parses and compiles expr, reporting either failure against the
// expression the operator typed.
func compileFilter(expr string) (*filter, error) {
	query, err := gojq.Parse(expr)
	if err != nil {
		return nil, fmt.Errorf("filter %q is not valid jq syntax: %w", expr, err)
	}
	code, err := gojq.Compile(query)
	if err != nil {
		return nil, fmt.Errorf("filter %q could not be compiled: %w", expr, err)
	}
	return &filter{expr: expr, code: code}, nil
}

// apply runs the filter over one document and returns every value it produced,
// which may be none — jq's "select" drops its input by producing nothing.
func (f *filter) apply(doc json.RawMessage) ([]json.RawMessage, error) {
	var input any
	if err := json.Unmarshal(doc, &input); err != nil {
		return nil, fmt.Errorf("filter input is not JSON: %w", err)
	}
	var out []json.RawMessage
	iter := f.code.Run(input)
	for {
		v, ok := iter.Next()
		if !ok {
			return out, nil
		}
		if e, isErr := v.(error); isErr {
			return nil, fmt.Errorf("filter %q failed: %w", f.expr, e)
		}
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
}
