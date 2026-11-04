package keychain

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"slices"
	"sync"

	"github.com/SkyeBeFreeman/maple/internal/platform/atomicfile"
)

// NewFile returns a Store backed by a plaintext JSON file at path.
//
// For tests only, never for production: secrets are stored unencrypted. It
// exists so end-to-end tests can share secrets between separate maple
// processes, which is why every call rereads the file instead of caching.
// Concurrent writers in different processes may lose updates.
func NewFile(path string) Store {
	return &fileStore{path: path}
}

type fileStore struct {
	mu   sync.Mutex
	path string
}

func (f *fileStore) load() (map[string]string, error) {
	data, err := os.ReadFile(f.path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", f.path, err)
	}
	return m, nil
}

func (f *fileStore) save(m map[string]string) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(f.path, data, 0o600)
}

func (f *fileStore) Get(name string) (string, error) {
	if name == "" {
		return "", errEmptyName
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return "", err
	}
	v, ok := m[name]
	if !ok {
		return "", fmt.Errorf("%s: %w", name, ErrNotFound)
	}
	return v, nil
}

func (f *fileStore) Set(name, value string) error {
	if name == "" {
		return errEmptyName
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return err
	}
	m[name] = value
	return f.save(m)
}

func (f *fileStore) Delete(name string) error {
	if name == "" {
		return errEmptyName
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return err
	}
	if _, ok := m[name]; !ok {
		return fmt.Errorf("%s: %w", name, ErrNotFound)
	}
	delete(m, name)
	return f.save(m)
}

func (f *fileStore) List() ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return nil, err
	}
	return slices.Sorted(maps.Keys(m)), nil
}
