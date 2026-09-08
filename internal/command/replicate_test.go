package command

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// invokeContext is invoke with a caller-supplied context, for the commands
// that run until they are interrupted.
func invokeContext(ctx context.Context, c Command, s *session.Session, argv ...string) (Result, error) {
	fs := NewRegistry().NewFlagSet(c)
	if err := fs.Parse(argv); err != nil {
		return nil, err
	}
	if err := c.CheckArgsErr(fs.Args()); err != nil {
		return nil, err
	}
	return c.Run(ctx, s, Invocation{Args: fs.Args(), Flags: fs, Stdin: s.Stdin(), Stdout: s.Stdout, Stderr: s.Stderr})
}

func TestReplicateCreatesAJob(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/_replicator", 201, `{"ok":true,"id":"job1","rev":"1-a"}`)
	s := connected(t, srv)
	res, err := invoke(t, Replicate(), s, "/src", "https://other.example.com/dst", "--continuous", "--create-target", "--id", "job1")
	if err != nil {
		t.Fatal(err)
	}
	msg, ok := res.(Message)
	if !ok || !strings.Contains(msg.Text, "job1") {
		t.Errorf("result = %#v", res)
	}
	body := string(srv.Last("POST", "/_replicator").Body)
	if !strings.Contains(body, `"url":"`+s.Client.URL()+`/src"`) {
		t.Errorf("body %s did not expand the local source path", body)
	}
	if !strings.Contains(body, `"url":"https://other.example.com/dst"`) {
		t.Errorf("body %s lost the remote target", body)
	}
	if !strings.Contains(body, `"continuous":true`) || !strings.Contains(body, `"create_target":true`) {
		t.Errorf("body %s lost the flags", body)
	}
}

// TestReplicateKeepsARemotePasswordOutOfTheResult: a target URL may carry
// userinfo. It has to reach CouchDB, in the per-endpoint auth object, but it
// must never reach the operator's screen.
func TestReplicateKeepsARemotePasswordOutOfTheResult(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/_replicator", 201, `{"ok":true,"id":"job1","rev":"1-a"}`)
	s := connected(t, srv)
	var out bytes.Buffer
	s.Stdout = &out
	res, err := invoke(t, Replicate(), s, "/src", "https://admin:s3cret@other.example.com/dst")
	if err != nil {
		t.Fatal(err)
	}
	msg, ok := res.(Message)
	if !ok {
		t.Fatalf("result is %T, want Message", res)
	}
	if strings.Contains(msg.Text, "s3cret") {
		t.Errorf("result message leaks the password: %s", msg.Text)
	}
	if strings.Contains(out.String(), "s3cret") {
		t.Errorf("stdout leaks the password: %s", out.String())
	}
	body := string(srv.Last("POST", "/_replicator").Body)
	if !strings.Contains(body, `"basic":{"password":"s3cret","username":"admin"}`) {
		t.Errorf("body %s is missing the per-endpoint credentials", body)
	}
	if strings.Contains(body, "admin:s3cret@") {
		t.Errorf("body %s still carries userinfo in the URL", body)
	}
}

func TestReplicateRejectsANonDatabasePath(t *testing.T) {
	srv := couchtest.New(t)
	s := connected(t, srv)
	_, err := invoke(t, Replicate(), s, "/src/doc1", "/dst")
	if _, ok := err.(*UsageError); !ok {
		t.Fatalf("err = %#v, want *UsageError", err)
	}
}

func TestReplicationsList(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_scheduler/docs", 200, `{"total_rows":1,"offset":0,"docs":[
		{"database":"_replicator","doc_id":"job1","id":"abc","source":"http://a/","target":"http://b/","state":"running","node":"n1","error_count":0,"last_updated":"2026-09-08T00:00:00Z","info":{"docs_written":5}}]}`)
	s := connected(t, srv)
	res, err := invoke(t, Replications(), s)
	if err != nil {
		t.Fatal(err)
	}
	rows, ok := res.(Rows)
	if !ok {
		t.Fatalf("result is %T, want Rows", res)
	}
	if len(rows.Items) != 1 || rows.Items[0].Cells[0] != "job1" {
		t.Errorf("rows = %+v", rows.Items)
	}
}

func TestReplicationsShow(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_scheduler/docs/_replicator/job1", 200, `{"database":"_replicator","doc_id":"job1","id":"abc","source":"http://a/","target":"http://b/","state":"running","node":"n1","error_count":0,"last_updated":"2026-09-08T00:00:00Z","info":{"docs_written":5}}`)
	s := connected(t, srv)
	res, err := invoke(t, Replications(), s, "show", "job1")
	if err != nil {
		t.Fatal(err)
	}
	rows, ok := res.(Rows)
	if !ok {
		t.Fatalf("result is %T, want Rows", res)
	}
	joined := ""
	for _, r := range rows.Items {
		joined += r.Cells[0] + "=" + r.Cells[1] + ";"
	}
	if !strings.Contains(joined, "state=running") {
		t.Errorf("rows %q are missing the state", joined)
	}
}

// TestReplicationsRedactEndpointCredentials: a _replicator document written by
// cdb carries an auth object, and one written by hand may carry userinfo in
// the URL. Neither may reach a rendered cell or a Row.JSON payload.
func TestReplicationsRedactEndpointCredentials(t *testing.T) {
	const entry = `{"database":"_replicator","doc_id":"job1","id":"abc",
		"source":{"url":"http://a.example.com/src","auth":{"basic":{"username":"admin","password":"s3cret"}}},
		"target":"http://admin:s3cret@b.example.com/dst/",
		"state":"running","node":"n1","error_count":0,"last_updated":"2026-09-08T00:00:00Z"}`
	for _, tc := range []struct {
		name  string
		argv  []string
		route string
		body  string
	}{
		{"list", nil, "/_scheduler/docs", `{"docs":[` + entry + `]}`},
		{"show", []string{"show", "job1"}, "/_scheduler/docs/_replicator/job1", entry},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := couchtest.New(t)
			srv.JSON("GET", tc.route, 200, tc.body)
			s := connected(t, srv)
			res, err := invoke(t, Replications(), s, tc.argv...)
			if err != nil {
				t.Fatal(err)
			}
			rows, ok := res.(Rows)
			if !ok {
				t.Fatalf("result is %T, want Rows", res)
			}
			var rendered strings.Builder
			for _, r := range rows.Items {
				rendered.WriteString(strings.Join(r.Cells, " "))
				rendered.WriteString(" ")
				rendered.Write(r.JSON)
				rendered.WriteString("\n")
			}
			for _, leak := range []string{"s3cret", "auth", "admin"} {
				if strings.Contains(rendered.String(), leak) {
					t.Errorf("rendered rows leak %q:\n%s", leak, rendered.String())
				}
			}
			if !strings.Contains(rendered.String(), "http://b.example.com/dst/") {
				t.Errorf("rendered rows lost the target URL:\n%s", rendered.String())
			}
		})
	}
}

func TestReplicationsCancelConfirms(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("HEAD", "/_replicator/job1", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `"1-a"`)
		w.WriteHeader(200)
	})
	srv.JSON("DELETE", "/_replicator/job1", 200, `{"ok":true,"id":"job1","rev":"2-b"}`)
	s := connected(t, srv)
	s.Prefs.Interactive = true
	s.SetStdin(strings.NewReader("n\n"))
	if _, err := invoke(t, Replications(), s, "cancel", "job1"); err == nil {
		t.Fatal("cancel proceeded without confirmation")
	}
	if srv.Last("DELETE", "/_replicator/job1") != nil {
		t.Fatal("cancel deleted after the operator declined")
	}
	s.SetStdin(strings.NewReader("y\n"))
	if _, err := invoke(t, Replications(), s, "cancel", "job1"); err != nil {
		t.Fatal(err)
	}
	if srv.Last("DELETE", "/_replicator/job1") == nil {
		t.Fatal("cancel did not delete after confirmation")
	}
	if got := srv.Last("DELETE", "/_replicator/job1").Query("rev"); got != "1-a" {
		t.Errorf("DELETE rev = %q, want 1-a", got)
	}
}

func TestReplicationsCancelWithoutATerminalIsAUsageError(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("HEAD", "/_replicator/job1", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `"1-a"`)
		w.WriteHeader(200)
	})
	srv.JSON("DELETE", "/_replicator/job1", 200, `{"ok":true,"id":"job1","rev":"2-b"}`)
	s := connected(t, srv)
	s.Prefs.Interactive = false
	_, err := invoke(t, Replications(), s, "cancel", "job1")
	if _, ok := err.(*UsageError); !ok {
		t.Fatalf("err = %#v, want *UsageError", err)
	}
	if srv.Last("DELETE", "/_replicator/job1") != nil {
		t.Fatal("cancel deleted without a confirmation")
	}
}

func TestReplicationsCancelWithYesSkipsThePrompt(t *testing.T) {
	srv := couchtest.New(t)
	srv.On("HEAD", "/_replicator/job1", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `"1-a"`)
		w.WriteHeader(200)
	})
	srv.JSON("DELETE", "/_replicator/job1", 200, `{"ok":true,"id":"job1","rev":"2-b"}`)
	s := connected(t, srv)
	if _, err := invoke(t, Replications(), s, "cancel", "job1", "--yes"); err != nil {
		t.Fatal(err)
	}
	if srv.Last("DELETE", "/_replicator/job1") == nil {
		t.Fatal("cancel --yes did not delete")
	}
}

func TestReplicationsRejectsAnUnknownSubcommand(t *testing.T) {
	srv := couchtest.New(t)
	s := connected(t, srv)
	_, err := invoke(t, Replications(), s, "frobnicate")
	if _, ok := err.(*UsageError); !ok {
		t.Fatalf("err = %#v, want *UsageError", err)
	}
}

func TestReplicationsIsNotMarkedDestructive(t *testing.T) {
	// Only the cancel branch is dangerous, and it calls Confirm itself.
	// Marking the whole command would flag "replications list" as destructive.
	if Replications().Destructive {
		t.Error("replications is marked Destructive; list and show are read-only")
	}
}

func TestReplicationsListJSONUsesLowerCaseKeys(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_scheduler/docs", 200, `{"docs":[{"doc_id":"job1","id":"abc","source":"http://a/src","target":"http://b/dst","state":"running","node":"node1","error_count":0,"last_updated":"2026-09-08T00:00:00Z"}]}`)
	s := connected(t, srv)
	res, err := invoke(t, Replications(), s)
	if err != nil {
		t.Fatal(err)
	}
	rows := res.(Rows)
	if len(rows.Items) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows.Items))
	}
	body := string(rows.Items[0].JSON)
	for _, want := range []string{`"doc_id":"job1"`, `"state":"running"`} {
		if !strings.Contains(body, want) {
			t.Errorf("row JSON = %s, want %s", body, want)
		}
	}
	if strings.Contains(body, `"DocID"`) || strings.Contains(body, `"State"`) {
		t.Errorf("row JSON = %s, want lower-case keys", body)
	}
}

func TestReplicationsWatchRefusesWithoutATerminal(t *testing.T) {
	srv := couchtest.New(t)
	s := connected(t, srv)
	s.Prefs.Interactive = false
	_, err := invoke(t, Replications(), s, "--watch")
	if _, ok := err.(*UsageError); !ok {
		t.Fatalf("err = %#v, want *UsageError", err)
	}
}

// drawWatcher records what --watch printed and signals its first frame.
type drawWatcher struct {
	buf   bytes.Buffer
	once  sync.Once
	drawn chan struct{}
}

func (w *drawWatcher) Write(p []byte) (int, error) {
	n, err := w.buf.Write(p)
	w.once.Do(func() { close(w.drawn) })
	return n, err
}

func TestReplicationsWatchRedrawsUntilCancelled(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_scheduler/docs", 200, `{"docs":[
		{"database":"_replicator","doc_id":"job1","id":"abc","source":"http://a/src","target":"http://b/dst","state":"running","node":"n1","error_count":0,"last_updated":"2026-09-08T00:00:00Z"}]}`)
	s := connected(t, srv)
	s.Prefs.Interactive = true
	w := &drawWatcher{drawn: make(chan struct{})}
	s.Stdout = w

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type outcome struct {
		res Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := invokeContext(ctx, Replications(), s, "--watch")
		done <- outcome{res, err}
	}()
	select {
	case <-w.drawn:
	case <-time.After(10 * time.Second):
		t.Fatal("--watch never drew a frame")
	}
	cancel()
	select {
	case got := <-done:
		// Interruption is exit code 130, which the front-ends derive from
		// context.Canceled.
		if !errors.Is(got.err, context.Canceled) {
			t.Errorf("--watch returned %v, want context.Canceled", got.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("--watch did not stop when the context was cancelled")
	}
	if !strings.Contains(w.buf.String(), "job1") {
		t.Errorf("--watch drew %q, which does not name the job", w.buf.String())
	}
}

// TestReplicateHelpDocumentsUnreachableEndpoints: a same-server replication
// hands CouchDB the URL cdb itself connected with. When the server cannot
// reach that address (a remapped container port, an SSH tunnel), the job is
// accepted and then fails, which is baffling unless the help says so.
func TestReplicateHelpDocumentsUnreachableEndpoints(t *testing.T) {
	res := runHelp(t, "replicate")
	msg, ok := res.(Message)
	if !ok {
		t.Fatalf("help replicate returned %T, want Message", res)
	}
	if !strings.Contains(msg.Text, "econnrefused") {
		t.Errorf("help replicate does not document the unreachable-endpoint limitation:\n%s", msg.Text)
	}
}

func TestDefaultRegistersTheReplicationCommands(t *testing.T) {
	reg := Default()
	for _, name := range []string{"replicate", "replications"} {
		if _, ok := reg.Lookup(name); !ok {
			t.Errorf("the default registry has no %q command", name)
		}
	}
}
