package command

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

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
	srv.JSON("GET", "/_node/_local/_config/admins", 200, `{}`)
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

func TestUsersAddWritesTheDocumentAndPromptsTwice(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("HEAD", "/_users", 200, ``) // DatabaseExists issues HEAD /_users
	srv.JSON("GET", "/_users/org.couchdb.user:alice", 404, `{"error":"not_found","reason":"missing"}`)
	srv.JSON("GET", "/_node/_local/_config/admins", 200, `{}`)
	srv.JSON("PUT", "/_users/org.couchdb.user:alice", 201, `{"ok":true,"id":"org.couchdb.user:alice","rev":"1-b"}`)
	s := connected(t, srv)
	s.Prefs.Interactive = true
	s.SetStdin(strings.NewReader("hunter2\nhunter2\n"))

	res, err := invoke(t, Users(), s, "add", "alice", "--roles", "editor,reader")
	if err != nil {
		t.Fatal(err)
	}
	if msg := res.(Message).Text; msg != `Created user "alice".` {
		t.Errorf("message = %q", msg)
	}
	body := string(srv.Last("PUT", "/_users/org.couchdb.user:alice").Body)
	for _, want := range []string{
		`"_id":"org.couchdb.user:alice"`, `"name":"alice"`, `"type":"user"`,
		`"roles":["editor","reader"]`, `"password":"hunter2"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body %s is missing %s", body, want)
		}
	}
	out := s.Stdout.(*bytes.Buffer).String()
	if strings.Count(out, "Password for alice") != 1 || !strings.Contains(out, "Repeat password for alice") {
		t.Errorf("prompts = %q; the password must be asked for twice", out)
	}
	if strings.Contains(out, "hunter2") {
		t.Fatal("the password was echoed")
	}
}

func TestUsersAddRefusesAnExistingName(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("HEAD", "/_users", 200, ``)
	srv.JSON("GET", "/_users/org.couchdb.user:alice", 200, `{"_id":"org.couchdb.user:alice","_rev":"1-b","name":"alice","type":"user","roles":[]}`)
	srv.JSON("GET", "/_node/_local/_config/admins", 200, `{}`)
	s := connected(t, srv)
	s.Prefs.Interactive = true
	s.SetStdin(strings.NewReader("hunter2\nhunter2\n"))
	_, err := invoke(t, Users(), s, "add", "alice")
	if err == nil || !strings.Contains(err.Error(), `"alice" already exists`) {
		t.Fatalf("error = %v", err)
	}
	if srv.Last("PUT", "/_users/org.couchdb.user:alice") != nil {
		t.Error("the existing user was overwritten")
	}
}

func TestUsersAddCreatesTheUsersDatabase(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("HEAD", "/_users", 404, ``)
	srv.JSON("PUT", "/_users", 201, `{"ok":true}`)
	srv.JSON("GET", "/_users/org.couchdb.user:alice", 404, `{"error":"not_found","reason":"missing"}`)
	srv.JSON("GET", "/_node/_local/_config/admins", 200, `{}`)
	srv.JSON("PUT", "/_users/org.couchdb.user:alice", 201, `{"ok":true,"id":"org.couchdb.user:alice","rev":"1-b"}`)
	s := connected(t, srv)
	s.Prefs.Interactive = true
	s.SetStdin(strings.NewReader("hunter2\nhunter2\n"))
	res, err := invoke(t, Users(), s, "add", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if msg := res.(Message).Text; msg != `Created the _users database and user "alice".` {
		t.Errorf("message = %q", msg)
	}
	if srv.Last("PUT", "/_users") == nil {
		t.Error("the _users database was not created")
	}
}

func TestUsersAddAdminWritesTheConfigKey(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_node/_local/_config/admins", 200, `{}`)
	srv.JSON("PUT", "/_node/_local/_config/admins/ops", 200, `""`)
	s := connected(t, srv)
	s.Prefs.Interactive = true
	s.SetStdin(strings.NewReader("hunter2\nhunter2\n"))
	res, err := invoke(t, Users(), s, "add", "ops", "--admin")
	if err != nil {
		t.Fatal(err)
	}
	if msg := res.(Message).Text; msg != `Created server admin "ops".` {
		t.Errorf("message = %q", msg)
	}
	if body := strings.TrimSpace(string(srv.Last("PUT", "/_node/_local/_config/admins/ops").Body)); body != `"hunter2"` {
		t.Errorf("body = %s", body)
	}
	if srv.Last("PUT", "/_users/org.couchdb.user:ops") != nil {
		t.Error("a server admin was also written to _users")
	}
}

// waitForAdminHashWith is the command's own clock injection point, mirroring
// watchCompactionWith in compact_test.go: real intervals would make this test
// wait out the real 2-second budget.
func TestWaitForAdminHashHintsOnTimeout(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_node/_local/_config/admins/ops", 200, `"hunter2"`)
	s := connected(t, srv)
	waitForAdminHashWith(context.Background(), s, "ops", time.Millisecond, 5*time.Millisecond, time.Now)
	if out := s.Stderr.(*bytes.Buffer).String(); !strings.Contains(out, adminHashPendingHint) {
		t.Errorf("stderr = %q, want the pending hint", out)
	}
}

func TestWaitForAdminHashSaysNothingWhenItHashesInTime(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSONSeq("GET", "/_node/_local/_config/admins/ops", 200, `"hunter2"`, `"-pbkdf2-deadbeef,cafe,10"`)
	s := connected(t, srv)
	waitForAdminHashWith(context.Background(), s, "ops", time.Millisecond, time.Second, time.Now)
	if out := s.Stderr.(*bytes.Buffer).String(); out != "" {
		t.Errorf("stderr = %q, want no hint once the hash appears", out)
	}
}

func TestWaitForAdminHashSaysNothingWhenCancelled(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_node/_local/_config/admins/ops", 200, `"hunter2"`)
	s := connected(t, srv)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	waitForAdminHashWith(ctx, s, "ops", time.Millisecond, time.Second, time.Now)
	if out := s.Stderr.(*bytes.Buffer).String(); out != "" {
		t.Errorf("stderr = %q, want no hint on cancellation", out)
	}
}

func TestUsersPasswdStripsTheHashFields(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_node/_local/_config/admins", 200, `{}`)
	srv.JSON("GET", "/_users/org.couchdb.user:alice", 200,
		`{"_id":"org.couchdb.user:alice","_rev":"3-c","name":"alice","type":"user",
		  "roles":["editor"],"password_scheme":"pbkdf2","iterations":10,
		  "derived_key":"deadbeef","salt":"cafe"}`)
	srv.JSON("PUT", "/_users/org.couchdb.user:alice", 201, `{"ok":true,"id":"org.couchdb.user:alice","rev":"4-d"}`)
	s := connected(t, srv)
	s.SetStdin(strings.NewReader("newpass\n"))
	res, err := invoke(t, Users(), s, "passwd", "alice", "--password-stdin")
	if err != nil {
		t.Fatal(err)
	}
	if msg := res.(Message).Text; msg != `Changed the password of "alice".` {
		t.Errorf("message = %q", msg)
	}
	body := string(srv.Last("PUT", "/_users/org.couchdb.user:alice").Body)
	for _, gone := range []string{"password_sha", "salt", "derived_key", "iterations", "password_scheme"} {
		if strings.Contains(body, gone) {
			t.Errorf("body %s still carries %s; the server would ignore the new password", body, gone)
		}
	}
	if !strings.Contains(body, `"password":"newpass"`) || !strings.Contains(body, `"_rev":"3-c"`) {
		t.Errorf("body = %s", body)
	}
	if !strings.Contains(body, `"roles":["editor"]`) {
		t.Errorf("body %s dropped the roles", body)
	}
}

func TestUsersPasswdOnAServerAdminRewritesTheConfigKey(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_node/_local/_config/admins", 200, `{"ops":"-pbkdf2-deadbeef,cafe,10"}`)
	srv.JSON("PUT", "/_node/_local/_config/admins/ops", 200, `"-pbkdf2-deadbeef,cafe,10"`)
	s := connected(t, srv)
	s.SetStdin(strings.NewReader("newpass\n"))
	res, err := invoke(t, Users(), s, "passwd", "ops", "--password-stdin")
	if err != nil {
		t.Fatal(err)
	}
	if msg := res.(Message).Text; msg != `Changed the password of server admin "ops".` {
		t.Errorf("message = %q", msg)
	}
	if srv.Last("GET", "/_users/org.couchdb.user:ops") != nil {
		t.Error("a server admin was looked up in _users")
	}
	if strings.Contains(res.(Message).Text, "deadbeef") {
		t.Fatal("the old hash reached the message")
	}
}

func TestUsersRemoveRefusesTheConnectedAccount(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_session", 200,
		`{"ok":true,"userCtx":{"name":"alice","roles":["_admin"]},"info":{"authenticated":"default"}}`)
	s := connected(t, srv)
	s.Prefs.Yes = true
	_, err := invoke(t, Users(), s, "rm", "alice")
	if err == nil || err.Error() != "You are connected as alice; remove that user from another account." {
		t.Fatalf("error = %v", err)
	}
	if srv.Last("DELETE", "/_users/org.couchdb.user:alice") != nil {
		t.Error("the connected account was deleted anyway")
	}
}

func TestUsersRemoveDeletesTheDocument(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_session", 200,
		`{"ok":true,"userCtx":{"name":"admin","roles":["_admin"]},"info":{"authenticated":"default"}}`)
	srv.JSON("GET", "/_node/_local/_config/admins", 200, `{"admin":"-pbkdf2-x"}`)
	// GetRev reads the revision out of the ETag of a HEAD, so this stub has to
	// set the header; couchtest.Server.JSON only writes a body.
	srv.On("HEAD", "/_users/org.couchdb.user:alice", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"3-c"`)
		w.WriteHeader(http.StatusOK)
	})
	srv.JSON("DELETE", "/_users/org.couchdb.user:alice", 200, `{"ok":true,"id":"org.couchdb.user:alice","rev":"4-d"}`)
	s := connected(t, srv)
	s.Prefs.Interactive = true
	s.SetStdin(strings.NewReader("y\n"))
	res, err := invoke(t, Users(), s, "rm", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if out := s.Stdout.(*bytes.Buffer).String(); !strings.Contains(out, "Delete user alice?") {
		t.Errorf("prompt = %q", out)
	}
	if msg := res.(Message).Text; msg != `Deleted user "alice".` {
		t.Errorf("message = %q", msg)
	}
	if got := srv.Last("DELETE", "/_users/org.couchdb.user:alice").Query("rev"); got != "3-c" {
		t.Errorf("delete rev = %q", got)
	}
}

func TestUsersPasswordNeedsATerminalOrStdin(t *testing.T) {
	// The password is asked for last: "users add" looks the name up in
	// [admins], makes sure _users exists and checks the name is free before it
	// prompts, so all three have to answer before the usage error can happen.
	srv := couchtest.New(t)
	srv.JSON("GET", "/_node/_local/_config/admins", 200, `{}`)
	srv.JSON("HEAD", "/_users", 200, ``)
	srv.JSON("GET", "/_users/org.couchdb.user:alice", 404, `{"error":"not_found","reason":"missing"}`)
	s := connected(t, srv)
	s.Prefs.Interactive = false
	_, err := invoke(t, Users(), s, "add", "alice")
	var ue *UsageError
	if !errors.As(err, &ue) || !strings.Contains(ue.Reason, "--password-stdin") {
		t.Fatalf("error = %v", err)
	}
}

// failingStdin is a standard input that breaks rather than ending. Reading a
// password from it must be reported, not silently treated as no password.
type failingStdin struct{}

func (failingStdin) Read([]byte) (int, error) { return 0, errors.New("input device is not a stream") }

func TestUsersAddAdminWaitsForCouchDBToHashThePassword(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_node/_local/_config/admins", 200, `{}`)
	srv.JSON("PUT", "/_node/_local/_config/admins/ops", 200, `""`)
	// CouchDB stores the plaintext and hashes it a moment later; until it has,
	// the new password is refused. The command must not report success while
	// the section still holds what was typed.
	srv.JSONSeq("GET", "/_node/_local/_config/admins/ops", 200,
		`"hunter2"`, `"hunter2"`, `"-pbkdf2:sha256-deadbeef,cafe,10"`)
	s := connected(t, srv)
	s.Prefs.Interactive = true
	s.SetStdin(strings.NewReader("hunter2\nhunter2\n"))

	// adminHashPoll is shrunk for this test so the retries below run without
	// waiting out the real 100ms interval three times over.
	oldPoll := adminHashPoll
	adminHashPoll = time.Millisecond
	t.Cleanup(func() { adminHashPoll = oldPoll })

	if _, err := invoke(t, Users(), s, "add", "ops", "--admin"); err != nil {
		t.Fatal(err)
	}
	polls := 0
	for _, r := range srv.Requests() {
		if r.Method == "GET" && r.Path == "/_node/_local/_config/admins/ops" {
			polls++
		}
	}
	// Three: the two answers that were still the plaintext, and the hashed one
	// that let the command return. Fewer means it returned too early.
	if polls < 3 {
		t.Errorf("the key was read %d times; the command returned before the password was hashed", polls)
	}
	if out := s.Stderr.(*bytes.Buffer).String(); out != "" {
		t.Errorf("stderr = %q, want no hint once the hash appears", out)
	}
}

func TestUsersShowOnAServerAdmin(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_node/_local/_config/admins", 200, `{"admin":"-pbkdf2-deadbeef,cafe,10"}`)
	s := connected(t, srv)
	res, err := invoke(t, Users(), s, "show", "admin")
	if err != nil {
		t.Fatalf("error = %v; a name that users list prints must be one users show can print", err)
	}
	fields := map[string]string{}
	all := ""
	for _, item := range res.(Rows).Items {
		fields[item.Cells[0]] = item.Cells[1]
		all += strings.Join(item.Cells, " ") + string(item.JSON)
	}
	if fields["name"] != "admin" || fields["kind"] != "server admin" || fields["roles"] != "_admin" {
		t.Errorf("fields = %v", fields)
	}
	if fields["password"] != "set" {
		t.Errorf("password field = %q", fields["password"])
	}
	if strings.Contains(all, "deadbeef") || strings.Contains(all, "cafe") {
		t.Fatal("show printed the hash or the salt")
	}
	if srv.Last("GET", "/_users/org.couchdb.user:admin") != nil {
		t.Error("a server admin was looked up in _users")
	}
}

func TestUsersPasswordReportsAStdinReadFailure(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_node/_local/_config/admins", 200, `{}`)
	srv.JSON("GET", "/_users/org.couchdb.user:alice", 200,
		`{"_id":"org.couchdb.user:alice","_rev":"3-c","name":"alice","type":"user","roles":[]}`)
	s := connected(t, srv)
	s.SetStdin(failingStdin{})
	_, err := invoke(t, Users(), s, "passwd", "alice", "--password-stdin")
	if err == nil {
		t.Fatal("a broken standard input was accepted as a password")
	}
	var ue *UsageError
	if errors.As(err, &ue) {
		t.Errorf("error = %v; a device that failed is not the operator forgetting to pipe a password", err)
	}
	if !strings.Contains(err.Error(), "input device is not a stream") {
		t.Errorf("error = %v; it does not say what went wrong", err)
	}
	if srv.Last("PUT", "/_users/org.couchdb.user:alice") != nil {
		t.Error("a password was written even though none was read")
	}
}

func TestUsersPasswdRereadsTheRevisionAfterAConflict(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_node/_local/_config/admins", 200, `{}`)
	// The document is edited while the operator is typing: the revision read
	// before the prompt is stale by the time the write goes out.
	srv.JSONSeq("GET", "/_users/org.couchdb.user:alice", 200,
		`{"_id":"org.couchdb.user:alice","_rev":"3-c","name":"alice","type":"user","roles":["editor"]}`,
		`{"_id":"org.couchdb.user:alice","_rev":"4-d","name":"alice","type":"user","roles":["editor","reader"]}`)
	var puts int
	srv.On("PUT", "/_users/org.couchdb.user:alice", func(w http.ResponseWriter, r *http.Request) {
		puts++
		w.Header().Set("Content-Type", "application/json")
		if puts == 1 {
			w.WriteHeader(409)
			_, _ = w.Write([]byte(`{"error":"conflict","reason":"Document update conflict."}`))
			return
		}
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"ok":true,"id":"org.couchdb.user:alice","rev":"5-e"}`))
	})
	s := connected(t, srv)
	s.SetStdin(strings.NewReader("newpass\n"))
	res, err := invoke(t, Users(), s, "passwd", "alice", "--password-stdin")
	if err != nil {
		t.Fatalf("error = %v; a conflict during the prompt must be retried once", err)
	}
	if msg := res.(Message).Text; msg != `Changed the password of "alice".` {
		t.Errorf("message = %q", msg)
	}
	body := string(srv.Last("PUT", "/_users/org.couchdb.user:alice").Body)
	if !strings.Contains(body, `"_rev":"4-d"`) || !strings.Contains(body, `"password":"newpass"`) {
		t.Errorf("the retry wrote %s", body)
	}
	if !strings.Contains(body, `"roles":["editor","reader"]`) {
		t.Errorf("the retry lost the concurrent edit: %s", body)
	}
	for _, gone := range hashFields {
		if strings.Contains(body, gone) {
			t.Errorf("the retry left %s in %s", gone, body)
		}
	}
}

func TestUsersPasswdAdminRefusesAnUnknownServerAdmin(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_node/_local/_config/admins", 200, `{"admin":"-pbkdf2-deadbeef,cafe,10"}`)
	srv.JSON("PUT", "/_node/_local/_config/admins/ghost", 200, `""`)
	s := connected(t, srv)
	s.SetStdin(strings.NewReader("newpass\n"))
	_, err := invoke(t, Users(), s, "passwd", "ghost", "--admin", "--password-stdin")
	want := `Server admin "ghost" does not exist; use users add ghost --admin to create one.`
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
	if srv.Last("PUT", "/_node/_local/_config/admins/ghost") != nil {
		t.Error("a server admin was created by passwd --admin")
	}
}

func TestUsersRemoveAdminOnAnUnknownNameSaysSo(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_session", 200,
		`{"ok":true,"userCtx":{"name":"admin","roles":["_admin"]},"info":{"authenticated":"default"}}`)
	srv.JSON("GET", "/_node/_local/_config/admins", 200, `{"admin":"-pbkdf2-x"}`)
	srv.JSON("DELETE", "/_node/_local/_config/admins/ghost", 404,
		`{"error":"not_found","reason":"unknown_config_value"}`)
	s := connected(t, srv)
	s.Prefs.Yes = true
	_, err := invoke(t, Users(), s, "rm", "ghost", "--admin")
	want := `Server admin "ghost" does not exist.`
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
}
