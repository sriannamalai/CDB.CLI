package command

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// invokeContext parses argv for a command and runs it under ctx, for the
// commands that run until they are interrupted. It mirrors what the two
// front-ends do, including copying the shared --yes and --verbose flags into
// the session preferences; without that, "--yes" would never reach Confirm.
// invoke is this with a background context.
func invokeContext(ctx context.Context, c Command, s *session.Session, argv ...string) (Result, error) {
	fs := NewRegistry().NewFlagSet(c)
	if err := fs.Parse(argv); err != nil {
		return nil, err
	}
	if err := c.CheckArgsErr(fs.Args()); err != nil {
		return nil, err
	}
	if v, err := fs.GetBool("yes"); err == nil && v {
		s.Prefs.Yes = true
	}
	if v, err := fs.GetBool("verbose"); err == nil && v {
		s.Prefs.Verbose = true
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
// userinfo. It has to reach CouchDB, in the per-endpoint Authorization header,
// but it must never reach the operator's screen.
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
	// base64("admin:s3cret"); CouchDB 3.0 and 3.1 ignore the "auth" object.
	if !strings.Contains(body, `"headers":{"Authorization":"Basic YWRtaW46czNjcmV0"}`) {
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

// The same shapes tail refuses, refused here: CouchDB answers a filter that is
// not "<design>/<name>" with a 400, and a mistyped flag reported as a server
// error exits 1 with the server's words instead of 2 with cdb's.
func TestReplicateRejectsAMalformedFilter(t *testing.T) {
	for _, filter := range []string{"byname", "app/", "/by_type", "app/by_type/extra"} {
		t.Run(filter, func(t *testing.T) {
			srv := couchtest.New(t)
			srv.JSON("POST", "/_replicator", 201, `{"ok":true,"id":"job1","rev":"1-a"}`)
			s := connected(t, srv)
			_, err := invoke(t, Replicate(), s, "/src", "/dst", "--filter", filter)
			var ue *UsageError
			if !errors.As(err, &ue) {
				t.Fatalf("err = %v, want a UsageError", err)
			}
			if want := `--filter takes a design document and a filter name, as "app/by_type".`; !strings.Contains(ue.Error(), want) {
				t.Errorf("message = %q, want it to contain %q", ue.Error(), want)
			}
			if ue.Command != "replicate" {
				t.Errorf("Command = %q, want replicate", ue.Command)
			}
			if srv.Last("POST", "/_replicator") != nil {
				t.Error("the replication document was written despite the bad filter")
			}
		})
	}
}

func TestReplicatePassesAWellFormedFilterThrough(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/_replicator", 201, `{"ok":true,"id":"job1","rev":"1-a"}`)
	s := connected(t, srv)
	if _, err := invoke(t, Replicate(), s, "/src", "/dst", "--filter", "app/by_type"); err != nil {
		t.Fatal(err)
	}
	req := srv.Last("POST", "/_replicator")
	if req == nil || !strings.Contains(string(req.Body), `"filter":"app/by_type"`) {
		t.Errorf("replication document = %s", req.Body)
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

// showFields flattens a "replications show" result into "field=value;" pairs.
func showFields(t *testing.T, res Result) string {
	t.Helper()
	rows, ok := res.(Rows)
	if !ok {
		t.Fatalf("result is %T, want Rows", res)
	}
	joined := ""
	for _, r := range rows.Items {
		joined += r.Cells[0] + "=" + r.Cells[1] + ";"
	}
	return joined
}

func TestReplicationsShow(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_scheduler/docs/_replicator/job1", 200, `{"database":"_replicator","doc_id":"job1","id":"abc","source":"http://a/","target":"http://b/","state":"running","node":"n1","error_count":0,"last_updated":"2026-09-08T00:00:00Z","info":{"docs_written":5}}`)
	s := connected(t, srv)
	res, err := invoke(t, Replications(), s, "show", "job1")
	if err != nil {
		t.Fatal(err)
	}
	joined := showFields(t, res)
	if !strings.Contains(joined, "state=running") {
		t.Errorf("rows %q are missing the state", joined)
	}
}

// TestReplicationsShowJoinsTheSchedulerJob: _scheduler/docs describes the
// document, _scheduler/jobs the process running it. Only the job carries the
// history, the pid and the start time, so show reads both.
func TestReplicationsShowJoinsTheSchedulerJob(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_scheduler/docs/_replicator/job1", 200, `{"database":"_replicator","doc_id":"job1","id":"abc+continuous","source":"http://a/","target":"http://b/","state":"running","node":"","error_count":0,"last_updated":"2026-09-08T00:00:00Z","info":{"docs_written":5}}`)
	srv.JSON("GET", "/_scheduler/jobs/abc+continuous", 200, `{"database":"_replicator","id":"abc+continuous","pid":"<0.383018.0>","source":"http://a/","target":"http://b/","doc_id":"job1","node":"nonode@nohost","start_time":"2026-09-08T05:55:59Z",
		"history":[{"timestamp":"2026-09-08T05:55:59Z","type":"started"},{"timestamp":"2026-09-08T05:55:58Z","type":"crashed","reason":"econnrefused"}]}`)
	s := connected(t, srv)
	res, err := invoke(t, Replications(), s, "show", "job1")
	if err != nil {
		t.Fatal(err)
	}
	joined := showFields(t, res)
	for _, want := range []string{
		"start time=2026-09-08T05:55:59Z;",
		"pid=<0.383018.0>;",
		"history=2026-09-08T05:55:59Z started;",
		"history=2026-09-08T05:55:58Z crashed: econnrefused;",
		// The document entry has no node while the job is what holds one.
		"node=nonode@nohost;",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("rows %q are missing %q", joined, want)
		}
	}
	for _, item := range rowsOf(t, res) {
		if len(item.JSON) == 0 {
			t.Errorf("row %v has no JSON", item.Cells)
		}
	}
}

// TestReplicationsShowWithoutARunningJob: a completed replication keeps its
// _scheduler/docs entry but has no job. That is the normal end state, not a
// failure, so show prints the document alone.
func TestReplicationsShowWithoutARunningJob(t *testing.T) {
	srv := couchtest.New(t)
	// A finished replication reports a null job id; the stub answers 404 for
	// the jobs route either way.
	srv.JSON("GET", "/_scheduler/docs/_replicator/job1", 200, `{"database":"_replicator","doc_id":"job1","id":null,"source":"http://a/","target":"http://b/","state":"completed","node":"","error_count":0,"last_updated":"2026-09-08T00:00:00Z","info":{"docs_written":5}}`)
	s := connected(t, srv)
	res, err := invoke(t, Replications(), s, "show", "job1")
	if err != nil {
		t.Fatal(err)
	}
	joined := showFields(t, res)
	if !strings.Contains(joined, "state=completed;") {
		t.Errorf("rows %q are missing the state", joined)
	}
	if strings.Contains(joined, "history=") || strings.Contains(joined, "pid=") {
		t.Errorf("rows %q invented job details for a replication with no job", joined)
	}
	for _, r := range srv.Requests() {
		if strings.HasPrefix(r.Path, "/_scheduler/jobs") {
			t.Errorf("show requested %s for a replication with no job id", r.Path)
		}
	}
}

// TestReplicationsShowCapsTheHistory: CouchDB keeps dozens of history events,
// newest first. A status table shows the newest few.
func TestReplicationsShowCapsTheHistory(t *testing.T) {
	var events []string
	for i := 0; i < historyEvents+3; i++ {
		events = append(events, fmt.Sprintf(`{"timestamp":"2026-09-08T00:00:%02dZ","type":"started"}`, i))
	}
	srv := couchtest.New(t)
	srv.JSON("GET", "/_scheduler/docs/_replicator/job1", 200, `{"doc_id":"job1","id":"abc","source":"http://a/","target":"http://b/","state":"running","error_count":0,"last_updated":"2026-09-08T00:00:00Z"}`)
	srv.JSON("GET", "/_scheduler/jobs/abc", 200, `{"id":"abc","doc_id":"job1","source":"http://a/","target":"http://b/","node":"n1","pid":"<0.1.0>","start_time":"2026-09-08T00:00:00Z","history":[`+strings.Join(events, ",")+`]}`)
	s := connected(t, srv)
	res, err := invoke(t, Replications(), s, "show", "job1")
	if err != nil {
		t.Fatal(err)
	}
	var got int
	for _, item := range rowsOf(t, res) {
		if item.Cells[0] == "history" {
			got++
		}
	}
	if got != historyEvents {
		t.Errorf("show printed %d history rows, want %d", got, historyEvents)
	}
	// Newest first, so the first event of the answer survives the cap.
	if !strings.Contains(showFields(t, res), "history=2026-09-08T00:00:00Z started;") {
		t.Errorf("show dropped the newest history event: %s", showFields(t, res))
	}
}

// rowsOf asserts a Result is Rows and returns its items.
func rowsOf(t *testing.T, res Result) []Row {
	t.Helper()
	rows, ok := res.(Rows)
	if !ok {
		t.Fatalf("result is %T, want Rows", res)
	}
	return rows.Items
}

// TestReplicationsRedactEndpointCredentials: a _replicator document written by
// cdb carries an auth object, and one written by hand may carry userinfo in
// the URL. The scheduler repeats both on its job entry. None of it may reach a
// rendered cell or a Row.JSON payload, on either subcommand.
func TestReplicationsRedactEndpointCredentials(t *testing.T) {
	const entry = `{"database":"_replicator","doc_id":"job1","id":"abc",
		"source":{"url":"http://a.example.com/src","auth":{"basic":{"username":"admin","password":"s3cret"}}},
		"target":"http://admin:s3cret@b.example.com/dst/",
		"state":"running","node":"n1","error_count":0,"last_updated":"2026-09-08T00:00:00Z"}`
	// The job entry carries the same credentials, and show renders its
	// endpoints in preference to the document's.
	const job = `{"database":"_replicator","id":"abc","doc_id":"job1","pid":"<0.1.0>","node":"n1","start_time":"2026-09-08T00:00:00Z",
		"source":{"url":"http://a.example.com/src","auth":{"basic":{"username":"admin","password":"s3cret"}}},
		"target":"http://admin:s3cret@b.example.com/dst/",
		"history":[{"timestamp":"2026-09-08T00:00:00Z","type":"started"}]}`
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
			srv.JSON("GET", "/_scheduler/jobs/abc", 200, job)
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

// TestReplicationsShowRedactsEndpointHeaders pins the one thing "replications
// show" must never do. Since 1.2 cdb writes replication credentials as an
// Authorization header on the endpoint object, so a server that echoes the
// stored document back verbatim — rather than redacting it the way CouchDB
// 3.5.2 does — would otherwise put a Basic credential on the screen.
func TestReplicationsShowRedactsEndpointHeaders(t *testing.T) {
	const secret = "YWRtaW46czNjcmV0"
	srv := couchtest.New(t)
	srv.JSON("GET", "/_scheduler/docs/_replicator/job1", 200, `{
		"database":"_replicator","doc_id":"job1","id":"abc","state":"completed",
		"source":{"url":"http://localhost:5984/src","headers":{"Authorization":"Basic `+secret+`"}},
		"target":{"url":"http://localhost:5984/dst","auth":{"basic":{"username":"admin","password":"s3cret"}}},
		"node":"n1","error_count":0,"last_updated":"2026-09-09T00:00:00Z"}`)
	srv.JSON("GET", "/_scheduler/jobs/abc", 404, `{"error":"not_found","reason":"missing"}`)

	s := connected(t, srv)
	res, err := invoke(t, Replications(), s, "show", "job1")
	if err != nil {
		t.Fatal(err)
	}

	rows, ok := res.(Rows)
	if !ok {
		t.Fatalf("got %T, want Rows", res)
	}
	for _, banned := range []string{secret, "s3cret", "Authorization", "Basic ", "\"auth\""} {
		for _, item := range rows.Items {
			for _, cell := range item.Cells {
				if strings.Contains(cell, banned) {
					t.Errorf("rendered cell %q contains %q", cell, banned)
				}
			}
			if strings.Contains(string(item.JSON), banned) {
				t.Errorf("row JSON %s contains %q", item.JSON, banned)
			}
		}
	}
	// And the URLs still arrive, so the test cannot pass by rendering nothing.
	var source string
	for _, item := range rows.Items {
		if len(item.Cells) == 2 && item.Cells[0] == "source" {
			source = item.Cells[1]
		}
	}
	if source != "http://localhost:5984/src" {
		t.Errorf("source = %q, want %q", source, "http://localhost:5984/src")
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
