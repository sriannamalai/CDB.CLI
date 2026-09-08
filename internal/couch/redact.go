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
	if u, err := url.Parse(raw); err == nil && u.User != nil {
		u.User = nil
		return u.String()
	}
	// url.Parse either failed, or parsed the string in a way that hides the
	// credentials from it: "admin:hunter2@localhost:5984" with no scheme parses
	// happily as scheme "admin", opaque "hunter2@localhost:5984", and no User
	// at all. Neither case may be trusted, so cut the userinfo out textually.
	return trimUserinfo(raw)
}

// trimUserinfo drops everything up to the last "@" of the authority — the part
// after any "scheme://" and before the path, query or fragment. An "@" later in
// the string is part of a path and is left alone. Anything before the "@" is
// dropped whether or not it looks like it has a password: a bare user name is
// not worth keeping, and deciding "this one is safe" is exactly the judgement
// that let the password through in the first place.
func trimUserinfo(raw string) string {
	start := 0
	if i := strings.Index(raw, "://"); i >= 0 {
		start = i + len("://")
	}
	end := len(raw)
	if i := strings.IndexAny(raw[start:], "/?#"); i >= 0 {
		end = start + i
	}
	at := strings.LastIndex(raw[start:end], "@")
	if at < 0 {
		return raw
	}
	return raw[:start] + raw[start+at+1:]
}
