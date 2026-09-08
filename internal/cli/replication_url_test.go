package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/command"
	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// One shot: --replication-url is a global flag, so it reaches "replicate"
// whether or not the auto-connect was the thing that read it.
func TestOneShotReplicationURLReachesTheReplicationDocument(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/_replicator", 201, `{"ok":true,"id":"job1","rev":"1-a"}`)
	withTempDeps(t, nil)

	var out, errOut bytes.Buffer
	s := session.New(strings.NewReader(""), &out, &errOut)
	code := Execute(context.Background(), command.Default(), s, BuildInfo{},
		[]string{"--url", srv.URL(), "--replication-url", "http://couchdb:5984", "replicate", "/src", "/dst"})
	if code != ExitOK {
		t.Fatalf("exit code = %d (stderr: %s)", code, errOut.String())
	}
	body := string(srv.Last("POST", "/_replicator").Body)
	if !strings.Contains(body, `"url":"http://couchdb:5984/src"`) {
		t.Errorf("_replicator body did not use the flag's base: %s", body)
	}
	if strings.Contains(body, srv.URL()) {
		t.Errorf("_replicator body still names the client URL: %s", body)
	}
}

// A value carrying credentials is refused, and the refusal never echoes them.
func TestOneShotReplicationURLWithUserinfoIsAUsageError(t *testing.T) {
	srv := couchtest.New(t)
	withTempDeps(t, nil)

	var out, errOut bytes.Buffer
	s := session.New(strings.NewReader(""), &out, &errOut)
	code := Execute(context.Background(), command.Default(), s, BuildInfo{},
		[]string{"--url", srv.URL(), "--replication-url", "http://admin:hunter2@couchdb:5984", "replicate", "/src", "/dst"})
	if code != ExitUsage {
		t.Errorf("exit code = %d, want the usage code %d (stderr: %s)", code, ExitUsage, errOut.String())
	}
	if strings.Contains(errOut.String()+out.String(), "hunter2") {
		t.Errorf("the error echoed the secret: %s%s", out.String(), errOut.String())
	}
	if srv.Last("POST", "/_replicator") != nil {
		t.Error("a rejected replication URL still wrote a _replicator document")
	}
}
