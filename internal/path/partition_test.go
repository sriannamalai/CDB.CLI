package path

import "testing"

func TestResolvePartitionForms(t *testing.T) {
	for _, tc := range []struct {
		in         string
		kind       Kind
		docID      string
		view       string
		attachment string
	}{
		{"/db/_partition/p1", KindPartition, "", "", ""},
		{"/db/_partition/p1/doc1", KindDocument, "p1:doc1", "", ""},
		// A path built by pasting an id straight out of "ls" names the same
		// document as the short form.
		{"/db/_partition/p1/p1:doc1", KindDocument, "p1:doc1", "", ""},
		{"/db/_partition/p1/doc1/photo.jpg", KindAttachment, "p1:doc1", "", "photo.jpg"},
		{"/db/_partition/p1/_design/app/_view/by_date", KindView, "_design/app", "by_date", ""},
	} {
		t.Run(tc.in, func(t *testing.T) {
			got, err := Resolve("/", tc.in)
			if err != nil {
				t.Fatalf("Resolve(%q) = %v", tc.in, err)
			}
			if got.Kind != tc.kind {
				t.Errorf("Kind = %v, want %v", got.Kind, tc.kind)
			}
			if got.Database != "db" {
				t.Errorf("Database = %q, want db", got.Database)
			}
			if got.Partition != "p1" {
				t.Errorf("Partition = %q, want p1", got.Partition)
			}
			if got.DocID != tc.docID {
				t.Errorf("DocID = %q, want %q", got.DocID, tc.docID)
			}
			if got.View != tc.view {
				t.Errorf("View = %q, want %q", got.View, tc.view)
			}
			if got.Attachment != tc.attachment {
				t.Errorf("Attachment = %q, want %q", got.Attachment, tc.attachment)
			}
			// The path keeps the _partition form as typed: it is the text
			// error messages and paging hints print.
			if got.Path != tc.in {
				t.Errorf("Path = %q, want %q", got.Path, tc.in)
			}
		})
	}
}

func TestResolvePartitionRefusals(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"/db/_partition", "a partition path looks like /db/_partition/<key>"},
		{"/db/_partition/p1/_design", "a design document path needs a name, as in /db/_design/app"},
		{"/db/_partition/p1/_design/app", "a design document is not partition-scoped; use /db/_design/app, or add /_view/<name> to run the view against the partition"},
		{"/db/_partition/p1/_design/app/_view", "a design document is not partition-scoped; use /db/_design/app, or add /_view/<name> to run the view against the partition"},
		{"/db/_partition/p1/_design/app/notview/x", "expected _view after a design document name"},
		{"/db/_partition/p1/_design/app/_view/by_date/extra", "path has too many segments"},
		{"/db/_partition/p1/doc1/photo.jpg/extra", "path has too many segments"},
	} {
		t.Run(tc.in, func(t *testing.T) {
			_, err := Resolve("/", tc.in)
			if err == nil {
				t.Fatalf("Resolve(%q) = nil error", tc.in)
			}
			pe, ok := err.(*Error)
			if !ok {
				t.Fatalf("err is %T, want *path.Error", err)
			}
			if pe.Reason != tc.want {
				t.Errorf("Reason = %q, want %q", pe.Reason, tc.want)
			}
		})
	}
}

// A partition key with a percent-encoded character still round-trips, and the
// document id it builds is decoded text.
func TestResolvePartitionDecodesSegments(t *testing.T) {
	got, err := Resolve("/", "/db/_partition/a%20b/doc1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Partition != "a b" || got.DocID != "a b:doc1" {
		t.Errorf("Partition = %q, DocID = %q", got.Partition, got.DocID)
	}
}

// A run of ".." that steps over the partition separator owes the collapse to
// whatever follows it, however many links long the run is. One ".." still
// lands on the separator — "../p2" is the sibling partition — but after two or
// more the walk has left the partition, so a name is resolved against the
// database. Getting this wrong parks the shell in a partition that does not
// exist, and because partitions are implicit nothing refuses the path.
func TestResolveNamedSegmentAfterAChainOfDotDots(t *testing.T) {
	for _, tc := range []struct {
		name, base, input, want string
		kind                    Kind
		partition               string
	}{
		{"two out of a document, then a name", "/db/_partition/p1/doc1", "../../x", "/db/x", KindDocument, ""},
		{"three out of an attachment, then a name", "/db/_partition/p1/doc1/att.txt", "../../../x", "/db/x", KindDocument, ""},
		// The cases the collapse already got right, held in place.
		{"two out of a document", "/db/_partition/p1/doc1", "../..", "/db", KindDatabase, ""},
		{"one out of a partition, then a name", "/db/_partition/p1", "../p2", "/db/_partition/p2", KindPartition, "p2"},
		{"two out of a partition, then a name", "/db/_partition/p1", "../../other", "/other", KindDatabase, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Resolve(tc.base, tc.input)
			if err != nil {
				t.Fatalf("Resolve(%q, %q) = %v", tc.base, tc.input, err)
			}
			if got.Path != tc.want || got.Kind != tc.kind {
				t.Errorf("Resolve(%q, %q) = %q (%v), want %q (%v)", tc.base, tc.input, got.Path, got.Kind, tc.want, tc.kind)
			}
			if got.Partition != tc.partition {
				t.Errorf("Partition = %q, want %q", got.Partition, tc.partition)
			}
		})
	}
}

// PartitionDocID and TrimPartition are the two halves of CouchDB's
// "<key>:<id>" convention, and they are exported so that the completion code
// in internal/command builds and strips the prefix exactly the way Resolve
// does. The pair round-trips: trimming what the builder produced gives the
// short form back.
func TestPartitionDocID(t *testing.T) {
	for _, tc := range []struct{ name, key, seg, want string }{
		{"short form gains the prefix", "p1", "doc1", "p1:doc1"},
		{"a qualified id is used as it is", "p1", "p1:doc1", "p1:doc1"},
		{"another partition's prefix is not a prefix of this one", "p1", "p2:doc1", "p1:p2:doc1"},
		{"an empty segment still names the partition", "p1", "", "p1:"},
		{"a key with a colon in it", "a:b", "doc1", "a:b:doc1"},
		{"a decoded key keeps its spaces", "a b", "doc1", "a b:doc1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := PartitionDocID(tc.key, tc.seg); got != tc.want {
				t.Errorf("PartitionDocID(%q, %q) = %q, want %q", tc.key, tc.seg, got, tc.want)
			}
		})
	}
}

func TestTrimPartition(t *testing.T) {
	for _, tc := range []struct{ name, key, id, want string }{
		{"the prefix comes off", "p1", "p1:doc1", "doc1"},
		{"an id of another partition is left alone", "p1", "p2:doc1", "p2:doc1"},
		{"an unqualified id is left alone", "p1", "doc1", "doc1"},
		{"only the first prefix comes off", "p1", "p1:p1:doc1", "p1:doc1"},
		{"a key with a colon in it", "a:b", "a:b:doc1", "doc1"},
		{"the round trip", "p1", PartitionDocID("p1", "doc1"), "doc1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := TrimPartition(tc.key, tc.id); got != tc.want {
				t.Errorf("TrimPartition(%q, %q) = %q, want %q", tc.key, tc.id, got, tc.want)
			}
		})
	}
}
