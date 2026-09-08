package couch

import (
	"strings"
	"testing"
)

// couch.New used to ignore an authentication kind it did not recognise and
// build an unauthenticated client. A hand-edited config with auth = "sesion"
// then connected as nobody and failed on every command, and dial's
// anonymous-login guard does not catch it: that guard fires only when Auth is
// not "none", and a typo is not "none" either — it is a client that sends no
// credentials while claiming it will.
func TestNewRejectsAnUnknownAuthKind(t *testing.T) {
	_, err := New(Config{URL: "http://localhost:5984", Auth: AuthKind("sesion")})
	if err == nil {
		t.Fatal("New accepted an unknown authentication kind")
	}
	for _, want := range []string{"sesion", "session", "jwt", "none"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q is missing %q", err, want)
		}
	}
}

// An empty kind is not a typo: it is the zero value, and the config layer
// already defaults it to "session" before it gets here. Treat it as "none" so
// a caller that builds a Config by hand still works.
func TestNewAcceptsTheKnownAuthKinds(t *testing.T) {
	for _, kind := range []AuthKind{"", AuthNone, AuthSession, AuthJWT} {
		if _, err := New(Config{URL: "http://localhost:5984", Auth: kind, Username: "admin", Secret: "x"}); err != nil {
			t.Errorf("New with auth %q: %v", kind, err)
		}
	}
}
