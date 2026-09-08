package path

import "testing"

// Every "%s is a %s" message renders a Kind, and one of the kinds starts with
// a vowel, so the sentence read "…/movies/by_year is a attachment".
func TestKindArticle(t *testing.T) {
	for _, tc := range []struct {
		kind Kind
		want string
	}{
		{KindServer, "a"},
		{KindDatabase, "a"},
		{KindDocument, "a"},
		{KindDesignDoc, "a"},
		{KindView, "a"},
		{KindAttachment, "an"},
		{KindPartition, "a"},
	} {
		if got := tc.kind.Article(); got != tc.want {
			t.Errorf("%s: article = %q, want %q", tc.kind, got, tc.want)
		}
	}
}
