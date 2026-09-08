package command

import "strings"

// checkFilter holds a --filter value to CouchDB's own shape: the name of a
// filter function inside a design document, written "<design>/<name>" with
// both halves present. tail sends it to _changes and replicate writes it into
// the replication document; both endpoints answer anything else with a 400, so
// without this a mistyped flag exits 1 with the server's words rather than 2
// with cdb's. An empty value means no filter at all and is not checked.
//
// cmd names the command in the message, because the two commands raise the
// same complaint about the same flag and the operator has to know which one is
// speaking.
func checkFilter(cmd, filter string) error {
	if filter == "" {
		return nil
	}
	ddoc, name, ok := strings.Cut(filter, "/")
	if !ok || ddoc == "" || name == "" || strings.Contains(name, "/") {
		return Usagef(cmd, "--filter takes a design document and a filter name, as \"app/by_type\".")
	}
	return nil
}
