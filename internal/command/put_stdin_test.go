package command

import (
	"net/http"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
)

// Inside the shell, standard input is the terminal the operator is typing at.
// `put /db/doc` with no file drained it to EOF: the prompt vanished and the
// only way out was Ctrl-D. Reading the terminal has to be asked for.
func TestPutInTheShellRequiresAFileOrDash(t *testing.T) {
	srv := couchtest.New(t)
	s := connected(t, srv)
	s.SetPath("/mydb")
	s.SetStdin(strings.NewReader(`{"name":"bob"}`))
	s.Prefs.Interactive = true

	_, err := invoke(t, Put(), s, "doc1")
	if err == nil {
		t.Fatal("put with no file inside the shell returned no error")
	}
	ue, ok := err.(*UsageError)
	if !ok {
		t.Fatalf("err = %#v, want a *UsageError", err)
	}
	if !strings.Contains(ue.Reason, "needs a file, or - to read from standard input") {
		t.Errorf("reason = %q", ue.Reason)
	}
	if srv.Last("PUT", "/mydb/doc1") != nil {
		t.Error("put wrote the document anyway")
	}
}

// An explicit "-" is the operator asking for standard input, so it still reads
// it inside the shell — that is how a here-doc or a pasted body gets in.
func TestPutInTheShellReadsStdinWithADash(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("HEAD", "/mydb/doc1", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(404) })
	srv.JSON("PUT", "/mydb/doc1", 201, `{"ok":true,"id":"doc1","rev":"1-a"}`)
	s := connected(t, srv)
	s.SetPath("/mydb")
	s.SetStdin(strings.NewReader(`{"name":"bob"}`))
	s.Prefs.Interactive = true

	if _, err := invoke(t, Put(), s, "doc1", "-"); err != nil {
		t.Fatal(err)
	}
	put := srv.Last("PUT", "/mydb/doc1")
	if put == nil || string(put.Body) != `{"name":"bob"}` {
		t.Errorf("put = %#v, want the body from standard input", put)
	}
}

// One-shot use is unchanged: `cdb put /db/doc < file.json` has no terminal to
// hang on, and the pipe is the whole point.
func TestPutOneShotStillReadsPipedStdin(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("HEAD", "/mydb/doc1", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(404) })
	srv.JSON("PUT", "/mydb/doc1", 201, `{"ok":true,"id":"doc1","rev":"1-a"}`)
	s := connected(t, srv)
	s.SetPath("/mydb")
	s.SetStdin(strings.NewReader(`{"name":"bob"}`))
	s.Prefs.Interactive = false

	if _, err := invoke(t, Put(), s, "doc1"); err != nil {
		t.Fatal(err)
	}
	if put := srv.Last("PUT", "/mydb/doc1"); put == nil || string(put.Body) != `{"name":"bob"}` {
		t.Errorf("put = %#v, want the body from standard input", put)
	}
}
