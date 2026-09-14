package command

import (
	"context"
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

func TestCompleteOffersSearchSegmentsUnderADesignDoc(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/movies/_design/app", 200,
		`{"_id":"_design/app","views":{"by_year":{}},"indexes":{"by_title":{}},"nouveau":{"by_body":{}}}`)
	s := connected(t, srv)
	got := map[string]bool{}
	for _, c := range CompletePath(context.Background(), s, nil, "/movies/_design/app/_") {
		got[c.Display] = true
	}
	for _, want := range []string{"_view", "_search", "_nouveau"} {
		if !got[want] {
			t.Errorf("completion does not offer %q; got %v", want, got)
		}
	}
}

// The tag is the group heading the shell prints above the candidates, so a
// search separator filed under "views" reads as a lie.
func TestCompleteTagsTheSearchSegments(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/movies/_design/app", 200,
		`{"_id":"_design/app","views":{"by_year":{}},"indexes":{"by_title":{}},"nouveau":{"by_body":{}}}`)
	s := connected(t, srv)
	want := map[string]string{"_view": "views", "_search": "search", "_nouveau": "search"}
	for _, c := range CompletePath(context.Background(), s, nil, "/movies/_design/app/_") {
		if w, ok := want[c.Display]; ok && c.Tag != w {
			t.Errorf("%s is tagged %q, want %q", c.Display, c.Tag, w)
		}
	}
}

func TestCompleteListsSearchIndexNames(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/movies/_design/app", 200,
		`{"_id":"_design/app","views":{"by_year":{}},"indexes":{"by_title":{},"by_actor":{}},"nouveau":{"by_body":{}}}`)
	s := connected(t, srv)

	clouseau := CompletePath(context.Background(), s, nil, "/movies/_design/app/_search/by_")
	var names []string
	for _, c := range clouseau {
		names = append(names, c.Display)
	}
	if len(names) != 2 || names[0] != "by_actor" || names[1] != "by_title" {
		t.Errorf("clouseau candidates = %v, want the two sorted index names", names)
	}
	if clouseau[0].Value != "/movies/_design/app/_search/by_actor" {
		t.Errorf("candidate value = %q", clouseau[0].Value)
	}

	nouveau := CompletePath(context.Background(), s, nil, "/movies/_design/app/_nouveau/")
	if len(nouveau) != 1 || nouveau[0].Display != "by_body" {
		t.Errorf("nouveau candidates = %#v, want only by_body", nouveau)
	}
}
