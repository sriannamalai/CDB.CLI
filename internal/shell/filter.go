package shell

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/itchyny/gojq"
)

// ApplyFilter runs a gojq expression over each input document and returns every
// value the expression produced. It binds no variables; the pipeline executor
// is what passes bindings in.
func ApplyFilter(expr string, docs []json.RawMessage) ([]json.RawMessage, error) {
	f, err := compileFilter(expr, nil)
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
	// values are the variable values, in the same order as the names the code
	// was compiled with. gojq wants them positionally on every Run.
	values []any
}

// compileFilter parses and compiles expr, reporting either failure against the
// expression the operator typed. Each entry of vars becomes a jq variable of
// the same name: "year" is readable as "$year". A nil map binds nothing.
func compileFilter(expr string, vars map[string]any) (*filter, error) {
	query, err := gojq.Parse(expr)
	if err != nil {
		return nil, fmt.Errorf("filter %q is not valid jq syntax: %w", expr, err)
	}
	names := make([]string, 0, len(vars))
	for name := range vars {
		names = append(names, name)
	}
	sort.Strings(names)
	dollar := make([]string, len(names))
	values := make([]any, len(names))
	for i, name := range names {
		dollar[i] = "$" + name
		values[i] = vars[name]
	}
	code, err := gojq.Compile(query, gojq.WithVariables(dollar))
	if err != nil {
		return nil, fmt.Errorf("filter %q could not be compiled: %w", expr, err)
	}
	return &filter{expr: expr, code: code, values: values}, nil
}

// apply runs the filter over one document and returns every value it produced,
// which may be none — jq's "select" drops its input by producing nothing.
func (f *filter) apply(doc json.RawMessage) ([]json.RawMessage, error) {
	var input any
	if err := json.Unmarshal(doc, &input); err != nil {
		return nil, fmt.Errorf("filter input is not JSON: %w", err)
	}
	var out []json.RawMessage
	iter := f.code.Run(input, f.values...)
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
