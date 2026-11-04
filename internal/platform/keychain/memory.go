package keychain

import (
	"fmt"
	"maps"
	"slices"
	"sync"
)

// Memory is an in-process Store for tests of other packages.
type Memory struct {
	mu      sync.Mutex
	secrets map[string]string
}

var _ Store = (*Memory)(nil)

// NewMemory returns an empty Memory store.
func NewMemory() *Memory {
	return &Memory{secrets: map[string]string{}}
}

// Get implements Store.
func (m *Memory) Get(name string) (string, error) {
	if name == "" {
		return "", errEmptyName
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.secrets[name]
	if !ok {
		return "", fmt.Errorf("%s: %w", name, ErrNotFound)
	}
	return v, nil
}

// Set implements Store.
func (m *Memory) Set(name, value string) error {
	if name == "" {
		return errEmptyName
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.secrets[name] = value
	return nil
}

// Delete implements Store.
func (m *Memory) Delete(name string) error {
	if name == "" {
		return errEmptyName
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.secrets[name]; !ok {
		return fmt.Errorf("%s: %w", name, ErrNotFound)
	}
	delete(m.secrets, name)
	return nil
}

// List implements Store.
func (m *Memory) List() ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Sorted(maps.Keys(m.secrets)), nil
}
