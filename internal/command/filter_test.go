package command

import (
	"errors"
	"strings"
	"testing"
)

// tail and replicate hand the same string to two different CouchDB endpoints,
// and both endpoints answer a filter that is not "<design>/<name>" with a 400.
// One helper, one sentence, so a mistyped flag is a usage error either way.
func TestCheckFilter(t *testing.T) {
	for _, tc := range []struct {
		name   string
		filter string
		ok     bool
	}{
		{"empty is not a filter at all", "", true},
		{"both halves", "app/by_type", true},
		{"a dotted design document name", "app.v2/by_type", true},
		{"no slash", "byname", false},
		{"no name", "app/", false},
		{"no design document", "/by_type", false},
		{"nothing but a slash", "/", false},
		{"too many halves", "app/by_type/extra", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkFilter("tail", tc.filter)
			if tc.ok {
				if err != nil {
					t.Fatalf("checkFilter(%q) = %v, want nil", tc.filter, err)
				}
				return
			}
			var ue *UsageError
			if !errors.As(err, &ue) {
				t.Fatalf("checkFilter(%q) = %v, want a UsageError", tc.filter, err)
			}
			if ue.Command != "tail" {
				t.Errorf("Command = %q, want the calling command", ue.Command)
			}
			if want := `--filter takes a design document and a filter name, as "app/by_type".`; !strings.Contains(ue.Error(), want) {
				t.Errorf("message = %q, want it to contain %q", ue.Error(), want)
			}
		})
	}
}
