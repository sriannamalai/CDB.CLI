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
