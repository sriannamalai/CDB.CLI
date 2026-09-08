package shell

import (
	"encoding/json"
	"fmt"

	"github.com/itchyny/gojq"
)

// ApplyFilter runs a gojq expression over each input document and returns every
// value the expression produced.
func ApplyFilter(expr string, docs []json.RawMessage) ([]json.RawMessage, error) {
	query, err := gojq.Parse(expr)
	if err != nil {
		return nil, fmt.Errorf("filter %q is not valid jq syntax: %w", expr, err)
	}
	code, err := gojq.Compile(query)
	if err != nil {
		return nil, fmt.Errorf("filter %q could not be compiled: %w", expr, err)
	}
	var out []json.RawMessage
	for _, doc := range docs {
		var input any
		if err := json.Unmarshal(doc, &input); err != nil {
			return nil, fmt.Errorf("filter input is not JSON: %w", err)
		}
		iter := code.Run(input)
		for {
			v, ok := iter.Next()
			if !ok {
				break
			}
			if e, isErr := v.(error); isErr {
				return nil, fmt.Errorf("filter %q failed: %w", expr, e)
			}
			b, err := json.Marshal(v)
			if err != nil {
				return nil, err
			}
			out = append(out, b)
		}
	}
	return out, nil
}
