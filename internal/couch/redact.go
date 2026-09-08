package couch

import (
	"net/url"
	"strings"
)

// RedactURL returns raw with any userinfo removed. Every message that names a
// server URL — an error, a prompt, a log line — must go through it first: a
// URL the operator typed may carry a password, and a secret must never reach
// stdout, stderr or an error message.
//
// url.URL.Redacted is not used: it substitutes "xxxxx" for the password but
// keeps the user name, and the whole userinfo is noise in a message. An
// unparseable URL cannot be shown to be credential-free, so everything up to
// the last "@" — which is where userinfo would be — is dropped instead.
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		if i := strings.LastIndex(raw, "@"); i >= 0 {
			return raw[i+1:]
		}
		return raw
	}
	if u.User == nil {
		// Nothing to strip, so hand back exactly what was passed in rather
		// than url.URL.String's re-encoded form.
		return raw
	}
	u.User = nil
	return u.String()
}
