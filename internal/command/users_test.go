package command

import (
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
)

// twoUsers is _users/_all_docs?include_docs=true with one user, one design
// document (which must be skipped) and the _security document CouchDB puts in
// some installs (which must also be skipped, because its type is not "user").
const twoUsers = `{"total_rows":3,"offset":0,"rows":[
	{"id":"_design/_auth","key":"_design/_auth","value":{"rev":"1-a"},
	 "doc":{"_id":"_design/_auth","_rev":"1-a","language":"javascript"}},
	{"id":"org.couchdb.user:alice","key":"org.couchdb.user:alice","value":{"rev":"1-b"},
	 "doc":{"_id":"org.couchdb.user:alice","_rev":"1-b","name":"alice","type":"user",
	        "roles":["editor","reader"],"password_scheme":"pbkdf2","iterations":10,
	        "derived_key":"deadbeef","salt":"cafe"}},
	{"id":"org.couchdb.user:bob","key":"org.couchdb.user:bob","value":{"rev":"1-c"},
	 "doc":{"_id":"org.couchdb.user:bob","_rev":"1-c","name":"bob","type":"user","roles":[]}}]}`

func TestUsersListsUsersAndServerAdmins(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_users/_all_docs", 200, twoUsers)
	srv.JSON("GET", "/_node/_local/_config/admins", 200, `{"admin":"-pbkdf2-deadbeef,cafe,10"}`)
	s := connected(t, srv)
	res, err := invoke(t, Users(), s)
	if err != nil {
		t.Fatal(err)
	}
	rows, ok := res.(Rows)
	if !ok {
		t.Fatalf("result is %T, want Rows", res)
	}
	for i, want := range []string{"name", "kind", "roles"} {
		if rows.Columns[i].Title != want {
			t.Fatalf("column %d = %q", i, rows.Columns[i].Title)
		}
	}
	if len(rows.Items) != 3 {
		t.Fatalf("got %d rows, want alice, bob and the server admin: %#v", len(rows.Items), rows.Items)
	}
	byName := map[string][]string{}
	for _, item := range rows.Items {
		byName[item.Cells[0]] = item.Cells
	}
	if got := byName["alice"]; got == nil || got[1] != "user" || got[2] != "editor, reader" {
		t.Errorf("alice = %v", got)
	}
	if got := byName["bob"]; got == nil || got[2] != "" {
		t.Errorf("bob = %v", got)
	}
	if got := byName["admin"]; got == nil || got[1] != "server admin" {
		t.Errorf("admin = %v", got)
	}
	joined := ""
	for _, item := range rows.Items {
		joined += string(item.JSON)
	}
	if strings.Contains(joined, "deadbeef") {
		t.Fatal("a password hash reached the output")
	}
}

func TestUsersListWithoutTheUsersDatabaseSaysSo(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_users/_all_docs", 404, `{"error":"not_found","reason":"Database does not exist."}`)
	srv.JSON("GET", "/_node/_local/_config/admins", 200, `{}`)
	s := connected(t, srv)
	res, err := invoke(t, Users(), s)
	if err != nil {
		t.Fatal(err)
	}
	msg, ok := res.(Message)
	if !ok || msg.Text != `No users are defined; the _users database does not exist yet. "users add <name>" creates it.` {
		t.Fatalf("result = %#v", res)
	}
}

func TestUsersShowNeverPrintsAHash(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_users/org.couchdb.user:alice", 200,
		`{"_id":"org.couchdb.user:alice","_rev":"1-b","name":"alice","type":"user",
		  "roles":["editor"],"password_scheme":"pbkdf2","iterations":10,
		  "derived_key":"deadbeef","salt":"cafe"}`)
	s := connected(t, srv)
	res, err := invoke(t, Users(), s, "show", "alice")
	if err != nil {
		t.Fatal(err)
	}
	rows := res.(Rows)
	fields := map[string]string{}
	for _, item := range rows.Items {
		fields[item.Cells[0]] = item.Cells[1]
	}
	if fields["name"] != "alice" || fields["type"] != "user" || fields["roles"] != "editor" {
		t.Errorf("fields = %v", fields)
	}
	if fields["password"] != "set" {
		t.Errorf("password field = %q, want \"set\"", fields["password"])
	}
	all := ""
	for _, item := range rows.Items {
		all += strings.Join(item.Cells, " ") + string(item.JSON)
	}
	if strings.Contains(all, "deadbeef") || strings.Contains(all, "cafe") {
		t.Fatal("show printed the hash or the salt")
	}
}
