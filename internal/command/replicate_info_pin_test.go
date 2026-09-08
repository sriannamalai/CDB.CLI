package command

import (
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
)

// A pinning test, not an approval. "replications show" renders the scheduler's
// `info` object verbatim — it is the only field of a scheduler entry that is
// not redacted, filtered or reshaped on the way out.
//
// That is accepted for v1 because CouchDB writes `info` itself (documents
// written, revisions read, the crash reason) and every endpoint that could
// carry a credential goes through redactEndpoint first. The exposure that
// remains is a hand-written _replicator document whose own `info` member the
// scheduler echoes back. This test exists so that changing either side —
// starting to redact it, or starting to trust it further — is a deliberate
// edit with a failing test in front of it, rather than something nobody
// notices.
func TestReplicationsShowRendersSchedulerInfoVerbatim(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_scheduler/docs/_replicator/job1", 200,
		`{"database":"_replicator","doc_id":"job1","id":null,"source":"http://a/","target":"http://b/",`+
			`"state":"completed","node":"","error_count":0,"last_updated":"2026-09-08T00:00:00Z",`+
			`"info":{"docs_read":5,"docs_written":5,"doc_write_failures":0}}`)
	s := connected(t, srv)

	res, err := invoke(t, Replications(), s, "show", "job1")
	if err != nil {
		t.Fatal(err)
	}
	joined := showFields(t, res)
	if !strings.Contains(joined, `"docs_written":5`) {
		t.Errorf("rows %q do not carry the scheduler's info object", joined)
	}
	// The endpoints beside it are redacted, which is the contract this field
	// deliberately sits outside of.
	if strings.Contains(joined, "@") {
		t.Errorf("rows %q carry userinfo", joined)
	}
}
