package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sriannamalai/CDB.CLI/internal/config"
	"github.com/sriannamalai/CDB.CLI/internal/couch/couchtest"
	"github.com/sriannamalai/CDB.CLI/internal/session"
)

// TestMain points the real dependency builder at a throwaway directory and at
// the file keyring backend. Every test installs its own Deps through withDeps,
// but a test that forgot to would otherwise reach the operator's real
// config.toml and OS keychain; this makes that impossible.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "cdb-command-test")
	if err != nil {
		panic(err)
	}
	os.Setenv("XDG_CONFIG_HOME", dir)
	os.Setenv("APPDATA", dir)
	os.Setenv("CDB_KEYRING_BACKEND", "file")
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// withDeps installs a temporary config file and an in-memory keyring.
func withDeps(t *testing.T, cfg *config.Config, env map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if cfg != nil {
		if err := cfg.Save(path); err != nil {
			t.Fatal(err)
		}
	}
	secrets := config.NewMemorySecrets()
	SetDeps(&Deps{
		ConfigPath: path,
		Secrets:    secrets,
		LookupEnv: func(k string) (string, bool) {
			v, ok := env[k]
			return v, ok
		},
	})
	t.Cleanup(func() { SetDeps(nil) })
	return path
}

func TestConnectUsesTheOnlyProfile(t *testing.T) {
	srv := couchtest.New(t)
	cfg := config.Defaults()
	cfg.SetProfile(config.Profile{Name: "local", URL: srv.URL(), Auth: "session", Username: "admin"})
	withDeps(t, cfg, nil)
	deps := CurrentDeps()
	if err := deps.Secrets.Set("local", "password"); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	s := session.New(strings.NewReader(""), &out, &out)
	res, err := Connect().Run(context.Background(), s, Invocation{})
	if err != nil {
		t.Fatalf("connect: %v (output: %s)", err, out.String())
	}
	if !s.Connected() {
		t.Fatal("session is not connected after connect")
	}
	if s.Profile != "local" {
		t.Errorf("Profile = %q, want local", s.Profile)
	}
	msg, ok := res.(Message)
	if !ok || !strings.Contains(msg.Text, "3.5.2") {
		t.Errorf("connect result = %#v, want a message naming the server version", res)
	}
}

func TestConnectWithAnExplicitURLUsesNoAuth(t *testing.T) {
	srv := couchtest.New(t)
	withDeps(t, config.Defaults(), nil)
	s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if _, err := Connect().Run(context.Background(), s, Invocation{Args: []string{srv.URL()}}); err != nil {
		t.Fatal(err)
	}
	if !s.Connected() {
		t.Fatal("not connected")
	}
	for _, r := range srv.Requests() {
		if r.Method == "POST" && r.Path == "/_session" {
			t.Fatal("connect to a bare URL performed a session login")
		}
	}
}

func TestConnectEnvOverridesWin(t *testing.T) {
	srv := couchtest.New(t)
	cfg := config.Defaults()
	cfg.Default = "local"
	cfg.SetProfile(config.Profile{Name: "local", URL: "http://unreachable.invalid:5984", Auth: "session", Username: "admin"})
	withDeps(t, cfg, map[string]string{"CDB_URL": srv.URL(), "CDB_USER": "admin", "CDB_PASSWORD": "password"})
	s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if _, err := Connect().Run(context.Background(), s, Invocation{}); err != nil {
		t.Fatalf("connect with env overrides: %v", err)
	}
	if s.Client.URL() != strings.TrimRight(srv.URL(), "/") {
		t.Errorf("client URL = %q, want %q", s.Client.URL(), srv.URL())
	}
}

func TestConnectVerifiesWithTheSessionEndpoint(t *testing.T) {
	srv := couchtest.New(t)
	withDeps(t, config.Defaults(), nil)
	s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if _, err := Connect().Run(context.Background(), s, Invocation{Args: []string{srv.URL()}}); err != nil {
		t.Fatal(err)
	}
	if srv.Last("GET", "/_session") == nil {
		t.Error("connect did not verify the connection with GET /_session")
	}
}

func TestConnectSaveWritesTheProfileAndSecret(t *testing.T) {
	srv := couchtest.New(t)
	path := withDeps(t, config.Defaults(), map[string]string{"CDB_PASSWORD": "hunter2", "CDB_USER": "admin"})
	s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	fs := NewRegistry().NewFlagSet(Connect())
	if err := fs.Parse([]string{srv.URL(), "--save", "--as", "saved"}); err != nil {
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
	if p.URL != srv.URL() {
		t.Errorf("saved URL = %q, want %q", p.URL, srv.URL())
	}
	if back.Default != "saved" {
		t.Errorf("Default = %q, want \"saved\"", back.Default)
	}
	if s.Profile != "saved" {
		t.Errorf("session profile = %q, want \"saved\"", s.Profile)
	}
	got, err := CurrentDeps().Secrets.Get("saved")
	if err != nil || got != "hunter2" {
		t.Errorf("saved secret = %q, %v; want the password from the environment", got, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "hunter2") {
		t.Errorf("the secret was written into the config file:\n%s", raw)
	}
}

func TestConnectGuidedPromptCreatesTheFirstProfile(t *testing.T) {
	srv := couchtest.New(t)
	path := withDeps(t, config.Defaults(), nil)
	var out bytes.Buffer
	s := session.New(strings.NewReader(""), &out, &out)
	s.Prefs.Interactive = true
	// Server URL, authentication kind, profile name, then "y" to save.
	s.SetStdin(strings.NewReader(srv.URL() + "\nnone\nlocal\ny\n"))
	if _, err := Connect().Run(context.Background(), s, Invocation{}); err != nil {
		t.Fatalf("guided connect: %v (output: %s)", err, out.String())
	}
	if !s.Connected() {
		t.Fatal("guided connect did not connect")
	}
	back, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := back.Profile("local")
	if !ok || p.URL != srv.URL() || p.Auth != "none" {
		t.Errorf("saved profile = %+v (ok=%v), want the answers from stdin", p, ok)
	}
	if !strings.Contains(out.String(), "Server URL") {
		t.Errorf("the walk-through did not ask for a URL:\n%s", out.String())
	}
}

// Ctrl-D at "Save this connection as a profile?" is not consent. The question
// writes a file and a keyring entry, so an answer nobody gave must be "no".
func TestConnectGuidedPromptSavesNothingOnEOF(t *testing.T) {
	srv := couchtest.New(t)
	path := withDeps(t, config.Defaults(), nil)
	var out bytes.Buffer
	s := session.New(strings.NewReader(""), &out, &out)
	s.Prefs.Interactive = true
	// Server URL, authentication kind, profile name — then stdin runs out at
	// the save question.
	s.SetStdin(strings.NewReader(srv.URL() + "\nnone\nlocal\n"))

	if _, err := Connect().Run(context.Background(), s, Invocation{}); err != nil {
		t.Fatalf("guided connect: %v (output: %s)", err, out.String())
	}
	if !s.Connected() {
		t.Error("declining to save also dropped the connection")
	}
	back, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Profiles) != 0 {
		t.Errorf("profiles = %v, want none written for a question nobody answered", back.Profiles)
	}
	if names, _ := CurrentDeps().Secrets.List(); len(names) != 0 {
		t.Errorf("secrets = %v, want none written", names)
	}
}

// With two profiles and no default, "no profile is saved" is untrue and sends
// the operator to create a third.
func TestConnectWithNoDefaultNamesTheSavedProfiles(t *testing.T) {
	cfg := config.Defaults()
	cfg.SetProfile(config.Profile{Name: "one", URL: "http://one:5984", Auth: "none"})
	cfg.SetProfile(config.Profile{Name: "two", URL: "http://two:5984", Auth: "none"})
	cfg.Default = ""
	withDeps(t, cfg, nil)
	s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})

	_, err := Connect().Run(context.Background(), s, Invocation{})
	if err == nil {
		t.Fatal("connect with two profiles and no default succeeded")
	}
	if strings.Contains(err.Error(), "no profile is saved") {
		t.Errorf("error = %q, but two profiles are saved", err)
	}
	for _, want := range []string{"default", "profiles list"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q is missing %q", err, want)
		}
	}
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Errorf("error is %T, want a *UsageError", err)
	}
}

func TestConnectUnknownProfileIsAUsageError(t *testing.T) {
	withDeps(t, config.Defaults(), nil)
	s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	_, err := Connect().Run(context.Background(), s, Invocation{Args: []string{"nope"}})
	if _, ok := err.(*UsageError); !ok {
		t.Fatalf("err = %#v, want *UsageError", err)
	}
}

func TestProfilesList(t *testing.T) {
	cfg := config.Defaults()
	cfg.Default = "local"
	cfg.SetProfile(config.Profile{Name: "local", URL: "http://localhost:5984", Auth: "session", Username: "admin"})
	cfg.SetProfile(config.Profile{Name: "prod", URL: "https://couch.example.com", Auth: "jwt"})
	withDeps(t, cfg, nil)
	s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	res, err := Profiles().Run(context.Background(), s, Invocation{Args: []string{"list"}})
	if err != nil {
		t.Fatal(err)
	}
	rows, ok := res.(Rows)
	if !ok {
		t.Fatalf("result is %T, want Rows", res)
	}
	if len(rows.Items) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows.Items))
	}
	if rows.Items[0].Cells[0] != "local" || !strings.Contains(rows.Items[0].Cells[3], "yes") {
		t.Errorf("row 0 = %v, want local marked as the default", rows.Items[0].Cells)
	}
	body := strings.Join([]string{rows.Items[0].Cells[1], rows.Items[1].Cells[1]}, " ")
	if strings.Contains(body, "password") {
		t.Errorf("profiles output leaked a secret: %s", body)
	}
}

func TestProfilesRemoveDeletesProfileAndSecret(t *testing.T) {
	cfg := config.Defaults()
	cfg.SetProfile(config.Profile{Name: "gone", URL: "http://localhost:5984", Auth: "session", Username: "admin"})
	path := withDeps(t, cfg, nil)
	_ = CurrentDeps().Secrets.Set("gone", "hunter2")
	s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	s.Prefs.Yes = true
	if _, err := Profiles().Run(context.Background(), s, Invocation{Args: []string{"remove", "gone"}}); err != nil {
		t.Fatal(err)
	}
	back, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := back.Profile("gone"); ok {
		t.Error("profile still present after remove")
	}
	if names, _ := CurrentDeps().Secrets.List(); len(names) != 0 {
		t.Errorf("secrets after remove = %v, want none", names)
	}
}

func TestProfilesDefaultSetsTheDefault(t *testing.T) {
	cfg := config.Defaults()
	cfg.SetProfile(config.Profile{Name: "a", URL: "http://a:5984", Auth: "session"})
	path := withDeps(t, cfg, nil)
	s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if _, err := Profiles().Run(context.Background(), s, Invocation{Args: []string{"default", "a"}}); err != nil {
		t.Fatal(err)
	}
	back, _ := config.Load(path)
	if back.Default != "a" {
		t.Errorf("Default = %q, want a", back.Default)
	}
}

func TestSessionCommandShowsUserRolesAndVersion(t *testing.T) {
	srv := couchtest.New(t)
	withDeps(t, config.Defaults(), nil)
	s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if _, err := Connect().Run(context.Background(), s, Invocation{Args: []string{srv.URL()}}); err != nil {
		t.Fatal(err)
	}
	res, err := SessionCmd().Run(context.Background(), s, Invocation{})
	if err != nil {
		t.Fatal(err)
	}
	rows, ok := res.(Rows)
	if !ok {
		t.Fatalf("result is %T, want Rows", res)
	}
	joined := ""
	for _, r := range rows.Items {
		joined += r.Cells[0] + "=" + r.Cells[1] + ";"
	}
	for _, want := range []string{"user=admin", "roles=_admin", "version=3.5.2"} {
		if !strings.Contains(joined, want) {
			t.Errorf("session rows %q are missing %q", joined, want)
		}
	}
}

func TestSessionRequiresAConnection(t *testing.T) {
	if !SessionCmd().NeedsClient {
		t.Error("session command does not set NeedsClient")
	}
	if Connect().NeedsClient {
		t.Error("connect must not require an existing client")
	}
}

// loginCredentials decodes the body of the stub server's last POST /_session,
// so that a test can prove which credential cdb actually presented rather than
// only that a request happened.
func loginCredentials(t *testing.T, srv *couchtest.Server) (name, password string) {
	t.Helper()
	req := srv.Last("POST", "/_session")
	if req == nil {
		t.Fatal("no POST /_session reached the server")
	}
	var body struct {
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if err := json.Unmarshal(req.Body, &body); err != nil {
		t.Fatalf("decode the login body: %v", err)
	}
	return body.Name, body.Password
}

func TestConnectSessionAuthSendsTheStoredCredentials(t *testing.T) {
	srv := couchtest.New(t)
	cfg := config.Defaults()
	cfg.SetProfile(config.Profile{Name: "local", URL: srv.URL(), Auth: "session", Username: "admin"})
	withDeps(t, cfg, nil)
	if err := CurrentDeps().Secrets.Set("local", "keyring-password"); err != nil {
		t.Fatal(err)
	}
	s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if _, err := Connect().Run(context.Background(), s, Invocation{}); err != nil {
		t.Fatal(err)
	}
	name, password := loginCredentials(t, srv)
	if name != "admin" {
		t.Errorf("login name = %q, want admin", name)
	}
	if password != "keyring-password" {
		t.Error("the login did not present the secret held in the keyring")
	}
	verify := srv.Last("GET", "/_session")
	if verify == nil {
		t.Fatal("connect did not verify with GET /_session")
	}
	if !strings.Contains(verify.Header.Get("Cookie"), "AuthSession=test-cookie") {
		t.Errorf("GET /_session Cookie = %q, want the cookie the login returned", verify.Header.Get("Cookie"))
	}
	if got := verify.Header.Get("Authorization"); got != "" {
		t.Errorf("GET /_session Authorization = %q, want none for cookie auth", got)
	}
}

func TestConnectEnvSecretBypassesTheKeyring(t *testing.T) {
	srv := couchtest.New(t)
	cfg := config.Defaults()
	cfg.SetProfile(config.Profile{Name: "local", URL: srv.URL(), Auth: "session", Username: "admin"})
	withDeps(t, cfg, map[string]string{"CDB_PASSWORD": "from-env"})
	if err := CurrentDeps().Secrets.Set("local", "from-keyring"); err != nil {
		t.Fatal(err)
	}
	s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if _, err := Connect().Run(context.Background(), s, Invocation{}); err != nil {
		t.Fatal(err)
	}
	_, password := loginCredentials(t, srv)
	if password == "from-keyring" {
		t.Error("CDB_PASSWORD was set but connect still read the keyring")
	}
	if password != "from-env" {
		t.Error("the login did not present CDB_PASSWORD")
	}
}

func TestConnectJWTSendsTheBearerToken(t *testing.T) {
	srv := couchtest.New(t)
	cfg := config.Defaults()
	cfg.SetProfile(config.Profile{Name: "prod", URL: srv.URL(), Auth: "jwt"})
	withDeps(t, cfg, map[string]string{"CDB_TOKEN": "tok-123"})
	s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if _, err := Connect().Run(context.Background(), s, Invocation{Args: []string{"prod"}}); err != nil {
		t.Fatal(err)
	}
	verify := srv.Last("GET", "/_session")
	if verify == nil {
		t.Fatal("connect did not verify with GET /_session")
	}
	if got := verify.Header.Get("Authorization"); got != "Bearer tok-123" {
		t.Errorf("GET /_session Authorization = %q, want %q", got, "Bearer tok-123")
	}
	if srv.Last("POST", "/_session") != nil {
		t.Error("a JWT connection performed a cookie login")
	}
}

func TestConnectToABareURLSendsNoCredentials(t *testing.T) {
	srv := couchtest.New(t)
	withDeps(t, config.Defaults(), nil)
	s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if _, err := Connect().Run(context.Background(), s, Invocation{Args: []string{srv.URL()}}); err != nil {
		t.Fatal(err)
	}
	for _, r := range srv.Requests() {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("%s %s carried Authorization %q, want none", r.Method, r.Path, got)
		}
		if got := r.Header.Get("Cookie"); got != "" {
			t.Errorf("%s %s carried Cookie %q, want none", r.Method, r.Path, got)
		}
	}
}

func TestConnectAsRejectsADottedProfileName(t *testing.T) {
	srv := couchtest.New(t)
	path := withDeps(t, config.Defaults(), nil)
	s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	fs := NewRegistry().NewFlagSet(Connect())
	if err := fs.Parse([]string{srv.URL(), "--save", "--as", "couch.example.com"}); err != nil {
		t.Fatal(err)
	}
	_, err := Connect().Run(context.Background(), s, Invocation{Args: fs.Args(), Flags: fs})
	if _, ok := err.(*UsageError); !ok {
		t.Fatalf("err = %#v, want *UsageError for a dotted profile name", err)
	}
	back, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Profiles) != 0 {
		t.Errorf("profiles = %v, want nothing written for a rejected name", back.Profiles)
	}
}

func TestProfilesAddRejectsADottedProfileName(t *testing.T) {
	path := withDeps(t, config.Defaults(), nil)
	s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	_, err := Profiles().Run(context.Background(), s, Invocation{Args: []string{"add", "couch.example.com", "https://couch.example.com"}})
	if _, ok := err.(*UsageError); !ok {
		t.Fatalf("err = %#v, want *UsageError for a dotted profile name", err)
	}
	back, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Profiles) != 0 {
		t.Errorf("profiles = %v, want nothing written for a rejected name", back.Profiles)
	}
}

func TestProfilesAddRoundTripsThroughTheConfigFile(t *testing.T) {
	path := withDeps(t, config.Defaults(), nil)
	s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if _, err := Profiles().Run(context.Background(), s, Invocation{Args: []string{"add", "prod", "https://couch.example.com"}}); err != nil {
		t.Fatal(err)
	}
	back, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := back.Profile("prod")
	if !ok || p.URL != "https://couch.example.com" || p.Auth != "session" {
		t.Errorf("profile = %+v (ok=%v), want the added profile back unchanged", p, ok)
	}
}

func TestConnectSaveDerivesAProfileNameFromTheHost(t *testing.T) {
	srv := couchtest.New(t)
	path := withDeps(t, config.Defaults(), nil)
	s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	fs := NewRegistry().NewFlagSet(Connect())
	if err := fs.Parse([]string{srv.URL(), "--save"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Connect().Run(context.Background(), s, Invocation{Args: fs.Args(), Flags: fs}); err != nil {
		t.Fatal(err)
	}
	back, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	// The stub server runs on 127.0.0.1, so the derived name has to lose its
	// dots: --as refuses a dotted profile name, and a name the operator could
	// not have typed would be a name they cannot reason about.
	p, ok := back.Profile("127-0-0-1")
	if !ok {
		t.Fatalf("profiles = %v, want one named 127-0-0-1", back.Profiles)
	}
	if p.URL != srv.URL() {
		t.Errorf("saved URL = %q, want %q", p.URL, srv.URL())
	}
}

func TestConnectSaveKeepsURLCredentialsOutOfTheConfigFile(t *testing.T) {
	srv := couchtest.New(t)
	withCreds := strings.Replace(srv.URL(), "http://", "http://admin:hunter2@", 1)
	path := withDeps(t, config.Defaults(), nil)
	s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	fs := NewRegistry().NewFlagSet(Connect())
	if err := fs.Parse([]string{withCreds, "--save", "--as", "creds"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Connect().Run(context.Background(), s, Invocation{Args: fs.Args(), Flags: fs}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "hunter2") {
		t.Errorf("a URL password reached the config file:\n%s", raw)
	}
	back, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := back.Profile("creds")
	if !ok {
		t.Fatalf("no profile named \"creds\" in %s", path)
	}
	if p.URL != srv.URL() {
		t.Errorf("saved URL = %q, want the URL without its credentials (%q)", p.URL, srv.URL())
	}
	if p.Username != "admin" {
		t.Errorf("saved username = %q, want admin", p.Username)
	}
	if got, err := CurrentDeps().Secrets.Get("creds"); err != nil || got != "hunter2" {
		t.Errorf("reading the saved secret: %v; want the URL password moved into the keyring", err)
	}
}

// guidedSession returns an interactive session whose stdin replays answers.
func guidedSession(answers string) (*session.Session, *bytes.Buffer) {
	var out bytes.Buffer
	s := session.New(strings.NewReader(""), &out, &out)
	s.Prefs.Interactive = true
	s.SetStdin(strings.NewReader(answers))
	return s, &out
}

func TestConnectGuidedPromptSavesNothingWhenVerificationFails(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("POST", "/_session", 401, `{"error":"unauthorized","reason":"Name or password is incorrect."}`)
	path := withDeps(t, config.Defaults(), nil)
	// URL, auth kind, profile name, username, password.
	s, out := guidedSession(srv.URL() + "\nsession\nlocal\nadmin\nwrong-password\n")

	_, err := Connect().Run(context.Background(), s, Invocation{})
	if err == nil {
		t.Fatalf("guided connect succeeded against a server that rejected the credentials (output: %s)", out.String())
	}
	if s.Connected() {
		t.Error("the session holds a client after a failed verification")
	}
	back, loadErr := config.Load(path)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(back.Profiles) != 0 {
		t.Errorf("profiles = %v, want none written when verification failed", back.Profiles)
	}
	if names, _ := CurrentDeps().Secrets.List(); len(names) != 0 {
		t.Errorf("secrets = %v, want none written when verification failed", names)
	}
	raw, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Contains(string(raw), "wrong-password") {
		t.Errorf("the rejected password reached the config file:\n%s", raw)
	}
	if strings.Contains(out.String(), "wrong-password") {
		t.Errorf("the rejected password was echoed to the terminal:\n%s", out.String())
	}
}

func TestConnectGuidedPromptCanDeclineToSave(t *testing.T) {
	srv := couchtest.New(t)
	path := withDeps(t, config.Defaults(), nil)
	// URL, auth kind, profile name, then "no" to the save question.
	s, out := guidedSession(srv.URL() + "\nnone\nlocal\nn\n")

	if _, err := Connect().Run(context.Background(), s, Invocation{}); err != nil {
		t.Fatalf("guided connect: %v (output: %s)", err, out.String())
	}
	if !s.Connected() {
		t.Fatal("declining to save also dropped the connection")
	}
	if s.Profile != "" {
		t.Errorf("session profile = %q, want \"\" for a connection that was not saved", s.Profile)
	}
	back, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Profiles) != 0 {
		t.Errorf("profiles = %v, want none after declining to save", back.Profiles)
	}
}

func TestConnectGuidedPromptSavesAfterVerifying(t *testing.T) {
	srv := couchtest.New(t)
	path := withDeps(t, config.Defaults(), nil)
	// URL, auth kind, profile name, username, password, then "yes" to save.
	s, out := guidedSession(srv.URL() + "\nsession\nlocal\nadmin\ns3cret\nyes\n")

	if _, err := Connect().Run(context.Background(), s, Invocation{}); err != nil {
		t.Fatalf("guided connect: %v (output: %s)", err, out.String())
	}
	name, password := loginCredentials(t, srv)
	if name != "admin" {
		t.Errorf("login name = %q, want admin", name)
	}
	if password != "s3cret" {
		t.Error("the login did not present the password typed at the prompt")
	}
	back, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := back.Profile("local")
	if !ok || p.Auth != "session" || p.Username != "admin" {
		t.Errorf("saved profile = %+v (ok=%v), want the answers from the walk-through", p, ok)
	}
	if s.Profile != "local" {
		t.Errorf("session profile = %q, want local", s.Profile)
	}
	if got, err := CurrentDeps().Secrets.Get("local"); err != nil || got != "s3cret" {
		t.Errorf("reading the saved secret: %v; want the password from the walk-through", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "s3cret") {
		t.Errorf("the secret was written into the config file:\n%s", raw)
	}
	if strings.Contains(out.String(), "s3cret") {
		t.Errorf("the secret was echoed to the terminal:\n%s", out.String())
	}
}

func TestConnectGuidedPromptReAsksForAnUnknownAuthKind(t *testing.T) {
	srv := couchtest.New(t)
	path := withDeps(t, config.Defaults(), nil)
	// URL, a typo'd auth kind, then a valid one, profile name, save.
	s, out := guidedSession(srv.URL() + "\nsesion\nnone\nlocal\ny\n")

	if _, err := Connect().Run(context.Background(), s, Invocation{}); err != nil {
		t.Fatalf("guided connect: %v (output: %s)", err, out.String())
	}
	if !strings.Contains(out.String(), "session, jwt or none") {
		t.Errorf("the walk-through did not name the valid authentication kinds:\n%s", out.String())
	}
	if n := strings.Count(out.String(), "Authentication ("); n != 2 {
		t.Errorf("asked for the authentication kind %d times, want 2 (one rejected, one accepted)", n)
	}
	back, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := back.Profile("local")
	if !ok {
		t.Fatalf("profiles = %v, want one named local", back.Profiles)
	}
	if p.Auth != "none" {
		t.Errorf("saved auth = %q, want the corrected answer \"none\" rather than the typo", p.Auth)
	}
}

func TestConnectRejectsAnAnonymousLoginWhenAuthWasAskedFor(t *testing.T) {
	srv := couchtest.New(t)
	// A server with no admins answers 200 with a null user name.
	srv.JSON("GET", "/_session", 200, `{"ok":true,"userCtx":{"name":null,"roles":[]},"info":{"authenticated":"default","authentication_handlers":["cookie","default"]}}`)
	path := withDeps(t, config.Defaults(), nil)
	// URL, auth kind, profile name, username, password, save.
	s, out := guidedSession(srv.URL() + "\nsession\nlocal\nadmin\ns3cret\ny\n")

	_, err := Connect().Run(context.Background(), s, Invocation{})
	if err == nil {
		t.Fatalf("connect reported success for a login that was silently anonymous (output: %s)", out.String())
	}
	if !strings.Contains(err.Error(), "anonymously") {
		t.Errorf("error = %q, want it to say the login was anonymous", err)
	}
	if s.Connected() {
		t.Error("the session holds a client after an anonymous login")
	}
	back, loadErr := config.Load(path)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(back.Profiles) != 0 {
		t.Errorf("profiles = %v, want none saved for a profile that cannot log in", back.Profiles)
	}
	if names, _ := CurrentDeps().Secrets.List(); len(names) != 0 {
		t.Errorf("secrets = %v, want none saved", names)
	}
}

func TestConnectAllowsAnAnonymousLoginWhenAuthIsNone(t *testing.T) {
	srv := couchtest.New(t)
	srv.JSON("GET", "/_session", 200, `{"ok":true,"userCtx":{"name":null,"roles":[]},"info":{"authenticated":"default"}}`)
	withDeps(t, config.Defaults(), nil)
	s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})

	// An unauthenticated connection is exactly what "none" asked for.
	if _, err := Connect().Run(context.Background(), s, Invocation{Args: []string{srv.URL()}}); err != nil {
		t.Fatalf("connect to an admin-party server with auth none: %v", err)
	}
	if !s.Connected() {
		t.Fatal("not connected")
	}
}

// withBrokenKeyring installs deps whose keyring refuses to open, the way a
// machine with no usable backend behaves.
func withBrokenKeyring(t *testing.T, cfg *config.Config, env map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if cfg != nil {
		if err := cfg.Save(path); err != nil {
			t.Fatal(err)
		}
	}
	SetDeps(&Deps{
		ConfigPath: path,
		secretsErr: errors.New("no available keyring implementation"),
		LookupEnv: func(k string) (string, bool) {
			v, ok := env[k]
			return v, ok
		},
	})
	t.Cleanup(func() { SetDeps(nil) })
	return path
}

func TestCurrentDepsDoesNotSubstituteAnInMemoryKeyring(t *testing.T) {
	SetDeps(nil)
	t.Cleanup(func() { SetDeps(nil) })
	t.Setenv("CDB_KEYRING_BACKEND", "no-such-backend")

	d := CurrentDeps()
	if d.Secrets != nil {
		t.Errorf("Deps.Secrets = %T, want nil rather than a store that forgets on exit", d.Secrets)
	}
	store, err := d.SecretStore()
	if err == nil {
		t.Fatalf("SecretStore() = %T, nil; want the keyring failure surfaced", store)
	}
	if store != nil {
		t.Errorf("SecretStore() returned %T alongside its error, want no substitute store", store)
	}
	for _, want := range []string{"keyring", "CDB_PASSWORD", "CDB_TOKEN"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q is missing %q", err, want)
		}
	}
}

func TestConnectFailsWhenTheKeyringCannotOpen(t *testing.T) {
	srv := couchtest.New(t)
	cfg := config.Defaults()
	cfg.SetProfile(config.Profile{Name: "local", URL: srv.URL(), Auth: "session", Username: "admin"})
	withBrokenKeyring(t, cfg, nil)
	s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})

	_, err := Connect().Run(context.Background(), s, Invocation{})
	if err == nil {
		t.Fatal("connect succeeded even though the stored secret was unreachable")
	}
	if !strings.Contains(err.Error(), "CDB_PASSWORD") {
		t.Errorf("error = %q, want it to say how to connect without the keyring", err)
	}
	if s.Connected() {
		t.Error("connect attached a client without the credential it was meant to load")
	}
}

func TestConnectWithAnEnvSecretWorksWhenTheKeyringCannotOpen(t *testing.T) {
	srv := couchtest.New(t)
	cfg := config.Defaults()
	cfg.SetProfile(config.Profile{Name: "local", URL: srv.URL(), Auth: "session", Username: "admin"})
	withBrokenKeyring(t, cfg, map[string]string{"CDB_PASSWORD": "from-env"})
	s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})

	// This is the escape hatch the keyring error advertises; it has to work.
	if _, err := Connect().Run(context.Background(), s, Invocation{}); err != nil {
		t.Fatalf("connect with CDB_PASSWORD and a broken keyring: %v", err)
	}
	if _, password := loginCredentials(t, srv); password != "from-env" {
		t.Error("the login did not present CDB_PASSWORD")
	}
}

func TestConnectSaveFailsAndWritesNothingWhenTheKeyringCannotOpen(t *testing.T) {
	srv := couchtest.New(t)
	path := withBrokenKeyring(t, config.Defaults(), map[string]string{"CDB_USER": "admin", "CDB_PASSWORD": "hunter2"})
	s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	fs := NewRegistry().NewFlagSet(Connect())
	if err := fs.Parse([]string{srv.URL(), "--save", "--as", "saved"}); err != nil {
		t.Fatal(err)
	}

	_, err := Connect().Run(context.Background(), s, Invocation{Args: fs.Args(), Flags: fs})
	if err == nil {
		t.Fatal("connect --save reported success while discarding the secret")
	}
	if !strings.Contains(err.Error(), "keyring") {
		t.Errorf("error = %q, want it to name the keyring", err)
	}
	back, loadErr := config.Load(path)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(back.Profiles) != 0 {
		t.Errorf("profiles = %v, want no half-saved profile whose secret was dropped", back.Profiles)
	}
}

func TestOpenConnectsAndRecordsTheProfile(t *testing.T) {
	srv := couchtest.New(t)
	cfg := config.Defaults()
	cfg.Default = "local"
	cfg.SetProfile(config.Profile{Name: "local", URL: srv.URL(), Auth: "none"})
	withDeps(t, cfg, nil)
	s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})

	if err := Open(context.Background(), s, ""); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !s.Connected() {
		t.Fatal("Open returned nil but attached no client")
	}
	if s.Profile != "local" {
		t.Errorf("session profile = %q, want local", s.Profile)
	}
	if s.Client.URL() != srv.URL() {
		t.Errorf("client URL = %q, want %q", s.Client.URL(), srv.URL())
	}
	if srv.Last("GET", "/_session") == nil {
		t.Error("Open did not verify the connection with GET /_session")
	}
}

func TestOpenWithNoSavedProfileIsAUsageError(t *testing.T) {
	withDeps(t, config.Defaults(), nil)
	s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})

	err := Open(context.Background(), s, "")
	if _, ok := err.(*UsageError); !ok {
		t.Fatalf("err = %#v, want *UsageError", err)
	}
	if s.Connected() {
		t.Error("Open attached a client despite returning an error")
	}
}

func TestProfilesListRedactsURLCredentials(t *testing.T) {
	cfg := config.Defaults()
	cfg.SetProfile(config.Profile{Name: "hand-edited", URL: "https://admin:hunter2@couch.example.com", Auth: "session"})
	withDeps(t, cfg, nil)
	s := session.New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	res, err := Profiles().Run(context.Background(), s, Invocation{Args: []string{"list"}})
	if err != nil {
		t.Fatal(err)
	}
	rows, ok := res.(Rows)
	if !ok {
		t.Fatalf("result is %T, want Rows", res)
	}
	if got := rows.Items[0].Cells[1]; strings.Contains(got, "hunter2") {
		t.Errorf("profiles list printed a password: %q", got)
	}
	if got := string(rows.Items[0].JSON); strings.Contains(got, "hunter2") {
		t.Errorf("profiles list JSON printed a password: %q", got)
	}
}
