package command

import "testing"

func TestSupportedVersion(t *testing.T) {
	// cdb 1.2 supports every 3.x release CouchDB has shipped: 3.0 through 3.5.
	cases := []struct {
		version string
		want    bool
	}{
		{"2.3.1", false},
		{"3.0.0", true},
		{"3.0.1", true},
		{"3.1.2", true},
		{"3.5.2", true},
		{"3.6.0", false},
		{"4.0.0", false},
		{"", false},
		{"3", false},
		{"3.x.1", false},
	}
	for _, tc := range cases {
		t.Run(tc.version, func(t *testing.T) {
			if got := supportedVersion(tc.version); got != tc.want {
				t.Errorf("supportedVersion(%q) = %v, want %v", tc.version, got, tc.want)
			}
		})
	}
}

func TestJWTVersionHint(t *testing.T) {
	cases := []struct {
		version string
		want    string
	}{
		{"3.0.0", "JWT authentication needs CouchDB 3.1 or later; this server is 3.0.0."},
		{"3.0.1", "JWT authentication needs CouchDB 3.1 or later; this server is 3.0.1."},
		{"3.1.2", ""},
		{"3.5.2", ""},
		{"", ""},
	}
	for _, tc := range cases {
		t.Run(tc.version, func(t *testing.T) {
			if got := jwtVersionHint(tc.version); got != tc.want {
				t.Errorf("jwtVersionHint(%q) = %q, want %q", tc.version, got, tc.want)
			}
		})
	}
}
