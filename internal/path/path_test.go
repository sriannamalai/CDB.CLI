package path

import (
	"strings"
	"testing"
)

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

// "/mydb/_partition" names nothing — it is the separator in front of the
// partition key, not a directory — so ".." out of a partition steps over it
// and lands on the database. Textual popping alone would strand the shell on
// an unresolvable path.
func TestResolveUpOutOfAPartition(t *testing.T) {
	for _, tc := range []struct {
		name, base, input, want string
		kind                    Kind
	}{
		{"partition to database", "/mydb/_partition/p1", "..", "/mydb", KindDatabase},
		{"document to partition", "/mydb/_partition/p1/doc1", "..", "/mydb/_partition/p1", KindPartition},
		{"document to database", "/mydb/_partition/p1/doc1", "../..", "/mydb", KindDatabase},
		{"partition to server", "/mydb/_partition/p1", "../..", "/", KindServer},
		{"attachment to document", "/mydb/_partition/p1/doc1/a.png", "..", "/mydb/_partition/p1/doc1", KindDocument},
		// A ".." with a named segment after it is an ordinary step sideways,
		// so the separator stays: "../p2" is the sibling partition, not a
		// document called p2 in the database.
		{"sibling partition", "/mydb/_partition/p1", "../p2", "/mydb/_partition/p2", KindPartition},
		{"sibling partition document", "/mydb/_partition/p1", "../p2/doc1", "/mydb/_partition/p2/doc1", KindDocument},
		// A ".." typed out after the separator itself is a plain pop: nothing
		// stepped over it on the way in.
		{"explicit separator", "/", "/mydb/_partition/..", "/mydb", KindDatabase},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Resolve(tc.base, tc.input)
			if err != nil {
				t.Fatalf("Resolve(%q, %q) = %v", tc.base, tc.input, err)
			}
			if got.Path != tc.want || got.Kind != tc.kind {
				t.Errorf("Resolve(%q, %q) = %q (%v), want %q (%v)", tc.base, tc.input, got.Path, got.Kind, tc.want, tc.kind)
			}
		})
	}
	// A path typed out in full still says what is wrong with it: only ".."
	// steps over the separator.
	if _, err := Resolve("/", "/mydb/_partition"); err == nil {
		t.Error("Resolve(/mydb/_partition) = nil error, want the missing-key error")
	}
}

func TestResolveSearchPaths(t *testing.T) {
	for _, tc := range []struct {
		in        string
		db        string
		docID     string
		index     string
		backend   Backend
		partition string
	}{
		{"/movies/_design/app/_search/by_title", "movies", "_design/app", "by_title", BackendClouseau, ""},
		{"/movies/_design/app/_nouveau/by_title", "movies", "_design/app", "by_title", BackendNouveau, ""},
		{"/movies/_partition/p1/_design/app/_search/by_title", "movies", "_design/app", "by_title", BackendClouseau, "p1"},
		{"/movies/_partition/p1/_design/app/_nouveau/by_title", "movies", "_design/app", "by_title", BackendNouveau, "p1"},
		// Percent-encoded names survive, the way view names do.
		{"/movies/_design/app/_search/by%20title", "movies", "_design/app", "by title", BackendClouseau, ""},
	} {
		got, err := Resolve("/", tc.in)
		if err != nil {
			t.Errorf("Resolve(%q) = %v", tc.in, err)
			continue
		}
		if got.Kind != KindSearch {
			t.Errorf("Resolve(%q).Kind = %v, want KindSearch", tc.in, got.Kind)
		}
		if got.Database != tc.db || got.DocID != tc.docID || got.Index != tc.index ||
			got.Backend != tc.backend || got.Partition != tc.partition {
			t.Errorf("Resolve(%q) = %#v", tc.in, got)
		}
		if got.View != "" {
			t.Errorf("Resolve(%q).View = %q, want empty: a search target is not a view", tc.in, got.View)
		}
	}
}

func TestKindSearchNames(t *testing.T) {
	if got := KindSearch.String(); got != "search index" {
		t.Errorf("KindSearch.String() = %q", got)
	}
	if got := KindSearch.Article(); got != "a" {
		t.Errorf("KindSearch.Article() = %q", got)
	}
}

// The fourth segment is now one of three words, and the message has to name
// all three: an operator who typed "_serach" is told what was expected.
func TestResolveRejectsAnUnknownDesignDocSegment(t *testing.T) {
	for _, in := range []string{"/movies/_design/app/_serach/x", "/movies/_partition/p1/_design/app/_serach/x"} {
		_, err := Resolve("/", in)
		if err == nil {
			t.Fatalf("Resolve(%q) succeeded", in)
		}
		for _, want := range []string{"_view", "_search", "_nouveau"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("Resolve(%q) error %q is missing %q", in, err, want)
			}
		}
	}
}

// ".." out of a search index behaves exactly as it does out of a view: one
// step lands on the separator segment, two on the design document. Pinning it
// keeps the two path families from drifting apart.
func TestParentOfASearchPathMatchesAView(t *testing.T) {
	for _, sep := range []string{"_view", "_search", "_nouveau"} {
		p := "/movies/_design/app/" + sep + "/x"
		if got := Parent(p); got != "/movies/_design/app/"+sep {
			t.Errorf("Parent(%q) = %q", p, got)
		}
		up, err := Clean(p, "../..")
		if err != nil {
			t.Fatal(err)
		}
		if up != "/movies/_design/app" {
			t.Errorf("Clean(%q, \"../..\") = %q, want the design document", p, up)
		}
		t2, err := Resolve("/", up)
		if err != nil || t2.Kind != KindDesignDoc {
			t.Errorf("%q resolved to %v, %v; want a design document", up, t2.Kind, err)
		}
	}
}
