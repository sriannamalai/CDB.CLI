package config

import (
	"errors"
	"testing"
)

func TestMemorySecrets(t *testing.T) {
	s := NewMemorySecrets()
	if _, err := s.Get("local"); !errors.Is(err, ErrSecretNotFound) {
		t.Errorf("Get on an empty store = %v, want ErrSecretNotFound", err)
	}
	if err := s.Set("local", "hunter2"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("local")
	if err != nil || got != "hunter2" {
		t.Errorf("Get = %q, %v", got, err)
	}
	names, err := s.List()
	if err != nil || len(names) != 1 || names[0] != "local" {
		t.Errorf("List = %v, %v", names, err)
	}
	if err := s.Remove("local"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("local"); !errors.Is(err, ErrSecretNotFound) {
		t.Errorf("Get after Remove = %v, want ErrSecretNotFound", err)
	}
}

func TestOpenSecretsFileBackendRoundTrip(t *testing.T) {
	// Pin the backend here, not in the caller's environment: without this the
	// first allowed backend is the real macOS Keychain, so a bare
	// "go test ./..." on a developer Mac pops a Keychain dialog (which hangs an
	// unattended run) and writes a real cdb/local credential.
	t.Setenv("CDB_KEYRING_BACKEND", "file")
	dir := t.TempDir()
	s, err := OpenSecrets(dir, func(string) (string, error) { return "test-passphrase", nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set("local", "hunter2"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("local")
	if err != nil || got != "hunter2" {
		t.Fatalf("Get = %q, %v", got, err)
	}
	if err := s.Remove("local"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("local"); !errors.Is(err, ErrSecretNotFound) {
		t.Errorf("Get after Remove = %v, want ErrSecretNotFound", err)
	}
}
