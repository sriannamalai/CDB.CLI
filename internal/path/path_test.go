package path

import "testing"

func TestResolve(t *testing.T) {
	tests := []struct {
		name  string
		base  string
		input string
		want  Target
	}{
		{"server root", "/", "/", Target{Kind: KindServer, Path: "/"}},
		{"database", "/", "/mydb", Target{Kind: KindDatabase, Path: "/mydb", Database: "mydb"}},
		{"document", "/", "/mydb/doc1", Target{Kind: KindDocument, Path: "/mydb/doc1", Database: "mydb", DocID: "doc1"}},
		{"relative document", "/mydb", "doc1", Target{Kind: KindDocument, Path: "/mydb/doc1", Database: "mydb", DocID: "doc1"}},
		{"parent", "/mydb/doc1", "..", Target{Kind: KindDatabase, Path: "/mydb", Database: "mydb"}},
		{"dot", "/mydb", ".", Target{Kind: KindDatabase, Path: "/mydb", Database: "mydb"}},
		{"design doc", "/", "/mydb/_design/app", Target{Kind: KindDesignDoc, Path: "/mydb/_design/app", Database: "mydb", DocID: "_design/app"}},
		{"view", "/", "/mydb/_design/app/_view/by_date", Target{Kind: KindView, Path: "/mydb/_design/app/_view/by_date", Database: "mydb", DocID: "_design/app", View: "by_date"}},
		{"attachment", "/", "/mydb/doc1/photo.jpg", Target{Kind: KindAttachment, Path: "/mydb/doc1/photo.jpg", Database: "mydb", DocID: "doc1", Attachment: "photo.jpg"}},
		{"ddoc attachment", "/", "/mydb/_design/app/logo.png", Target{Kind: KindAttachment, Path: "/mydb/_design/app/logo.png", Database: "mydb", DocID: "_design/app", Attachment: "logo.png"}},
		{"partition", "/", "/mydb/_partition/p1", Target{Kind: KindPartition, Path: "/mydb/_partition/p1", Database: "mydb", Partition: "p1"}},
		{"encoded db name", "/", "/a%2Fb", Target{Kind: KindDatabase, Path: "/a%2Fb", Database: "a/b"}},
		{"encoded db doc", "/", "/a%2Fb/doc1", Target{Kind: KindDocument, Path: "/a%2Fb/doc1", Database: "a/b", DocID: "doc1"}},
		{"trailing slash", "/", "/mydb/", Target{Kind: KindDatabase, Path: "/mydb", Database: "mydb"}},
		{"climb above root", "/mydb", "../../..", Target{Kind: KindServer, Path: "/"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Resolve(tc.base, tc.input)
			if err != nil {
				t.Fatalf("Resolve(%q, %q) returned error: %v", tc.base, tc.input, err)
			}
			if got != tc.want {
				t.Errorf("Resolve(%q, %q) = %+v, want %+v", tc.base, tc.input, got, tc.want)
			}
		})
	}
}

func TestResolveErrors(t *testing.T) {
	tests := []struct{ name, base, input string }{
		{"partition without key", "/", "/mydb/_partition"},
		{"design without name", "/", "/mydb/_design"},
		{"view without _view", "/", "/mydb/_design/app/nope/by_date"},
		{"too deep", "/", "/mydb/doc1/photo.jpg/extra"},
		{"bad escape", "/", "/my%zzdb"},
		{"relative base", "mydb", "doc1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Resolve(tc.base, tc.input); err == nil {
				t.Fatalf("Resolve(%q, %q) = nil error, want error", tc.base, tc.input)
			}
		})
	}
}

func TestParent(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"/", "/"},
		{"/mydb", "/"},
		{"/mydb/doc1", "/mydb"},
		{"/mydb/_design/app/_view/v", "/mydb/_design/app/_view"},
	} {
		if got := Parent(tc.in); got != tc.want {
			t.Errorf("Parent(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestEncodeDecode(t *testing.T) {
	enc := Encode("a/b")
	if enc != "a%2Fb" {
		t.Fatalf("Encode(%q) = %q, want %q", "a/b", enc, "a%2Fb")
	}
	dec, err := Decode(enc)
	if err != nil || dec != "a/b" {
		t.Fatalf("Decode(%q) = %q, %v; want %q, nil", enc, dec, err, "a/b")
	}
}
