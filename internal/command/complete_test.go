package command

import (
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
)

// --fields and --sort are StringSlice flags, so "title,ye" arrives as one
// word. Everything up to the last comma is settled and has to come back on the
// candidate, or accepting one would throw the earlier fields away.
func TestCompleteFieldsSplitsAtTheLastComma(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/mydb/_find", 200, `{"docs":[
		{"_id":"a","title":"x","year":1994}],"bookmark":"bk"}`)
	s := connected(t, srv)
	s.SetPath("/mydb")

	got := CompleteFields(t.Context(), s, []string{"--fields"}, "title,ye")
	if len(got) != 1 {
		t.Fatalf("candidates = %+v", got)
	}
	if got[0].Value != "title,year" {
		t.Errorf("value = %q, want title,year", got[0].Value)
	}
	if got[0].Display != "year" {
		t.Errorf("display = %q, want year", got[0].Display)
	}
}
