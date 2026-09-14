package command

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
)

func TestSecurityShowsOneRowPerEntry(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/movies/_security", 200,
		`{"admins":{"names":["alice"],"roles":[]},"members":{"names":["bob","carol"],"roles":["reader"]}}`)
	s := connected(t, srv)
	res, err := invoke(t, SecurityCmd(), s, "/movies")
	if err != nil {
		t.Fatal(err)
	}
	rows, ok := res.(Rows)
	if !ok {
		t.Fatalf("result is %T, want Rows", res)
	}
	for i, want := range []string{"role", "kind", "value"} {
		if rows.Columns[i].Title != want {
			t.Fatalf("column %d = %q", i, rows.Columns[i].Title)
		}
	}
	var got []string
	for _, item := range rows.Items {
		got = append(got, strings.Join(item.Cells, "/"))
	}
	want := []string{
		"admins/names/alice",
		"admins/roles/(none)",
		"members/names/bob",
		"members/names/carol",
		"members/roles/reader",
	}
	if len(got) != len(want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %q, want %q", i, got[i], want[i])
		}
	}
	var payload map[string]string
	if err := json.Unmarshal(rows.Items[0].JSON, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["role"] != "admins" || payload["kind"] != "names" || payload["value"] != "alice" {
		t.Errorf("Row.JSON = %s", rows.Items[0].JSON)
	}
}

func TestSecurityEditMakesOnePutAndOneConfirmation(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/movies/_security", 200,
		`{"admins":{"names":["alice"],"roles":[]},"members":{"names":["bob"],"roles":[]}}`)
	srv.JSON("PUT", "/movies/_security", 200, `{"ok":true}`)
	s := connected(t, srv)
	s.Prefs.Interactive = true
	s.SetStdin(strings.NewReader("y\n"))

	res, err := invoke(t, SecurityCmd(), s, "/movies",
		"--add-member", "carol", "--remove-member", "bob", "--add-admin-role", "ops")
	if err != nil {
		t.Fatal(err)
	}
	out := s.Stdout.(*bytes.Buffer).String()
	if strings.Count(out, "[y/N]") != 1 {
		t.Fatalf("asked %d times, want once: %q", strings.Count(out, "[y/N]"), out)
	}
	// The clauses run in flag-declaration order, and securityPrompt capitalises
	// the first one, so the admin-role clause is the capitalised one here.
	for _, want := range []string{
		"Grant admin access on movies to role ops",
		"grant member access on movies to carol",
		"revoke member access on movies from bob",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("prompt %q is missing %q", out, want)
		}
	}
	if n := len(srv.Requests()); n == 0 {
		t.Fatal("nothing was sent")
	}
	puts := 0
	for _, r := range srv.Requests() {
		if r.Method == "PUT" {
			puts++
		}
	}
	if puts != 1 {
		t.Fatalf("made %d PUTs, want exactly one", puts)
	}
	var body struct {
		Admins  struct{ Names, Roles []string }
		Members struct{ Names, Roles []string }
	}
	if err := json.Unmarshal(srv.Last("PUT", "/movies/_security").Body, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Members.Names) != 1 || body.Members.Names[0] != "carol" {
		t.Errorf("members.names = %v", body.Members.Names)
	}
	if len(body.Admins.Roles) != 1 || body.Admins.Roles[0] != "ops" {
		t.Errorf("admins.roles = %v", body.Admins.Roles)
	}
	if len(body.Admins.Names) != 1 || body.Admins.Names[0] != "alice" {
		t.Errorf("admins.names = %v; an untouched list changed", body.Admins.Names)
	}
	if msg := res.(Message).Text; msg != "Changed the security of movies." {
		t.Errorf("message = %q", msg)
	}
}

func TestSecuritySingleChangeReadsAsTheSpecSentence(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/movies/_security", 200, `{}`)
	srv.JSON("PUT", "/movies/_security", 200, `{"ok":true}`)
	s := connected(t, srv)
	s.Prefs.Interactive = true
	s.SetStdin(strings.NewReader("y\n"))
	if _, err := invoke(t, SecurityCmd(), s, "/movies", "--add-member", "alice"); err != nil {
		t.Fatal(err)
	}
	if out := s.Stdout.(*bytes.Buffer).String(); !strings.HasPrefix(out, "Grant member access on movies to alice? [y/N]") {
		t.Errorf("prompt = %q", out)
	}
}

func TestSecurityNoChangeMakesNoRequest(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/movies/_security", 200, `{"admins":{"names":["alice"],"roles":[]},"members":{"names":[],"roles":[]}}`)
	s := connected(t, srv)
	s.Prefs.Yes = true
	res, err := invoke(t, SecurityCmd(), s, "/movies", "--add-admin", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if msg := res.(Message).Text; msg != "The security of movies is already as asked; nothing was changed." {
		t.Errorf("message = %q", msg)
	}
	if srv.Last("PUT", "/movies/_security") != nil {
		t.Error("an unchanged document was written")
	}
}

func TestSecurityNeedsADatabasePath(t *testing.T) {
	srv := couchtest.New(t)
	s := connected(t, srv)
	_, err := invoke(t, SecurityCmd(), s, "/movies/doc1")
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Fatalf("error = %v", err)
	}
}
