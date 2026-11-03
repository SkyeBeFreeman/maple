package manage

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"slices"
	"strings"

	"github.com/SkyeBeFreeman/maple/internal/config"
	"github.com/SkyeBeFreeman/maple/internal/platform/keychain"
)

// SecretSet stores a secret for ${name} references.
func SecretSet(e Env, name, value string) error {
	if !config.IsSecretName(name) {
		return fmt.Errorf("invalid secret name %q: use A-Z, 0-9 and _, not starting with a digit", name)
	}
	if value == "" {
		return fmt.Errorf("secret %s: value is empty", name)
	}
	if err := e.Keychain.Set(name, value); err != nil {
		return fmt.Errorf("store secret %s: %w", name, err)
	}
	return nil
}

// SecretList returns the stored secret names, sorted; never the values.
func SecretList(e Env) ([]string, error) {
	names, err := e.Keychain.List()
	if err != nil {
		return nil, fmt.Errorf("list secrets: %w", err)
	}
	return names, nil
}

// SecretRemove deletes a secret unless a server still references it.
func SecretRemove(e Env, name string) error {
	c, _, err := config.Load(e.Paths)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return fmt.Errorf("cannot check which servers use %s: %w", name, err)
	default:
		var users []string
		for _, server := range slices.Sorted(maps.Keys(c.Servers)) {
			if slices.Contains(c.Servers[server].SecretRefs(), name) {
				users = append(users, server)
			}
		}
		if len(users) > 0 {
			return fmt.Errorf("secret %s is used by servers %s; remove those references first", name, strings.Join(users, ", "))
		}
	}
	if err := e.Keychain.Delete(name); err != nil {
		if errors.Is(err, keychain.ErrNotFound) {
			return fmt.Errorf("secret %s not found; run: maple secret list", name)
		}
		return fmt.Errorf("remove secret %s: %w", name, err)
	}
	return nil
}
