package command

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/config"
	"github.com/sriannamalai/CDB.CLI/internal/couch"
	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// profilesAdd runs "profiles add" with the shared flags parsed, the way both
// front-ends call it.
func profilesAdd(t *testing.T, s *session.Session, argv ...string) (Result, error) {
	t.Helper()
	fs := NewFlagSet(Profiles())
	if err := fs.Parse(argv); err != nil {
		t.Fatal(err)
	}
	return Profiles().Run(context.Background(), s, Invocation{Args: fs.Args(), Flags: fs})
}

// A bare URL saved a profile with no secret and no username, so "cdb connect"
// on it 401'd on every command afterwards. On a terminal, ask — the same two
// questions the guided connect asks — and prove the answers against the server
// before writing anything.
func TestProfilesAddPromptsForCredentialsOnABareURL(t *testing.T) {
	srv := couchtest.New(t)
	path := withDeps(t, config.Defaults(), nil)
	s, out := guidedSession("admin\ns3cret\n")

	res, err := profilesAdd(t, s, "add", "local", srv.URL())
	if err != nil {
		t.Fatalf("profiles add: %v (output: %s)", err, out.String())
	}
	if _, ok := res.(Message); !ok {
		t.Fatalf("result = %#v, want a Message", res)
	}
	name, password := loginCredentials(t, srv)
	if name != "admin" || password != "s3cret" {
		t.Errorf("the credentials were not verified against the server (name %q)", name)
	}
	back, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := back.Profile("local")
	if !ok {
		t.Fatalf("profiles = %v, want one named local", back.Profiles)
	}
	if p.Auth != "session" || p.Username != "admin" {
		t.Errorf("saved profile = %+v, want the answers from the prompt", p)
	}
	if got, err := CurrentDeps().Secrets.Get("local"); err != nil || got != "s3cret" {
		t.Errorf("keyring secret = %q, %v; want the typed password", got, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "s3cret") {
		t.Errorf("the password was written into the config file:\n%s", raw)
	}
	if strings.Contains(out.String(), "s3cret") {
		t.Errorf("the password was echoed to the terminal:\n%s", out.String())
	}
}

// A password the server rejects must leave nothing behind, and must be
// reported as the 401 it is, which is what renders as "Login failed for admin
// at <host>. Check the password with "profiles" or "connect"." and exits 3.
func TestProfilesAddSavesNothingWhenTheLoginIsRejected(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/_session", 401, `{"error":"unauthorized","reason":"Name or password is incorrect."}`)
	path := withDeps(t, config.Defaults(), nil)
	s, out := guidedSession("admin\nwrong-password\n")

	_, err := profilesAdd(t, s, "add", "local", srv.URL())
	if err == nil {
		t.Fatalf("profiles add saved a profile the server would not accept (output: %s)", out.String())
	}
	ce, ok := couch.AsError(err)
	if !ok || ce.Status != 401 {
		t.Fatalf("err = %v (%T), want a 401 from the server", err, err)
	}
	if strings.Contains(err.Error(), "wrong-password") {
		t.Errorf("the rejected password reached the error message: %v", err)
	}
	back, loadErr := config.Load(path)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(back.Profiles) != 0 {
		t.Errorf("profiles = %v, want none written when the login was refused", back.Profiles)
	}
	if names, _ := CurrentDeps().Secrets.List(); len(names) != 0 {
		t.Errorf("secrets = %v, want none written when the login was refused", names)
	}
	// Verifying a profile must not become a connection: the session was
	// pointed at nothing, and it stays that way.
	if s.Connected() {
		t.Error("profiles add attached a client to the session")
	}
}

// --anonymous is the operator saying there are no credentials. Saving auth
// "session" with no secret is the state this issue is about; "none" is the
// state that connects.
func TestProfilesAddAnonymousSavesAuthNone(t *testing.T) {
	srv := couchtest.New(t)
	path := withDeps(t, config.Defaults(), nil)
	s, out := guidedSession("")

	if _, err := profilesAdd(t, s, "add", "local", srv.URL(), "--anonymous"); err != nil {
		t.Fatalf("profiles add --anonymous: %v (output: %s)", err, out.String())
	}
	if strings.Contains(out.String(), "Username") {
		t.Errorf("--anonymous still asked for credentials:\n%s", out.String())
	}
	back, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := back.Profile("local"); !ok || p.Auth != "none" {
		t.Errorf("saved profile = %+v (ok=%v), want auth \"none\"", p, ok)
	}
}

// Pressing Enter at the password is the same answer --anonymous gives, and
// connect already reads it that way.
func TestProfilesAddEmptyPasswordSavesAuthNone(t *testing.T) {
	srv := couchtest.New(t)
	path := withDeps(t, config.Defaults(), nil)
	s, out := guidedSession("admin\n\n")

	if _, err := profilesAdd(t, s, "add", "local", srv.URL()); err != nil {
		t.Fatalf("profiles add: %v (output: %s)", err, out.String())
	}
	back, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := back.Profile("local"); !ok || p.Auth != "none" {
		t.Errorf("saved profile = %+v (ok=%v), want auth \"none\"", p, ok)
	}
	if names, _ := CurrentDeps().Secrets.List(); len(names) != 0 {
		t.Errorf("secrets = %v, want none for an anonymous profile", names)
	}
}

// Without a terminal there is nobody to ask, so the command does exactly what
// it did before: it stores the URL as typed and touches no server.
func TestProfilesAddWithoutATerminalIsUnchanged(t *testing.T) {
	srv := couchtest.New(t)
	path := withDeps(t, config.Defaults(), nil)
	var out bytes.Buffer
	s := session.New(strings.NewReader("admin\ns3cret\n"), &out, &out)

	if _, err := profilesAdd(t, s, "add", "local", srv.URL()); err != nil {
		t.Fatalf("profiles add: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("a non-interactive profiles add printed a prompt:\n%s", out.String())
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("a non-interactive profiles add reached the server: %d requests", len(srv.Requests()))
	}
	back, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := back.Profile("local")
	if !ok || p.Auth != "session" || p.Username != "" {
		t.Errorf("saved profile = %+v (ok=%v), want the 1.1.0 shape", p, ok)
	}
}

// A URL that carries its own userinfo is already answered for: the credentials
// come out of it, and nothing is asked.
func TestProfilesAddWithURLCredentialsDoesNotPrompt(t *testing.T) {
	path := withDeps(t, config.Defaults(), nil)
	s, out := guidedSession("")

	if _, err := profilesAdd(t, s, "add", "local", "http://admin:hunter2@db.example.com:5984/"); err != nil {
		t.Fatalf("profiles add: %v (output: %s)", err, out.String())
	}
	if out.Len() != 0 {
		t.Errorf("a URL with credentials still asked for them:\n%s", out.String())
	}
	back, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := back.Profile("local"); !ok || p.Username != "admin" || p.URL != "http://db.example.com:5984/" {
		t.Errorf("saved profile = %+v (ok=%v)", p, ok)
	}
	if got, _ := CurrentDeps().Secrets.Get("local"); got != "hunter2" {
		t.Errorf("keyring secret = %q, want the URL's password", got)
	}
}

// "profiles add --auth iam" has exactly one question to ask — the API key —
// and the answer is a secret. The path used to fall through to connect's two
// session questions, so the key was typed at "Username", with echo on, and
// written into config.toml as the profile's user name. An IAM profile has no
// user name at all.
func TestProfilesAddWithAuthIAMAsksForTheKeyOnly(t *testing.T) {
	srv := couchtest.New(t)
	path := withDeps(t, config.Defaults(), nil)
	s, out := guidedSession("an-api-key\n")

	if _, err := profilesAdd(t, s, "add", "cloud", srv.URL(), "--auth", "iam"); err != nil {
		t.Fatalf("profiles add --auth iam: %v (output: %s)", err, out.String())
	}
	if !strings.Contains(out.String(), "IAM API key:") {
		t.Errorf("the key was not asked for by name:\n%s", out.String())
	}
	if strings.Contains(out.String(), "Username") || strings.Contains(out.String(), "Password") {
		t.Errorf("an IAM profile was asked the session questions:\n%s", out.String())
	}
	back, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := back.Profile("cloud")
	if !ok {
		t.Fatalf("profiles = %v, want one named cloud", back.Profiles)
	}
	if p.Auth != "iam" || p.Username != "" {
		t.Errorf("saved profile = %+v, want an iam profile with no user name", p)
	}
	if got, err := CurrentDeps().Secrets.Get("cloud"); err != nil || got != "an-api-key" {
		t.Errorf("keyring secret = %q, %v; want the typed API key", got, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "an-api-key") {
		t.Errorf("the API key was written into the config file:\n%s", raw)
	}
}

// "profiles add --auth jwt" asks for the bearer token the way every other
// entry point does. It used to ask for a "Password", which is not what a JWT
// profile holds.
func TestProfilesAddWithAuthJWTAsksForTheBearerToken(t *testing.T) {
	srv := couchtest.New(t)
	path := withDeps(t, config.Defaults(), nil)
	s, out := guidedSession("a.jwt.token\n")

	if _, err := profilesAdd(t, s, "add", "bearer", srv.URL(), "--auth", "jwt"); err != nil {
		t.Fatalf("profiles add --auth jwt: %v (output: %s)", err, out.String())
	}
	if !strings.Contains(out.String(), "Bearer token:") {
		t.Errorf("the token was not asked for by name:\n%s", out.String())
	}
	if strings.Contains(out.String(), "Password") {
		t.Errorf("a jwt profile was asked for a password:\n%s", out.String())
	}
	back, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := back.Profile("bearer")
	if !ok {
		t.Fatalf("profiles = %v, want one named bearer", back.Profiles)
	}
	if p.Auth != "jwt" || p.Username != "" {
		t.Errorf("saved profile = %+v, want a jwt profile with no user name", p)
	}
	if got, err := CurrentDeps().Secrets.Get("bearer"); err != nil || got != "a.jwt.token" {
		t.Errorf("keyring secret = %q, %v; want the typed token", got, err)
	}
}

// The global constraint, held for every kind at once: whatever "profiles add"
// collected, no field of the saved profile may hold it, and it may not appear
// anywhere in the plaintext config file. Only the keyring keeps secrets.
func TestProfilesAddNeverWritesTheSecretIntoTheProfile(t *testing.T) {
	cases := []struct {
		kind    string
		answers string
		secret  string
		stub    func(t *testing.T) *couchtest.Server
	}{
		{"session", "admin\nsession-credential\n", "session-credential", func(t *testing.T) *couchtest.Server { return couchtest.New(t) }},
		{"jwt", "jwt-credential\n", "jwt-credential", func(t *testing.T) *couchtest.Server { return couchtest.New(t) }},
		{"proxy", "ops\n_admin\nproxysecret\n", "proxysecret", func(t *testing.T) *couchtest.Server { return proxyStub(t, proxyTokenSHA1) }},
		{"iam", "iam-credential\n", "iam-credential", func(t *testing.T) *couchtest.Server { return couchtest.New(t) }},
		// "none" has no credential to collect, so it must ask nothing at all:
		// an empty stdin is an error if any question is put.
		{"none", "", "", func(t *testing.T) *couchtest.Server { return couchtest.New(t) }},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			srv := tc.stub(t)
			path := withDeps(t, config.Defaults(), nil)
			s, out := guidedSession(tc.answers)
			if _, err := profilesAdd(t, s, "add", "p", srv.URL(), "--auth", tc.kind); err != nil {
				t.Fatalf("profiles add --auth %s: %v (output: %s)", tc.kind, err, out.String())
			}
			back, err := config.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			p, ok := back.Profile("p")
			if !ok {
				t.Fatalf("profiles = %v, want one named p", back.Profiles)
			}
			if tc.secret == "" {
				if p.Username != "" {
					t.Errorf("profile = %+v, want no user name for %s", p, tc.kind)
				}
				return
			}
			v := reflect.ValueOf(p)
			for i := 0; i < v.NumField(); i++ {
				if strings.Contains(fmt.Sprint(v.Field(i).Interface()), tc.secret) {
					t.Errorf("profile field %s holds the secret: %+v", v.Type().Field(i).Name, p)
				}
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), tc.secret) {
				t.Errorf("the %s credential was written into the config file:\n%s", tc.kind, raw)
			}
			if got, err := CurrentDeps().Secrets.Get("p"); err != nil || got != tc.secret {
				t.Errorf("keyring secret = %q, %v; want the typed credential", got, err)
			}
		})
	}
}

// Roles belong to proxy authentication and nothing else reads them: couch.New
// sends X-Auth-CouchDB-Roles only under auth = "proxy". A --roles that rode
// along with a session connection used to be written into the saved profile,
// where it looks like a claim cdb honours and is never sent anywhere.
func TestRolesAreSavedOnlyForProxyProfiles(t *testing.T) {
	srv := couchtest.New(t)
	path := withDeps(t, config.Defaults(), map[string]string{"CDB_PASSWORD": "hunter2", "CDB_USER": "admin"})
	s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	fs := NewRegistry().NewFlagSet(Connect())
	if err := fs.Parse([]string{srv.URL(), "--auth", "session", "--roles", "a,b", "--save", "--as", "saved"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Connect().Run(context.Background(), s, Invocation{Args: fs.Args(), Flags: fs}); err != nil {
		t.Fatal(err)
	}
	back, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := back.Profile("saved")
	if !ok {
		t.Fatalf("no profile named \"saved\" in %s", path)
	}
	if len(p.Roles) != 0 {
		t.Errorf("session profile = %+v, want no roles", p)
	}
}

// The same rule where "profiles add" writes the profile.
func TestProfilesAddSavesRolesOnlyForProxyProfiles(t *testing.T) {
	srv := couchtest.New(t)
	path := withDeps(t, config.Defaults(), nil)
	s, out := guidedSession("a.jwt.token\n")

	if _, err := profilesAdd(t, s, "add", "bearer", srv.URL(), "--auth", "jwt", "--roles", "a,b"); err != nil {
		t.Fatalf("profiles add --auth jwt --roles: %v (output: %s)", err, out.String())
	}
	back, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := back.Profile("bearer")
	if !ok {
		t.Fatalf("profiles = %v, want one named bearer", back.Profiles)
	}
	if len(p.Roles) != 0 {
		t.Errorf("jwt profile = %+v, want no roles", p)
	}
}
