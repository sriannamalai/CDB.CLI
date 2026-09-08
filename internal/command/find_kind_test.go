package command

import (
	"errors"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
)

// splitFindArgs resolves its path argument and then uses only Target.Database,
// so "find /mydb/doc1 …" silently queried the whole database.
func TestFindRefusesANonDatabasePath(t *testing.T) {
	for _, tc := range []struct{ name, arg, want string }{
		{
			"document",
			"/mydb/doc1",
			`/mydb/doc1 is a document; find queries a database or a partition. Use "cat /mydb/doc1" to read one document.`,
		},
		// Only the document case gets the "cat" suggestion: spec section 10.2
		// specifies that sentence for a document, and reading a view with
		// "cat" is neither what cat does nor one document.
		{
			"attachment",
			"/mydb/doc1/photo.jpg",
			`/mydb/doc1/photo.jpg is an attachment; find queries a database or a partition.`,
		},
		{
			"view",
			"/mydb/_design/app/_view/by_date",
			`/mydb/_design/app/_view/by_date is a view; find queries a database or a partition.`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := couchtest.New(t)
			s := connected(t, srv)
			_, err := invoke(t, Find(), s, tc.arg, `{"a":1}`)
			var ue *UsageError
			if err == nil || !errors.As(err, &ue) {
				t.Fatalf("err = %v, want a UsageError", err)
			}
			if !strings.Contains(ue.Error(), tc.want) {
				t.Errorf("message = %q, want it to contain %q", ue.Error(), tc.want)
			}
			if tc.name != "document" && strings.Contains(ue.Error(), "read one document") {
				t.Errorf("message = %q, want no cat suggestion for %s", ue.Error(), tc.name)
			}
			if srv.Last("POST", "/mydb/_find") != nil {
				t.Error("find queried the database anyway")
			}
		})
	}
}

// The server root keeps its own sentence: there is no database to name.
func TestFindAtTheRootKeepsItsSentence(t *testing.T) {
	srv := couchtest.New(t)
	s := connected(t, srv)
	_, err := invoke(t, Find(), s, "/", `{"a":1}`)
	if err == nil || !strings.Contains(err.Error(), "find needs a database; cd into one or pass a path") {
		t.Fatalf("err = %v", err)
	}
}

func TestFindStillAcceptsADatabaseAndAPartition(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/mydb/_find", 200, `{"docs":[],"bookmark":"bk"}`)
	srv.JSON("POST", "/mydb/_partition/p1/_find", 200, `{"docs":[],"bookmark":"bk"}`)
	s := connected(t, srv)
	for _, arg := range []string{"/mydb", "/mydb/_partition/p1"} {
		if _, err := invoke(t, Find(), s, arg, `{"a":1}`); err != nil {
			t.Errorf("find %s = %v", arg, err)
		}
	}
}
