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
