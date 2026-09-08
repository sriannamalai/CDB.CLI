package couch

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

// net/http decorates a transport failure with the method and the whole URL.
// trimPrefixes strips that decoration for every verb cdb uses — except COPY,
// which "cp" issues, so a failed copy rendered as
// `Copy "http://admin:xxxxx@host/db/doc": dial tcp ...`: the user name and the
// full URL in an operator-facing sentence.
func TestTrimPrefixesStripsTheCopyVerb(t *testing.T) {
	err := &url.Error{
		Op:  "Copy",
		URL: "http://admin:xxxxx@localhost:5984/mydb/doc1",
		Err: errors.New("dial tcp 127.0.0.1:5984: connect: connection refused"),
	}
	got := trimPrefixes(err.Error())
	if strings.Contains(got, "Copy") || strings.Contains(got, "admin") || strings.Contains(got, "localhost:5984/mydb") {
		t.Errorf("trimPrefixes = %q, want the method and URL decoration removed", got)
	}
	if !strings.Contains(got, "connection refused") {
		t.Errorf("trimPrefixes = %q, want the underlying reason kept", got)
	}
}
