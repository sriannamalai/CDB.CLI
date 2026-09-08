package shell

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/command"
	"github.com/sriannamalai/CDB.CLI/internal/couch"
	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// replicationShell is testShell with the stub server handed back, so a test
// can read the _replicator body the line produced.
func replicationShell(t *testing.T, out *bytes.Buffer) (*Shell, *session.Session, *couchtest.Server) {
	t.Helper()
	srv := couchtest.New(t)
	srv.JSON("POST", "/_replicator", 201, `{"ok":true,"id":"job1","rev":"1-a"}`)
	c, err := couch.New(couch.Config{URL: srv.URL(), Auth: couch.AuthNone})
	if err != nil {
		t.Fatal(err)
	}
	s := session.New(strings.NewReader(""), out, out)
	s.Attach(c, "test")
	t.Cleanup(func() { _ = s.Detach() })
	sh, err := New(command.Default(), s, Config{HistoryFile: "", Keymap: "emacs"})
	if err != nil {
		t.Fatal(err)
	}
	return sh, s, srv
}

// The shell connects once, at startup. A --replication-url typed on a later
// line therefore never reaches openProfile, so "replicate" has to read the
// per-line preference itself or the flag is silently dropped.
func TestRunLineHonoursTheReplicationURLOnAnAlreadyConnectedSession(t *testing.T) {
	var out bytes.Buffer
	sh, s, srv := replicationShell(t, &out)

	if err := sh.RunLine(context.Background(), "replicate /src /dst --replication-url http://couchdb:5984"); err != nil {
		t.Fatalf("replicate: %v (output: %s)", err, out.String())
	}
	body := string(srv.Last("POST", "/_replicator").Body)
	if !strings.Contains(body, `"url":"http://couchdb:5984/src"`) {
		t.Errorf("_replicator body did not use the flag's base: %s", body)
	}
	if strings.Contains(body, srv.URL()) {
		t.Errorf("_replicator body still names the client URL: %s", body)
	}
	// Like --yes and --anonymous, it applies to that line only.
	if s.Prefs.ReplicationURL != "" {
		t.Errorf("Prefs.ReplicationURL = %q after the line, want it restored", s.Prefs.ReplicationURL)
	}
}

// "cp" between two databases starts the same replication, so it reads the same
// preference.
func TestRunLineHonoursTheReplicationURLForCopyDatabase(t *testing.T) {
	var out bytes.Buffer
	sh, _, srv := replicationShell(t, &out)

	if err := sh.RunLine(context.Background(), "cp /src /dst --replication-url http://couchdb:5984"); err != nil {
		t.Fatalf("cp: %v (output: %s)", err, out.String())
	}
	body := string(srv.Last("POST", "/_replicator").Body)
	if !strings.Contains(body, `"url":"http://couchdb:5984/src"`) {
		t.Errorf("_replicator body did not use the flag's base: %s", body)
	}
}

// A malformed or credential-bearing value is a usage error, not a silently
// accepted endpoint.
func TestRunLineRejectsABadReplicationURL(t *testing.T) {
	var out bytes.Buffer
	sh, _, srv := replicationShell(t, &out)

	for _, line := range []string{
		"replicate /src /dst --replication-url notaurl",
		"replicate /src /dst --replication-url http://admin:hunter2@couchdb:5984",
		"replicate /src /dst --replication-url http://couchdb:5984/?x=1",
	} {
		err := sh.RunLine(context.Background(), line)
		if err == nil {
			t.Errorf("%q was accepted", line)
			continue
		}
		if strings.Contains(err.Error(), "hunter2") {
			t.Errorf("the error echoed the secret: %v", err)
		}
	}
	if srv.Last("POST", "/_replicator") != nil {
		t.Error("a rejected replication URL still wrote a _replicator document")
	}
}
