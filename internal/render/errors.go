package render

import (
	"fmt"
	"strings"

	"github.com/sriannamalai/CDB.CLI/internal/couch"
)

// jwtRejectedSentence is the literal command.dial synthesizes for a bearer
// token a pre-3.1 server silently ignores (there is no JWT handler to give a
// real reason). plainSentence below builds the same literal for a genuine
// server 401 with Auth == AuthJWT.
const jwtRejectedSentence = "The server rejected the token."

// ErrorMessage renders an error as a plain sentence. With verbose set, the raw
// status, error name and server reason are appended — unless that reason is
// just the synthetic pre-3.1 sentence above, which plainSentence already
// printed; repeating it in brackets adds nothing.
func ErrorMessage(err error, verbose bool) string {
	if err == nil {
		return ""
	}
	ce, ok := couch.AsError(err)
	if !ok {
		return err.Error()
	}
	msg := plainSentence(ce)
	if verbose && !(ce.Auth == couch.AuthJWT && ce.Reason == jwtRejectedSentence) {
		msg += fmt.Sprintf(" [status %d %s: %s]", ce.Status, ce.Name, ce.Reason)
	}
	return msg
}

// plainSentence turns a CouchDB failure into prose a non-technical operator can
// act on.
func plainSentence(e *couch.Error) string {
	kind, name, db := describeTarget(e.Target)
	switch {
	case e.Status == couch.StatusUnreachable && e.Name == "connection_refused":
		return fmt.Sprintf("Could not reach %s. Is CouchDB running?", hostFromTarget(e.Target))
	case e.Status == couch.StatusUnreachable && e.Name == "no_such_host":
		return fmt.Sprintf("Could not find the server %s. Check the URL in the profile.", hostFromTarget(e.Target))
	case e.Status == couch.StatusUnreachable && e.Name == "tls":
		return fmt.Sprintf("The TLS connection to %s failed: %s", hostFromTarget(e.Target), e.Reason)
	case e.Status == 401 && e.Auth == couch.AuthNone:
		// --anonymous, or a profile with auth = "none", sends no credentials
		// at all. Telling the operator to check the password is advice about a
		// password that does not exist.
		return fmt.Sprintf("The server requires credentials for %s %s. Connect with a username and password, or set CDB_USER and CDB_PASSWORD.", e.Op, e.Target)
	case e.Status == 401 && e.Auth == couch.AuthJWT:
		// A bearer token the server did not accept is not a password problem,
		// and "check the password" is advice about a password that does not
		// exist. CouchDB below 3.1 has no JWT handler at all, so the token is
		// ignored and the request is handled as anonymous; Hint carries that.
		msg := jwtRejectedSentence
		if e.Hint != "" {
			msg += " " + e.Hint
		}
		return msg
	case e.Status == 401:
		user, host := userAndHost(e.Target)
		return fmt.Sprintf("Login failed for %s at %s. Check the password with \"profiles\" or \"connect\".", user, host)
	case e.Status == 403:
		return fmt.Sprintf("You do not have permission to %s %s.", e.Op, e.Target)
	case e.Status == 404 && (kind == "database" || kind == "documents"):
		// couch.Client targets a missing database two ways: "database %q" from
		// an operation on the database itself (create, delete, cd's existence
		// check), and "documents in %q" from _all_docs / _find / _bulk_docs,
		// which is the only way those endpoints 404. Both name the database in
		// their one quoted segment, so both read the same sentence.
		return fmt.Sprintf("Database %q does not exist. \"ls /\" lists databases.", name)
	case e.Status == 404 && kind == "document":
		return fmt.Sprintf("Document %q was not found in %q.", name, db)
	case e.Status == 404 && kind == "attachment":
		// The parent document is the actionable half: naming the database
		// instead left the operator wondering which document was searched, and
		// a design document has to be called one, because a path below a design
		// document is otherwise easy to mistake for a mistyped view path.
		if parent, ok := attachmentParent(e.Target); ok {
			return fmt.Sprintf("Attachment %q was not found on %s.", name, parent)
		}
		return fmt.Sprintf("Attachment %q was not found on %q.", name, db)
	case e.Status == 404 && kind == "view":
		return fmt.Sprintf("View %q was not found in %q.", name, db)
	case e.Status == 404:
		return fmt.Sprintf("%s was not found.", capitalise(e.Target))
	case e.Status == 409 && kind == "document":
		return fmt.Sprintf("Document %q was changed by someone else. \"cat %s\" shows the latest version.", name, name)
	case e.Status == 409:
		return fmt.Sprintf("%s was changed by someone else. Read it again and retry.", capitalise(e.Target))
	case e.Status == 412 && kind == "database":
		return fmt.Sprintf("Database %q already exists.", name)
	case e.Status == 400:
		return fmt.Sprintf("The server rejected the request: %s", e.Reason)
	case e.Status >= 500:
		return fmt.Sprintf("The server had a problem handling this request: %s", e.Reason)
	default:
		if e.Reason != "" {
			return fmt.Sprintf("Could not %s %s: %s", e.Op, e.Target, e.Reason)
		}
		return fmt.Sprintf("Could not %s %s.", e.Op, e.Target)
	}
}

// describeTarget picks apart the target strings couch.Client builds, which take
// the forms:
//
//	database "mydb"
//	document "doc1" in "mydb"
//	attachment "photo.jpg" of "doc1" in "mydb"
//	view "by_name" in "mydb"
//	server localhost:5984
func describeTarget(target string) (kind, name, db string) {
	fields := splitQuoted(target)
	if len(fields.words) == 0 {
		return "", "", ""
	}
	kind = fields.words[0]
	if len(fields.quoted) > 0 {
		name = fields.quoted[0]
	}
	if n := len(fields.quoted); n > 0 {
		db = fields.quoted[n-1]
	}
	return kind, name, db
}

// attachmentParent reads the parent out of the two shapes couch.AttachmentTarget
// builds — `attachment "N" of "doc" in "db"` and
// `attachment "N" of design document "app" in "db"` — and returns it as a
// phrase ready to drop into a sentence.
func attachmentParent(target string) (string, bool) {
	fields := splitQuoted(target)
	if len(fields.quoted) < 2 || len(fields.words) == 0 || fields.words[0] != "attachment" {
		return "", false
	}
	kind := "document"
	for _, w := range fields.words {
		if w == "design" {
			kind = "design document"
			break
		}
	}
	return fmt.Sprintf("%s %q", kind, fields.quoted[1]), true
}

type quotedTarget struct {
	words  []string
	quoted []string
}

// splitQuoted pulls the bare words and the double-quoted strings out of a
// target description.
func splitQuoted(target string) quotedTarget {
	var out quotedTarget
	inQuote := false
	var cur strings.Builder
	for _, r := range target {
		switch {
		case r == '"':
			if inQuote {
				out.quoted = append(out.quoted, cur.String())
				cur.Reset()
			}
			inQuote = !inQuote
		case inQuote:
			cur.WriteRune(r)
		case r == ' ':
			if cur.Len() > 0 {
				out.words = append(out.words, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if !inQuote && cur.Len() > 0 {
		out.words = append(out.words, cur.String())
	}
	return out
}

// hostFromTarget pulls the host out of `server host:port` or
// `session on host:port`, falling back to the whole target.
func hostFromTarget(target string) string {
	fields := strings.Fields(target)
	if len(fields) == 0 {
		return target
	}
	return fields[len(fields)-1]
}

// userAndHost pulls a user name and host out of a `user "admin" at
// host:port` target — the only shape doDecode, GetRev and
// sessionTransport.login build for a 401. Any other target (a document, a
// database, a bare server) falls back to the generic form already used for
// connect's `server host:port` target: "the configured user" for the name,
// and whatever hostFromTarget can find for the host. Falling back this way,
// rather than reading the target's first quoted string, matters: a document
// or database target's quoted string is not a user name, and treating it as
// one would put a document id in a "Login failed for ..." sentence.
func userAndHost(target string) (string, string) {
	if name, host, ok := parseUserAtHost(target); ok {
		return name, host
	}
	return "the configured user", hostFromTarget(target)
}

// parseUserAtHost recognises exactly `user "<name>" at <host>`.
func parseUserAtHost(target string) (name, host string, ok bool) {
	const prefix = `user "`
	if !strings.HasPrefix(target, prefix) {
		return "", "", false
	}
	rest := target[len(prefix):]
	sep := strings.Index(rest, `" at `)
	if sep < 0 {
		return "", "", false
	}
	name = rest[:sep]
	host = rest[sep+len(`" at `):]
	if name == "" || host == "" || strings.ContainsAny(host, ` "`) {
		return "", "", false
	}
	return name, host, true
}

func capitalise(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
