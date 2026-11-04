// Package keychain stores upstream MCP secrets by name.
package keychain

import "errors"

// ErrNotFound reports that no secret with the given name exists.
var ErrNotFound = errors.New("secret not found")

// ErrDenied reports that the user refused, or the process lacks, access.
var ErrDenied = errors.New("keychain access denied")

// errEmptyName guards every Store: in the keychain an empty account drops out
// of the query and matches every item of the service, so Delete("") would
// wipe all secrets.
var errEmptyName = errors.New("keychain: empty secret name")

// Store is a named secret store. Get and Delete return an error matching
// ErrNotFound when the name is absent; List returns names sorted.
type Store interface {
	Get(name string) (string, error)
	Set(name, value string) error
	Delete(name string) error
	List() ([]string, error)
}
