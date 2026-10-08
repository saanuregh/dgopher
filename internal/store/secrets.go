package store

import (
	"errors"
	"sync"

	"github.com/zalando/go-keyring"
)

// ErrNotFound reports a missing secret or store file.
var ErrNotFound = errors.New("store: not found")

// Secrets stores credentials outside the config files.
type Secrets interface {
	Get(key string) (string, error) // ErrNotFound when absent
	Set(key, value string) error
	Delete(key string) error // nil when absent
	Available() bool         // false when the OS keychain cannot be used
}

const keyringProbeKey = "dgopher-availability-probe"

type keyringSecrets struct {
	service   string
	probeOnce sync.Once
	available bool
}

// KeyringSecrets returns Secrets backed by the OS keychain.
func KeyringSecrets(service string) Secrets {
	return &keyringSecrets{service: service}
}

func (k *keyringSecrets) Get(key string) (string, error) {
	v, err := keyring.Get(k.service, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	return v, err
}

func (k *keyringSecrets) Set(key, value string) error {
	return keyring.Set(k.service, key, value)
}

func (k *keyringSecrets) Delete(key string) error {
	err := keyring.Delete(k.service, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

func (k *keyringSecrets) Available() bool {
	k.probeOnce.Do(func() {
		_, err := keyring.Get(k.service, keyringProbeKey)
		k.available = err == nil || errors.Is(err, keyring.ErrNotFound)
	})
	return k.available
}

type memorySecrets struct {
	mu     sync.Mutex
	values map[string]string
}

// MemorySecrets returns an in-process Secrets for tests and keychain-less sessions.
func MemorySecrets() Secrets {
	return &memorySecrets{values: map[string]string{}}
}

func (m *memorySecrets) Get(key string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.values[key]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}

func (m *memorySecrets) Set(key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.values[key] = value
	return nil
}

func (m *memorySecrets) Delete(key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.values, key)
	return nil
}

func (m *memorySecrets) Available() bool { return true }
