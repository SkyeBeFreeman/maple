package manage

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/SkyeBeFreeman/maple/internal/platform/keychain"
)

func contains(s, sub string) bool { return strings.Contains(s, sub) }

func TestSecretSetListRemove(t *testing.T) {
	e := setup(t, "")
	for _, bad := range []string{"", "lower", "1X", "A-B"} {
		if err := SecretSet(e, bad, "v"); err == nil {
			t.Errorf("SecretSet(%q) accepted", bad)
		}
	}
	if err := SecretSet(e, "A", ""); err == nil {
		t.Error("empty value accepted")
	}
	if err := SecretSet(e, "B", "2"); err != nil {
		t.Fatal(err)
	}
	if err := SecretSet(e, "A", "1"); err != nil {
		t.Fatal(err)
	}
	got, err := SecretList(e)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"A", "B"}) {
		t.Errorf("List = %v", got)
	}
	if err := SecretRemove(e, "A"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Keychain.Get("A"); !errors.Is(err, keychain.ErrNotFound) {
		t.Errorf("A still present: %v", err)
	}
	if err := SecretRemove(e, "A"); err == nil || !contains(err.Error(), "not found") {
		t.Errorf("remove missing err = %v", err)
	}
}

func TestSecretRemoveInUse(t *testing.T) {
	e := setup(t, "servers:\n  b:\n    url: https://x/${K}\n  a:\n    command: npx\n    env:\n      T: ${K}\n  c:\n    command: npx\n")
	if err := SecretSet(e, "K", "v"); err != nil {
		t.Fatal(err)
	}
	err := SecretRemove(e, "K")
	if err == nil || !contains(err.Error(), "a, b") {
		t.Fatalf("err = %v, want it to list a, b", err)
	}
	if _, err := e.Keychain.Get("K"); err != nil {
		t.Errorf("K removed: %v", err)
	}
}

// Without config.yaml nothing can reference the secret.
func TestSecretRemoveWithoutConfig(t *testing.T) {
	e := setup(t, "")
	if err := os.Remove(e.Paths.Config()); err != nil {
		t.Fatal(err)
	}
	if err := SecretSet(e, "K", "v"); err != nil {
		t.Fatal(err)
	}
	if err := SecretRemove(e, "K"); err != nil {
		t.Fatal(err)
	}
}
