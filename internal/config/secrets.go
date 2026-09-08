package config

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/99designs/keyring"
)

// ErrSecretNotFound means the profile has no stored secret.
var ErrSecretNotFound = errors.New("no saved secret for that profile")

// Secrets stores one secret per profile.
type Secrets interface {
	Get(profile string) (string, error)
	Set(profile, secret string) error
	Remove(profile string) error
	List() ([]string, error)
}

type keyringSecrets struct{ ring keyring.Keyring }

// OpenSecrets opens the OS keyring, falling back to an encrypted file in
// dir/keyring. prompt is called for the file backend's passphrase. Setting
// CDB_KEYRING_BACKEND forces one backend, which is useful in CI.
func OpenSecrets(dir string, prompt func(string) (string, error)) (Secrets, error) {
	fileDir := filepath.Join(dir, "keyring")
	if err := os.MkdirAll(fileDir, 0o700); err != nil {
		return nil, err
	}
	allowed := []keyring.BackendType{
		keyring.KeychainBackend,
		keyring.WinCredBackend,
		keyring.SecretServiceBackend,
		keyring.KWalletBackend,
		keyring.FileBackend,
	}
	if forced := os.Getenv("CDB_KEYRING_BACKEND"); forced != "" {
		allowed = []keyring.BackendType{keyring.BackendType(forced)}
	}
	ring, err := keyring.Open(keyring.Config{
		ServiceName:              appName,
		AllowedBackends:          allowed,
		KeychainName:             "login",
		KeychainTrustApplication: true,
		LibSecretCollectionName:  appName,
		KWalletAppID:             appName,
		KWalletFolder:            appName,
		WinCredPrefix:            appName,
		FileDir:                  fileDir,
		FilePasswordFunc:         keyring.PromptFunc(prompt),
	})
	if err != nil {
		return nil, err
	}
	return &keyringSecrets{ring: ring}, nil
}

func (k *keyringSecrets) Get(profile string) (string, error) {
	item, err := k.ring.Get(profile)
	if errors.Is(err, keyring.ErrKeyNotFound) {
		return "", ErrSecretNotFound
	}
	if err != nil {
		return "", err
	}
	return string(item.Data), nil
}

func (k *keyringSecrets) Set(profile, secret string) error {
	return k.ring.Set(keyring.Item{
		Key:         profile,
		Data:        []byte(secret),
		Label:       appName + " " + profile,
		Description: "cdb CouchDB credential",
	})
}

func (k *keyringSecrets) Remove(profile string) error {
	err := k.ring.Remove(profile)
	if errors.Is(err, keyring.ErrKeyNotFound) {
		return ErrSecretNotFound
	}
	return err
}

func (k *keyringSecrets) List() ([]string, error) {
	names, err := k.ring.Keys()
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}

type memorySecrets struct {
	mu sync.Mutex
	m  map[string]string
}

// NewMemorySecrets returns a Secrets that lives only in memory. Tests use it;
// production code never does.
func NewMemorySecrets() Secrets { return &memorySecrets{m: map[string]string{}} }

func (s *memorySecrets) Get(profile string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[profile]
	if !ok {
		return "", ErrSecretNotFound
	}
	return v, nil
}

func (s *memorySecrets) Set(profile, secret string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[profile] = secret
	return nil
}

func (s *memorySecrets) Remove(profile string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[profile]; !ok {
		return ErrSecretNotFound
	}
	delete(s.m, profile)
	return nil
}

func (s *memorySecrets) List() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.m))
	for k := range s.m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, nil
}
