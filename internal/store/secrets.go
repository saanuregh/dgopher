package store

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"

	"github.com/egoist/mygo"
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

// MygoSecrets returns Secrets kept in the system's credential store by
// MyGo, under service. A secret go-keyring (KeyringSecrets) kept moves
// there the first time it is read.
func MygoSecrets(service string) Secrets {
	// On macOS both keep a key in the same Keychain item (service and
	// account); elsewhere each has items of its own.
	return newMigratingSecrets(&mygoSecrets{store: mygo.Secrets.Service(service)},
		KeyringSecrets(service), runtime.GOOS == "darwin")
}

type mygoSecrets struct {
	store     mygo.SecretsModule
	probeOnce sync.Once
	available bool
}

// offThread runs fn on another goroutine and waits for it. MyGo's calls
// on the main thread handle the window's events while they wait, and a
// handler could then run another secrets call inside this one, as with
// migratingSecrets' lock held; off the main thread they just block, as
// go-keyring's did.
func offThread[T any](fn func() T) T {
	ch := make(chan T, 1)
	go func() { ch <- fn() }()
	return <-ch
}

func (m *mygoSecrets) Get(key string) (string, error) {
	type result struct {
		v   []byte
		err error
	}
	r := offThread(func() result {
		v, err := m.store.Get(key)
		return result{v, err}
	})
	if errors.Is(r.err, mygo.ErrSecretNotFound) {
		return "", ErrNotFound
	}
	return string(r.v), r.err
}

func (m *mygoSecrets) Set(key, value string) error {
	return offThread(func() error { return m.store.Set(key, []byte(value)) })
}

func (m *mygoSecrets) Delete(key string) error {
	return offThread(func() error { return m.store.Delete(key) })
}

func (m *mygoSecrets) Available() bool {
	m.probeOnce.Do(func() {
		_, err := m.Get(keyringProbeKey)
		m.available = err == nil || errors.Is(err, ErrNotFound)
	})
	return m.available
}

// migratingSecrets keeps secrets in current, moving each one old kept
// there when it is read. Its old item goes only once current reads back
// the same value, so that no secret is lost on the way.
type migratingSecrets struct {
	// mu keeps a Set from landing between a move's read and write, where
	// the move would put the old value over it.
	mu           sync.Mutex
	current, old Secrets
	// sameItem is whether both keep a key in one item, as on macOS: the
	// old store is then never asked, since deleting there deletes it.
	sameItem bool
}

func newMigratingSecrets(current, old Secrets, sameItem bool) Secrets {
	return &migratingSecrets{current: current, old: old, sameItem: sameItem}
}

// go-keyring's prefixes of what it stores on macOS, which its Get strips.
const (
	keyringHexPrefix    = "go-keyring-encoded:"
	keyringBase64Prefix = "go-keyring-base64:"
)

// fallback reports whether only the old store works, as on Linux without
// libsecret, which go-keyring does without: it keeps the secrets then.
func (s *migratingSecrets) fallback() bool {
	return !s.current.Available() && s.old.Available()
}

func (s *migratingSecrets) Get(key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fallback() {
		return s.old.Get(key)
	}
	v, err := s.current.Get(key)
	switch {
	case err == nil && s.sameItem:
		return s.decodeInPlace(key, v)
	case err == nil, !errors.Is(err, ErrNotFound), s.sameItem:
		return v, err
	}
	v, err = s.old.Get(key)
	if err != nil {
		return "", err
	}
	if s.current.Set(key, v) != nil {
		return v, nil
	}
	if back, err := s.current.Get(key); err == nil && back == v {
		s.old.Delete(key) // when it fails, the old item just stays
	}
	return v, nil
}

// decodeInPlace decodes a value go-keyring stored in the shared item, and
// writes it back plain. When the write fails, the item keeps the encoded
// value, which go-keyring could still read.
func (s *migratingSecrets) decodeInPlace(key, v string) (string, error) {
	var plain []byte
	var err error
	switch {
	case strings.HasPrefix(v, keyringBase64Prefix):
		plain, err = base64.StdEncoding.DecodeString(v[len(keyringBase64Prefix):])
	case strings.HasPrefix(v, keyringHexPrefix):
		plain, err = hex.DecodeString(v[len(keyringHexPrefix):])
	default:
		return v, nil
	}
	if err != nil {
		// Not wrapped: hex's error quotes the byte, a part of the secret.
		return "", fmt.Errorf("store: the keychain item of %s is not in go-keyring's encoding", key)
	}
	s.current.Set(key, string(plain))
	return string(plain), nil
}

func (s *migratingSecrets) Set(key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fallback() {
		return s.old.Set(key, value)
	}
	return s.current.Set(key, value)
}

func (s *migratingSecrets) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fallback() {
		return s.old.Delete(key)
	}
	err := s.current.Delete(key)
	if s.sameItem {
		return err
	}
	return errors.Join(err, s.old.Delete(key))
}

func (s *migratingSecrets) Available() bool { return s.current.Available() || s.old.Available() }

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
