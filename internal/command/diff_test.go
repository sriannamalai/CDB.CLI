package command

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
)

func TestDiffRevisions(t *testing.T) {
	for _, tc := range []struct {
		name    string
		winner  string
		other   string
		summary string
	}{
		{
			name:    "no differences",
			winner:  `{"_id":"m","_rev":"3-a","title":"x"}`,
			other:   `{"_id":"m","_rev":"3-a","title":"x"}`,
			summary: "no differences from the current revision",
		},
		{
			name:    "changed, added and removed together",
			winner:  `{"_id":"m","_rev":"3-a","title":"The Shawshank Redemption is a nineteen ninety-four film","year":1994,"draft":true}`,
			other:   `{"_id":"m","_rev":"3-b","title":"Shawshank Redemption","year":1995,"rating":9.3}`,
			summary: `changed: title "The Shawshank Redemption is a nineteen… → "Shawshank Redemption"; changed: year 1994 → 1995; added: rating 9.3; removed: draft`,
		},
		{
			name:    "key order and whitespace do not register",
			winner:  `{"a": 1,   "b": {"x":1,"y":2}}`,
			other:   `{"b":{"y":2,"x":1},"a":1}`,
			summary: "no differences from the current revision",
		},
		{
			name:    "underscore fields are ignored except _deleted",
			winner:  `{"_id":"m","_rev":"3-a","_conflicts":["3-b"],"a":1}`,
			other:   `{"_id":"m","_rev":"3-b","a":1,"_deleted":true}`,
			summary: "added: _deleted true",
		},
		{
			name:    "attachments are compared by count",
			winner:  `{"a":1,"_attachments":{"one.txt":{"stub":true},"two.txt":{"stub":true}}}`,
			other:   `{"a":1,"_attachments":{"one.txt":{"stub":true}}}`,
			summary: "changed: _attachments 2 attachment(s) → 1 attachment(s)",
		},
		{
			name:    "attachments added",
			winner:  `{"a":1}`,
			other:   `{"a":1,"_attachments":{"one.txt":{"stub":true}}}`,
			summary: "added: _attachments 1 attachment(s)",
		},
		{
			// The chooser deletes the revision that is not kept, so a
			// difference past the end of the summary must still be reported.
			name:    "a value differing past the summary is still changed",
			winner:  `{"note":"the same first forty characters here, then ALPHA"}`,
			other:   `{"note":"the same first forty characters here, then OMEGA"}`,
			summary: `changed: note "the same first forty characters here, … → "the same first forty characters here, …`,
		},
		{
			name:    "a long value identical past the summary is unchanged",
			winner:  `{"note":"the same first forty characters here, then ALPHA"}`,
			other:   `{"note":"the same first forty characters here, then ALPHA"}`,
			summary: "no differences from the current revision",
		},
		{
			// A JSON null is a value a document can hold, and it is the value
			// the chooser is least able to guess at: an empty summary reads as
			// though the field were rendered and came out blank.
			name:    "a field added with a null value",
			winner:  `{"a":1}`,
			other:   `{"a":1,"note":null}`,
			summary: "added: note null",
		},
		{
			name:    "a field changed to null",
			winner:  `{"a":1,"note":"x"}`,
			other:   `{"a":1,"note":null}`,
			summary: `changed: note "x" → null`,
		},
		{
			name:    "a field changed from null",
			winner:  `{"a":1,"note":null}`,
			other:   `{"a":1,"note":"x"}`,
			summary: `changed: note null → "x"`,
		},
		{
			name:    "a null field present in both is no difference",
			winner:  `{"a":1,"note":null}`,
			other:   `{"a":1,"note":null}`,
			summary: "no differences from the current revision",
		},
		{
			name:    "a non-object revision stays selectable",
			winner:  `{"a":1}`,
			other:   `[1,2,3]`,
			summary: "this revision is not a JSON object",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := DiffRevisions(json.RawMessage(tc.winner), json.RawMessage(tc.other), "3-b")
			if d.Rev != "3-b" {
				t.Errorf("Rev = %q", d.Rev)
			}
			if got := d.Summary(); got != tc.summary {
				t.Errorf("Summary() =\n  %s\nwant\n  %s", got, tc.summary)
			}
		})
	}
}

func TestDiffRevisionsKindsAndOrder(t *testing.T) {
	d := DiffRevisions(
		json.RawMessage(`{"b":1,"c":2}`),
		json.RawMessage(`{"a":1,"b":9}`),
		"3-b",
	)
	// Changes are sorted by field name, so output is stable.
	want := []struct {
		field string
		kind  ChangeKind
	}{
		{"a", ChangeAdded},
		{"b", ChangeChanged},
		{"c", ChangeRemoved},
	}
	if len(d.Changes) != len(want) {
		t.Fatalf("Changes = %+v", d.Changes)
	}
	for i, w := range want {
		if d.Changes[i].Field != w.field || d.Changes[i].Kind != w.kind {
			t.Errorf("change %d = %+v, want %s/%v", i, d.Changes[i], w.field, w.kind)
		}
	}
}

// conflictedServer answers with one winner and one conflicting revision.
func conflictedServer(t *testing.T) *couchtest.Server {
	t.Helper()
	srv := couchtest.New(t)
	srv.On("GET", "/mydb/m", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		switch r.URL.Query().Get("rev") {
		case "3-b":
			_, _ = io.WriteString(w, `{"_id":"m","_rev":"3-b","year":1995,"rating":9.3}`)
		case "3-a":
			_, _ = io.WriteString(w, `{"_id":"m","_rev":"3-a","year":1994}`)
		default:
			_, _ = io.WriteString(w, `{"_id":"m","_rev":"3-a","year":1994,"_conflicts":["3-b"]}`)
		}
	})
	srv.JSON("DELETE", "/mydb/m", 200, `{"ok":true,"id":"m","rev":"4-z"}`)
	return srv
}

func TestResolveChooserShowsDiffs(t *testing.T) {
	srv := conflictedServer(t)
	s := connected(t, srv)
	s.Prefs.Interactive = true
	s.Prefs.Yes = true
	s.SetStdin(strings.NewReader("1\n"))

	if _, err := invoke(t, Resolve(), s, "/mydb/m"); err != nil {
		t.Fatal(err)
	}
	out := s.Stdout.(*bytes.Buffer).String()
	if !strings.Contains(out, "1) 3-a (current)\n   no differences from the current revision") {
		t.Errorf("chooser did not describe the winner:\n%s", out)
	}
	if !strings.Contains(out, "2) 3-b (conflict)\n   changed: year 1994 → 1995; added: rating 9.3") {
		t.Errorf("chooser did not diff the conflict:\n%s", out)
	}
	if !strings.Contains(out, "Keep which revision? [1-2]") {
		t.Errorf("prompt missing:\n%s", out)
	}
}

func TestResolveDiffFullPrintsWholeRevisions(t *testing.T) {
	srv := conflictedServer(t)
	s := connected(t, srv)
	s.Prefs.Interactive = true
	s.Prefs.Yes = true
	s.SetStdin(strings.NewReader("2\n"))

	if _, err := invoke(t, Resolve(), s, "/mydb/m", "--diff-full"); err != nil {
		t.Fatal(err)
	}
	out := s.Stdout.(*bytes.Buffer).String()
	if !strings.Contains(out, "  {\n    \"_id\": \"m\",\n    \"_rev\": \"3-b\",") {
		t.Errorf("--diff-full did not print indented JSON:\n%s", out)
	}
	if strings.Contains(out, "changed: year") {
		t.Errorf("--diff-full still printed a summary:\n%s", out)
	}
}

func TestResolveDiffFullWithKeepIsAUsageError(t *testing.T) {
	srv := conflictedServer(t)
	s := connected(t, srv)
	_, err := invoke(t, Resolve(), s, "/mydb/m", "--diff-full", "--keep", "3-b", "--yes")
	var ue *UsageError
	if err == nil || !errors.As(err, &ue) {
		t.Fatalf("err = %v, want a UsageError", err)
	}
	if !strings.Contains(ue.Error(), "--diff-full shows the revisions before you choose one, so it has no effect with --keep.") {
		t.Errorf("message = %q", ue.Error())
	}
}
