package config

import (
	"errors"
	"reflect"
	"runtime"
	"testing"

	"github.com/99designs/keyring"
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

// funcPointer returns a function value's entry point, for comparing that two
// func values refer to the same function. Go gives no direct == for funcs
// other than nil, so this is the usual workaround.
func funcPointer(f any) uintptr {
	return reflect.ValueOf(f).Pointer()
}

func TestResolvePromptDefaultsToTerminalPrompt(t *testing.T) {
	got := resolvePrompt(nil)
	if got == nil {
		t.Fatal("resolvePrompt(nil) = nil, want keyring.TerminalPrompt")
	}
	if funcPointer(got) != funcPointer(keyring.PromptFunc(keyring.TerminalPrompt)) {
		t.Errorf("resolvePrompt(nil) = %v, want keyring.TerminalPrompt", runtime.FuncForPC(funcPointer(got)).Name())
	}
}

func TestResolvePromptPassesThroughNonNil(t *testing.T) {
	custom := func(string) (string, error) { return "test-passphrase", nil }
	got := resolvePrompt(custom)
	if got == nil {
		t.Fatal("resolvePrompt(custom) = nil")
	}
	s, err := got("ignored")
	if err != nil || s != "test-passphrase" {
		t.Errorf("resolvePrompt(custom)(...) = %q, %v, want the custom prompt's result", s, err)
	}
}

func TestOpenSecretsWithNilPromptDoesNotPanicOnOpen(t *testing.T) {
	// OpenSecrets itself must not panic when prompt is nil: keyring.Open only
	// stores FilePasswordFunc, it does not call it. The file backend's
	// unlock() calls the func lazily on the first Get/Set, which -- for the
	// real default, keyring.TerminalPrompt -- would block on stdin here, so
	// this test stops at Open and leaves the "does the resolved func work"
	// case to TestResolvePromptDefaultsToTerminalPrompt above.
	t.Setenv("CDB_KEYRING_BACKEND", "file")
	dir := t.TempDir()
	if _, err := OpenSecrets(dir, nil); err != nil {
		t.Fatal(err)
	}
}
