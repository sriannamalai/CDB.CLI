package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/couch"
	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
)

func TestCatDocument(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb/doc1", 200, `{"_id":"doc1","_rev":"1-a","name":"alice"}`)
	s := connected(t, srv)
	s.SetPath("/mydb")
	res, err := invoke(t, Cat(), s, "doc1")
	if err != nil {
		t.Fatal(err)
	}
	doc, ok := res.(Document)
	if !ok {
		t.Fatalf("result is %T, want Document", res)
	}
	var m map[string]any
	if err := json.Unmarshal(doc.JSON, &m); err != nil {
		t.Fatal(err)
	}
	if m["name"] != "alice" {
		t.Errorf("document = %s", doc.JSON)
	}
}

func TestCatPassesRevAndConflicts(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb/doc1", 200, `{"_id":"doc1","_rev":"1-a"}`)
	s := connected(t, srv)
	s.SetPath("/mydb")
	if _, err := invoke(t, Cat(), s, "doc1", "--rev", "1-a", "--conflicts", "--revs"); err != nil {
		t.Fatal(err)
	}
	req := srv.Last("GET", "/mydb/doc1")
	if req.Query("rev") != "1-a" || req.Query("conflicts") != "true" || req.Query("revs") != "true" {
		t.Errorf("query = %q", req.RawQuery)
	}
}

func TestCatDesignDocument(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb/_design/app", 200, `{"_id":"_design/app","_rev":"1-a","views":{}}`)
	s := connected(t, srv)
	if _, err := invoke(t, Cat(), s, "/mydb/_design/app"); err != nil {
		t.Fatal(err)
	}
	if srv.Last("GET", "/mydb/_design/app") == nil {
		t.Error("cat did not read the design document")
	}
}

func TestCatRejectsADatabase(t *testing.T) {
	srv := couchtest.New(t)
	s := connected(t, srv)
	_, err := invoke(t, Cat(), s, "/mydb")
	if _, ok := err.(*UsageError); !ok {
		t.Fatalf("cat /mydb = %#v, want a *UsageError", err)
	}
}

func TestPutFromStdinFetchesRevAndAsksFirst(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("HEAD", "/mydb/doc1", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `"1-a"`)
		w.WriteHeader(200)
	})
	srv.JSON("PUT", "/mydb/doc1", 201, `{"ok":true,"id":"doc1","rev":"2-b"}`)
	s := connected(t, srv)
	s.SetPath("/mydb")
	s.SetStdin(strings.NewReader(`{"name":"bob"}`))
	s.Prefs.Yes = true
	res, err := invoke(t, Put(), s, "doc1")
	if err != nil {
		t.Fatal(err)
	}
	msg, ok := res.(Message)
	if !ok || !strings.Contains(msg.Text, "2-b") {
		t.Errorf("result = %#v, want a message naming the new rev", res)
	}
	put := srv.Last("PUT", "/mydb/doc1")
	if put.Query("rev") != "1-a" {
		t.Error("put did not send the fetched rev")
	}
	if string(put.Body) != `{"name":"bob"}` {
		t.Errorf("body = %s", put.Body)
	}
}

func TestPutOfANewDocumentSendsNoRev(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("PUT", "/mydb/new", 201, `{"ok":true,"id":"new","rev":"1-a"}`)
	s := connected(t, srv)
	s.SetPath("/mydb")
	s.SetStdin(strings.NewReader(`{"name":"carol"}`))
	if _, err := invoke(t, Put(), s, "new"); err != nil {
		t.Fatal(err)
	}
	// The HEAD 404s, so there is nothing to overwrite and nothing to confirm.
	if got := srv.Last("PUT", "/mydb/new").RawQuery; got != "" {
		t.Errorf("query = %q, want no rev on a create", got)
	}
}

func TestPutUsesRevFromTheDocumentBody(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("PUT", "/mydb/doc1", 201, `{"ok":true,"id":"doc1","rev":"3-c"}`)
	s := connected(t, srv)
	s.SetPath("/mydb")
	s.SetStdin(strings.NewReader(`{"_id":"doc1","_rev":"2-b","name":"bob"}`))
	if _, err := invoke(t, Put(), s, "doc1"); err != nil {
		t.Fatal(err)
	}
	if srv.Last("HEAD", "/mydb/doc1") != nil {
		t.Error("put fetched a rev even though the body carried one")
	}
	if srv.Last("PUT", "/mydb/doc1").Query("rev") != "2-b" {
		t.Error("put did not use the _rev from the body")
	}
}

func TestPutFromAFile(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("PUT", "/mydb/doc1", 201, `{"ok":true,"id":"doc1","rev":"1-a"}`)
	file := filepath.Join(t.TempDir(), "doc.json")
	if err := os.WriteFile(file, []byte(`{"from":"file"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := connected(t, srv)
	s.SetPath("/mydb")
	if _, err := invoke(t, Put(), s, "doc1", file); err != nil {
		t.Fatal(err)
	}
	if got := string(srv.Last("PUT", "/mydb/doc1").Body); got != `{"from":"file"}` {
		t.Errorf("body = %s", got)
	}
}

func TestPutDeclinedLeavesTheDocumentAlone(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("HEAD", "/mydb/doc1", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `"1-a"`)
		w.WriteHeader(200)
	})
	srv.JSON("PUT", "/mydb/doc1", 201, `{"ok":true,"id":"doc1","rev":"2-b"}`)
	s := connected(t, srv)
	s.SetPath("/mydb")
	s.Prefs.Interactive = true
	// The document body is the whole of stdin, so the prompt that follows sees
	// EOF and must treat that as "no" rather than as consent.
	s.SetStdin(strings.NewReader(`{"name":"bob"}`))
	if _, err := invoke(t, Put(), s, "doc1"); err == nil {
		t.Fatal("put overwrote without an answer")
	}
	if srv.Last("PUT", "/mydb/doc1") != nil {
		t.Error("put wrote after the operator declined")
	}
}

func TestPutRejectsInvalidJSON(t *testing.T) {
	srv := couchtest.New(t)
	s := connected(t, srv)
	s.SetPath("/mydb")
	s.SetStdin(strings.NewReader(`{not json`))
	_, err := invoke(t, Put(), s, "doc1")
	if err == nil {
		t.Fatal("put accepted invalid JSON")
	}
	if !strings.Contains(err.Error(), "JSON") {
		t.Errorf("error = %v, want it to mention JSON", err)
	}
}

func TestRmConfirmsBeforeDeleting(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("HEAD", "/mydb/doc1", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `"1-a"`)
		w.WriteHeader(200)
	})
	srv.JSON("DELETE", "/mydb/doc1", 200, `{"ok":true,"id":"doc1","rev":"2-b"}`)

	s := connected(t, srv)
	s.SetPath("/mydb")
	s.SetStdin(strings.NewReader("n\n"))
	s.Stdout = &bytes.Buffer{}
	s.Prefs.Interactive = true
	if _, err := invoke(t, Rm(), s, "doc1"); err == nil {
		t.Fatal("rm deleted without confirmation")
	}
	if srv.Last("DELETE", "/mydb/doc1") != nil {
		t.Fatal("rm sent DELETE after the operator declined")
	}

	s.SetStdin(strings.NewReader("y\n"))
	if _, err := invoke(t, Rm(), s, "doc1"); err != nil {
		t.Fatal(err)
	}
	del := srv.Last("DELETE", "/mydb/doc1")
	if del == nil {
		t.Fatal("rm did not send DELETE after confirmation")
	}
	if del.Query("rev") != "1-a" {
		t.Errorf("DELETE rev = %q, want the rev HEAD reported", del.Query("rev"))
	}
	if prompt := s.Stdout.(*bytes.Buffer).String(); !strings.Contains(prompt, "1-a") {
		t.Errorf("prompt = %q, want it to name the revision", prompt)
	}
}

func TestRmUsesTheRevFlagWithoutAHead(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("DELETE", "/mydb/doc1", 200, `{"ok":true,"id":"doc1","rev":"3-c"}`)
	s := connected(t, srv)
	s.SetPath("/mydb")
	if _, err := invoke(t, Rm(), s, "doc1", "--rev", "2-b", "--yes"); err != nil {
		t.Fatal(err)
	}
	if srv.Last("HEAD", "/mydb/doc1") != nil {
		t.Error("rm fetched a rev even though --rev was given")
	}
	if srv.Last("DELETE", "/mydb/doc1").Query("rev") != "2-b" {
		t.Error("rm did not send the --rev revision")
	}
}

func TestRmWithYesSkipsTheConfirmation(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("HEAD", "/mydb/doc1", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `"1-a"`)
		w.WriteHeader(200)
	})
	srv.JSON("DELETE", "/mydb/doc1", 200, `{"ok":true,"id":"doc1","rev":"2-b"}`)
	s := connected(t, srv)
	s.SetPath("/mydb")
	if _, err := invoke(t, Rm(), s, "doc1", "--yes"); err != nil {
		t.Fatal(err)
	}
	if srv.Last("DELETE", "/mydb/doc1") == nil {
		t.Fatal("rm --yes did not delete")
	}
}

func TestRmOnANonTerminalWithoutYesIsAUsageError(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("HEAD", "/mydb/doc1", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `"1-a"`)
		w.WriteHeader(200)
	})
	srv.JSON("DELETE", "/mydb/doc1", 200, `{"ok":true,"id":"doc1","rev":"2-b"}`)
	s := connected(t, srv)
	s.SetPath("/mydb")
	_, err := invoke(t, Rm(), s, "doc1")
	if _, ok := err.(*UsageError); !ok {
		t.Fatalf("rm = %#v, want a *UsageError", err)
	}
	if srv.Last("DELETE", "/mydb/doc1") != nil {
		t.Error("rm deleted on a non-terminal without --yes")
	}
}

func TestRmRejectsADatabase(t *testing.T) {
	srv := couchtest.New(t)
	s := connected(t, srv)
	_, err := invoke(t, Rm(), s, "/mydb", "--yes")
	if _, ok := err.(*UsageError); !ok {
		t.Fatalf("rm /mydb = %#v, want a *UsageError", err)
	}
}

func TestRmIsMarkedDestructive(t *testing.T) {
	if !Rm().Destructive {
		t.Error("rm is not marked Destructive")
	}
}

// editorScript writes an executable stub editor that runs body against the
// file it is given, and points $EDITOR at it. It returns a func reporting how
// many times the editor was launched.
func editorScript(t *testing.T, body string) func() int {
	t.Helper()
	dir := t.TempDir()
	name := filepath.Join(dir, "editor.sh")
	log := filepath.Join(dir, "runs")
	script := "#!/bin/sh\necho run >> " + log + "\n" + body + "\n"
	if err := os.WriteFile(name, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", name)
	return func() int {
		b, err := os.ReadFile(log)
		if err != nil {
			return 0
		}
		return strings.Count(string(b), "run\n")
	}
}

func TestEditWritesTheEditedDocument(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb/doc1", 200, `{"_id":"doc1","_rev":"1-a","n":1}`)
	srv.JSON("PUT", "/mydb/doc1", 201, `{"ok":true,"id":"doc1","rev":"2-b"}`)
	editorScript(t, `printf '{"_id":"doc1","n":2}' > "$1"`)
	s := connected(t, srv)
	s.SetPath("/mydb")
	res, err := invoke(t, Edit(), s, "doc1")
	if err != nil {
		t.Fatal(err)
	}
	msg, ok := res.(Message)
	if !ok || !strings.Contains(msg.Text, "2-b") {
		t.Errorf("result = %#v, want a message naming the new rev", res)
	}
	put := srv.Last("PUT", "/mydb/doc1")
	if put.Query("rev") != "1-a" {
		t.Errorf("rev = %q, want the rev edit read", put.Query("rev"))
	}
	if string(put.Body) != `{"_id":"doc1","n":2}` {
		t.Errorf("body = %s", put.Body)
	}
}

func TestEditWritesNothingWhenTheEditorSavesNoChange(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb/doc1", 200, `{"_id":"doc1","_rev":"1-a","n":1}`)
	srv.JSON("PUT", "/mydb/doc1", 201, `{"ok":true,"id":"doc1","rev":"2-b"}`)
	editorScript(t, `exit 0`)
	s := connected(t, srv)
	s.SetPath("/mydb")
	res, err := invoke(t, Edit(), s, "doc1")
	if err != nil {
		t.Fatal(err)
	}
	if msg, ok := res.(Message); !ok || !strings.Contains(msg.Text, "No changes") {
		t.Errorf("result = %#v, want a no-changes message", res)
	}
	if srv.Last("PUT", "/mydb/doc1") != nil {
		t.Error("edit wrote an unchanged document")
	}
}

// conflictingPUT answers the first n PUTs with a 409 and every later one with
// a 201 naming okRev.
func conflictingPUT(srv *couchtest.Server, n int, okRev string) {
	var mu sync.Mutex
	var puts int
	srv.On("PUT", "/mydb/doc1", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		puts++
		conflict := puts <= n
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if conflict {
			w.WriteHeader(409)
			_, _ = w.Write([]byte(`{"error":"conflict","reason":"Document update conflict."}`))
			return
		}
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"ok":true,"id":"doc1","rev":"` + okRev + `"}`))
	})
}

func TestEditRetriesAfterAConflict(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb/doc1", 200, `{"_id":"doc1","_rev":"1-a","n":1}`)
	srv.On("HEAD", "/mydb/doc1", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `"2-someone-else"`)
		w.WriteHeader(200)
	})
	conflictingPUT(srv, 1, "3-c")
	runs := editorScript(t, `printf '{"_id":"doc1","_rev":"1-a","edited":"mine"}' > "$1"`)
	s := connected(t, srv)
	s.SetPath("/mydb")
	s.SetStdin(strings.NewReader("y\n"))
	s.Stdout = &bytes.Buffer{}
	s.Prefs.Interactive = true
	res, err := invoke(t, Edit(), s, "doc1")
	if err != nil {
		t.Fatal(err)
	}
	if msg, ok := res.(Message); !ok || !strings.Contains(msg.Text, "3-c") {
		t.Errorf("result = %#v, want the second write's rev", res)
	}
	retry := srv.Last("PUT", "/mydb/doc1")
	if got := retry.Query("rev"); got != "2-someone-else" {
		t.Errorf("retry rev = %q, want the reloaded revision", got)
	}
	// The point of the reload is to reapply the operator's work, not to throw
	// it away and write the server's copy back.
	if !strings.Contains(string(retry.Body), `"edited":"mine"`) {
		t.Errorf("retry body = %s, want the edited field to survive", retry.Body)
	}
	if strings.Contains(string(retry.Body), `"1-a"`) {
		t.Errorf("retry body = %s, still carries the stale _rev", retry.Body)
	}
	// The editor runs once: the retry reuses the buffer it produced instead of
	// reopening the editor on the server's copy.
	if n := runs(); n != 1 {
		t.Errorf("the editor ran %d times, want 1", n)
	}
}

func TestEditStopsRetryingAfterThreeConflicts(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb/doc1", 200, `{"_id":"doc1","_rev":"1-a","n":1}`)
	srv.On("HEAD", "/mydb/doc1", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `"9-busy"`)
		w.WriteHeader(200)
	})
	conflictingPUT(srv, 100, "never")
	editorScript(t, `printf '{"_id":"doc1","edited":"mine"}' > "$1"`)
	s := connected(t, srv)
	s.SetPath("/mydb")
	// --yes answers every reload prompt, so only the attempt cap can stop this.
	s.Prefs.Yes = true
	s.Stdout = &bytes.Buffer{}
	_, err := invoke(t, Edit(), s, "doc1")
	if err == nil {
		t.Fatal("edit reported success against a permanently conflicted document")
	}
	if e, ok := couch.AsError(err); !ok || e.Status != 409 {
		t.Fatalf("err = %#v, want the 409 to surface", err)
	}
	puts := 0
	for _, r := range srv.Requests() {
		if r.Method == "PUT" {
			puts++
		}
	}
	if puts != 3 {
		t.Errorf("edit made %d PUTs, want it to stop after 3", puts)
	}
}

func TestEditGivesUpWhenTheReloadIsDeclined(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb/doc1", 200, `{"_id":"doc1","_rev":"1-a","n":1}`)
	srv.JSON("PUT", "/mydb/doc1", 409, `{"error":"conflict","reason":"Document update conflict."}`)
	editorScript(t, `printf '{"_id":"doc1","n":2}' > "$1"`)
	s := connected(t, srv)
	s.SetPath("/mydb")
	s.SetStdin(strings.NewReader("n\n"))
	s.Stdout = &bytes.Buffer{}
	s.Prefs.Interactive = true
	_, err := invoke(t, Edit(), s, "doc1")
	// The operator's "n" is the answer that ends the command, so it is the
	// answer that names it: "Cancelled: nothing was changed.", not the 409
	// that prompted the question.
	if !errors.Is(err, ErrDeclined) {
		t.Fatalf("error = %v, want ErrDeclined", err)
	}
	if srv.Last("HEAD", "/mydb/doc1") != nil {
		t.Error("edit reloaded the revision even though the operator declined")
	}
}

func TestWithRevReplacesOnlyTheRevision(t *testing.T) {
	got, err := withRev([]byte(`{"_id":"doc1","_rev":"1-a","n":12345678901234567890,"z":1}`), "2-b")
	if err != nil {
		t.Fatal(err)
	}
	want := `{"_id":"doc1","_rev":"2-b","n":12345678901234567890,"z":1}`
	if string(got) != want {
		t.Errorf("withRev = %s\nwant      %s", got, want)
	}
}

func TestWithRevAddsNothingWhenTheBodyHasNoRev(t *testing.T) {
	src := []byte(`{"_id":"doc1","n":1}`)
	got, err := withRev(src, "2-b")
	if err != nil {
		t.Fatal(err)
	}
	// The query parameter carries the revision in this case; adding a _rev the
	// operator did not write would be a surprise.
	if string(got) != string(src) {
		t.Errorf("withRev = %s, want it unchanged", got)
	}
}

func TestWithRevIgnoresANestedRev(t *testing.T) {
	src := []byte(`{"_id":"doc1","meta":{"_rev":"keep-me"},"_rev":"1-a"}`)
	got, err := withRev(src, "2-b")
	if err != nil {
		t.Fatal(err)
	}
	want := `{"_id":"doc1","meta":{"_rev":"keep-me"},"_rev":"2-b"}`
	if string(got) != want {
		t.Errorf("withRev = %s\nwant      %s", got, want)
	}
}

func TestEditWithoutAnEditorIsAUsageError(t *testing.T) {
	srv := couchtest.New(t)
	s := connected(t, srv)
	s.SetPath("/mydb")
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	_, err := invoke(t, Edit(), s, "doc1")
	if _, ok := err.(*UsageError); !ok {
		t.Fatalf("edit = %#v, want a *UsageError", err)
	}
}

func TestIndentJSONPreservesKeyOrderAndBigNumbers(t *testing.T) {
	src := []byte(`{"n":12345678901234567890,"z":1,"a":2}`)
	pretty, err := indentJSON(src)
	if err != nil {
		t.Fatal(err)
	}
	// A map[string]any round trip would sort the keys and turn n into
	// 12345678901234567000. json.Indent must keep both intact.
	if !strings.Contains(string(pretty), "12345678901234567890") {
		t.Errorf("indentJSON lost numeric precision:\n%s", pretty)
	}
	wantOrder := []string{`"n"`, `"z"`, `"a"`}
	pos := -1
	for _, key := range wantOrder {
		i := strings.Index(string(pretty), key)
		if i <= pos {
			t.Fatalf("indentJSON reordered the keys:\n%s", pretty)
		}
		pos = i
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, pretty); err != nil {
		t.Fatal(err)
	}
	if compact.String() != string(src) {
		t.Errorf("round trip = %s, want %s", compact.String(), src)
	}
}

// TestRmInterruptedExitsSilently is the operator-visible half of #32: Ctrl-C
// at "Delete /db/doc?" leaves no verdict on the screen and exits 130, the way
// Ctrl-C does everywhere else in cdb.
func TestRmInterruptedExitsSilently(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("HEAD", "/mydb/doc1", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `"1-a"`)
		w.WriteHeader(200)
	})
	s := connected(t, srv)
	s.SetPath("/mydb")
	s.Stdout = &bytes.Buffer{}
	s.SetStdin(strings.NewReader("\n"))
	s.Prefs.Interactive = true

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.SetStdin(&cancelAtPrompt{cancel: cancel, rest: strings.NewReader("\n")})
	_, err := invokeContext(ctx, Rm(), s, "doc1")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	if out := s.Stdout.(*bytes.Buffer).String(); !strings.HasSuffix(out, "[y/N] ") {
		t.Errorf("stdout = %q, want nothing but the prompt itself", out)
	}
	if srv.Last("DELETE", "/mydb/doc1") != nil {
		t.Fatal("rm deleted after an interrupted prompt")
	}
}

// TestEditInterruptedAtTheReapplyPrompt is #32 at edit's second prompt. The
// conflict that raised the question is not the answer to it: Ctrl-C means the
// operator interrupted, so edit reports the interruption and exits 130 in
// silence rather than blaming a 409 the operator never saw the end of.
func TestEditInterruptedAtTheReapplyPrompt(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/mydb/doc1", 200, `{"_id":"doc1","_rev":"1-a","n":1}`)
	srv.JSON("PUT", "/mydb/doc1", 409, `{"error":"conflict","reason":"Document update conflict."}`)
	editorScript(t, `printf '{"_id":"doc1","n":2}' > "$1"`)
	s := connected(t, srv)
	s.SetPath("/mydb")
	s.Stdout = &bytes.Buffer{}
	s.Prefs.Interactive = true

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.SetStdin(&cancelAtPrompt{cancel: cancel, rest: strings.NewReader("\n")})
	_, err := invokeContext(ctx, Edit(), s, "doc1")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if strings.Contains(fmt.Sprint(err), "conflict") {
		t.Errorf("error = %v, want no mention of the conflict", err)
	}
	if !strings.HasSuffix(s.Stdout.(*bytes.Buffer).String(), "[y/N] ") {
		t.Errorf("stdout = %q, want it to end at the prompt", s.Stdout.(*bytes.Buffer).String())
	}
	if srv.Last("HEAD", "/mydb/doc1") != nil {
		t.Error("edit reloaded the revision after an interrupted prompt")
	}
}
