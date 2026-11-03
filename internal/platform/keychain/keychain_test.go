package keychain

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func exerciseStore(t *testing.T, s Store) {
	t.Helper()
	if err := s.Set("", "x"); err == nil {
		t.Fatal("Set with empty name succeeded")
	}
	if _, err := s.Get(""); err == nil {
		t.Fatal("Get with empty name succeeded")
	}
	if err := s.Delete(""); err == nil {
		t.Fatal("Delete with empty name succeeded")
	}
	if _, err := s.Get("MISSING"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get missing: got %v, want ErrNotFound", err)
	}
	for name, v := range map[string]string{"B_KEY": "b1", "A_KEY": "a1"} {
		if err := s.Set(name, v); err != nil {
			t.Fatalf("Set %s: %v", name, err)
		}
	}
	// Overwrite must succeed rather than fail as a duplicate.
	if err := s.Set("B_KEY", "b2"); err != nil {
		t.Fatalf("Set overwrite: %v", err)
	}
	if v, err := s.Get("B_KEY"); err != nil || v != "b2" {
		t.Fatalf("Get B_KEY = %q, %v; want b2", v, err)
	}
	names, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if want := []string{"A_KEY", "B_KEY"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("List = %v, want %v", names, want)
	}
	if err := s.Delete("A_KEY"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get("A_KEY"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete: got %v, want ErrNotFound", err)
	}
	if err := s.Delete("A_KEY"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete missing: got %v, want ErrNotFound", err)
	}
}

func TestMemoryStore(t *testing.T) {
	exerciseStore(t, NewMemory())
}

func TestFileStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	s := NewFile(path)
	exerciseStore(t, s)

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("perm = %o, want 600", perm)
	}
	if v, err := NewFile(path).Get("B_KEY"); err != nil || v != "b2" {
		t.Fatalf("second instance Get = %q, %v; want b2", v, err)
	}
}

func TestDarwinStore(t *testing.T) {
	if os.Getenv("MAPLE_TEST_KEYCHAIN") != "1" {
		t.Skip("set MAPLE_TEST_KEYCHAIN=1 to touch the real login keychain")
	}
	s := New("maple-test")
	t.Cleanup(func() {
		names, _ := s.List()
		for _, n := range names {
			_ = s.Delete(n)
		}
	})
	exerciseStore(t, s)
}
