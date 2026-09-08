package couch

import "testing"

func TestTrimPrefixes(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Not Found: missing", "missing"},
		{"Not Found: Database does not exist.", "Database does not exist."},
		{"Conflict: Document update conflict.", "Document update conflict."},
		{"Not Found", "not found"},
		{`Get "http://localhost:5984/_all_dbs": Unauthorized: Name or password is incorrect.`, "Name or password is incorrect."},
		{`Get "http://localhost:1/_all_dbs": dial tcp [::1]:1: connect: connection refused`, "dial tcp [::1]:1: connect: connection refused"},
	} {
		if got := trimPrefixes(tc.in); got != tc.want {
			t.Errorf("trimPrefixes(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
