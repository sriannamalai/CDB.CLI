package command

import (
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
)

// Issue #53.6: "ls | put /dst" used to write the listing rows themselves as
// documents — {"id":…,"rev":…} objects, junk in the target. A value that is
// exactly a listing row is refused, with the stage that turns it into a
// document named.
func TestPutPipelineRefusesAListingRow(t *testing.T) {
	srv := couchtest.New(t)
	s := connected(t, srv)
	s.SetPath("/movies")
	_, err := piped(t, Put(), s, nil, `{"id":"a","rev":"1-a"}`)
	if err == nil {
		t.Fatal("a listing row was written")
	}
	want := "value 1 is a listing row, not a document; pipe it through cat first"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error = %v, want it to say %q", err, want)
	}
}

// A document that carries an "id" and a "rev" among other fields is a
// document, not a listing row, and is written.
func TestPutPipelineWritesADocumentThatHasIDAndRevFields(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/movies/_all_docs", 200, `{"rows":[]}`)
	srv.JSON("POST", "/movies/_bulk_docs", 201, `[{"ok":true,"id":"a","rev":"1-z"}]`)
	s := connected(t, srv)
	s.SetPath("/movies")
	rows, err := piped(t, Put(), s, nil, `{"id":"a","rev":"1-a","title":"Amelie"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0][2] != "ok" {
		t.Errorf("rows = %v", rows)
	}
}
