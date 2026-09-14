package config

import "testing"

func lookupFrom(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func TestLoadEnv(t *testing.T) {
	e := LoadEnv(lookupFrom(map[string]string{
		"CDB_PROFILE":      "staging",
		"CDB_URL":          "https://couch.example.com",
		"CDB_USER":         "svc",
		"CDB_PASSWORD":     "hunter2",
		"CDB_INSECURE_TLS": "true",
	}))
	if e.Profile != "staging" || e.URL != "https://couch.example.com" || e.User != "svc" || e.Password != "hunter2" {
		t.Errorf("LoadEnv = %+v", e)
	}
	if e.InsecureTLS == nil || !*e.InsecureTLS {
		t.Errorf("InsecureTLS = %v, want a pointer to true", e.InsecureTLS)
	}
}

func TestEnvApplyOverridesProfile(t *testing.T) {
	p := Profile{Name: "local", URL: "http://localhost:5984", Auth: "session", Username: "admin"}
	e := LoadEnv(lookupFrom(map[string]string{"CDB_URL": "http://other:5984", "CDB_USER": "bob"}))
	got := e.Apply(p)
	if got.URL != "http://other:5984" || got.Username != "bob" || got.Auth != "session" {
		t.Errorf("Apply = %+v", got)
	}
}

func TestEnvTokenSelectsJWT(t *testing.T) {
	e := LoadEnv(lookupFrom(map[string]string{"CDB_TOKEN": "tok.en.sig"}))
	got := e.Apply(Profile{Name: "local", URL: "http://localhost:5984", Auth: "session"})
	if got.Auth != "jwt" {
		t.Errorf("Auth = %q, want jwt when CDB_TOKEN is set", got.Auth)
	}
	secret, ok := e.Secret()
	if !ok || secret != "tok.en.sig" {
		t.Errorf("Secret() = %q, %v", secret, ok)
	}
}

func TestEnvSecretPrefersPassword(t *testing.T) {
	e := LoadEnv(lookupFrom(map[string]string{"CDB_PASSWORD": "pw"}))
	secret, ok := e.Secret()
	if !ok || secret != "pw" {
		t.Errorf("Secret() = %q, %v", secret, ok)
	}
	empty := LoadEnv(lookupFrom(map[string]string{}))
	if _, ok := empty.Secret(); ok {
		t.Error("Secret() = ok with no environment secret")
	}
}

func TestEnvTokenWinsOverPasswordWhenBothSet(t *testing.T) {
	// Apply picks "jwt" whenever CDB_TOKEN is set, regardless of CDB_PASSWORD.
	// Secret must agree, or the profile would claim JWT auth while handing
	// over the password as the bearer credential.
	e := LoadEnv(lookupFrom(map[string]string{"CDB_TOKEN": "tok.en.sig", "CDB_PASSWORD": "hunter2"}))
	got := e.Apply(Profile{Name: "local", URL: "http://localhost:5984", Auth: "session"})
	if got.Auth != "jwt" {
		t.Errorf("Auth = %q, want jwt when both CDB_TOKEN and CDB_PASSWORD are set", got.Auth)
	}
	secret, ok := e.Secret()
	if !ok || secret != "tok.en.sig" {
		t.Errorf("Secret() = %q, %v, want the token", secret, ok)
	}
}

// A profile that names proxy authentication keeps it. A shell exporting
// CDB_PASSWORD for another server used to flip the kind to "session" and send
// the shared secret as a password — a silent change of credential, on a
// profile the operator wrote by hand.
func TestApplyKeepsProxyAndIAMKindsOverCDBPassword(t *testing.T) {
	for _, kind := range []string{"proxy", "iam"} {
		e := Env{Password: "hunter2", Token: "eyJ..."}
		got := e.Apply(Profile{Name: "ops", URL: "https://couch.example.com", Auth: kind})
		if got.Auth != kind {
			t.Errorf("auth = %q, want %q", got.Auth, kind)
		}
	}
}

func TestIAMEnvironmentSelectsTheKindAndTheSecret(t *testing.T) {
	lookup := func(k string) (string, bool) {
		v, ok := map[string]string{
			"CDB_IAM_API_KEY": "an-api-key",
			"CDB_IAM_URL":     "https://iam.test.invalid/identity/token",
			"CDB_PASSWORD":    "hunter2",
			"CDB_TOKEN":       "eyJ...",
		}[k]
		return v, ok
	}
	e := LoadEnv(lookup)
	if e.IAMKey != "an-api-key" || e.IAMURL != "https://iam.test.invalid/identity/token" {
		t.Fatalf("env = %#v", e)
	}
	got := e.Apply(Profile{Name: "c", URL: "https://x.cloudantnosqldb.appdomain.cloud"})
	if got.Auth != "iam" {
		t.Errorf("auth = %q, want iam: CDB_IAM_API_KEY beats CDB_TOKEN and CDB_PASSWORD", got.Auth)
	}
	if got.IAMURL != "https://iam.test.invalid/identity/token" {
		t.Errorf("iam_url = %q", got.IAMURL)
	}
	secret, ok := e.Secret()
	if !ok || secret != "an-api-key" {
		t.Errorf("Secret() = %q, %v; want the API key", secret, ok)
	}
}

// Spec §4.1: CDB_TOKEN is not consulted for "iam". A shell that exports one for
// another server must not have it handed to Cloudant as an API key, and must
// not have it suppress the keyring read that would have found the real one.
func TestSecretForIAMIgnoresCDBToken(t *testing.T) {
	e := Env{Token: "eyJ...", Password: "hunter2"}
	if secret, ok := e.SecretFor("iam"); ok || secret != "" {
		t.Errorf("SecretFor(\"iam\") = %q, %v; want no environment secret", secret, ok)
	}
	// Every other kind keeps the 1.1 precedence exactly.
	if secret, ok := e.SecretFor("proxy"); !ok || secret != "eyJ..." {
		t.Errorf("SecretFor(\"proxy\") = %q, %v", secret, ok)
	}
	if secret, ok := (Env{IAMKey: "k", Token: "t"}).SecretFor("iam"); !ok || secret != "k" {
		t.Errorf("SecretFor(\"iam\") with a key = %q, %v", secret, ok)
	}
}

// CDB_IAM_API_KEY is a selector as well as a credential, but a profile that
// already names proxy authentication has said what it is: a key exported for
// some other server must not silently turn it into an IAM connection.
func TestApplyIAMKeyDoesNotOverrideAnExplicitKind(t *testing.T) {
	if got := (Env{IAMKey: "k"}).Apply(Profile{Auth: "proxy"}).Auth; got != "proxy" {
		t.Errorf("auth = %q, want proxy", got)
	}
	if got := (Env{IAMKey: "k"}).Apply(Profile{Auth: "iam"}).Auth; got != "iam" {
		t.Errorf("auth = %q, want iam", got)
	}
	// An unsettled profile is still switched: that is what the key is for.
	if got := (Env{IAMKey: "k"}).Apply(Profile{}).Auth; got != "iam" {
		t.Errorf("auth = %q, want iam", got)
	}
	if got := (Env{IAMKey: "k"}).Apply(Profile{Auth: "session"}).Auth; got != "iam" {
		t.Errorf("auth = %q, want iam", got)
	}
}
