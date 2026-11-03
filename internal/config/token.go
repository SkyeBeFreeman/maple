package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/SkyeBeFreeman/maple/internal/platform/atomicfile"
)

var errEmptyToken = errors.New("token file is empty")

// EnsureToken returns the gateway token, creating it if it does not exist yet
// or is empty.
func EnsureToken(p Paths) (token string, created bool, err error) {
	tok, err := LoadToken(p)
	if err == nil {
		return tok, false, nil
	}
	if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, errEmptyToken) {
		return "", false, err
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", false, err
	}
	tok = hex.EncodeToString(buf)
	if err := ensureDir(p); err != nil {
		return "", false, err
	}
	if err := atomicfile.WriteFile(p.Token(), []byte(tok+"\n"), 0o600); err != nil {
		return "", false, err
	}
	return tok, true, nil
}

// LoadToken reads the gateway token. A missing file yields an error matching
// fs.ErrNotExist.
func LoadToken(p Paths) (string, error) {
	data, err := os.ReadFile(p.Token())
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(data))
	if tok == "" {
		// An empty token would make an empty bearer header authenticate.
		return "", fmt.Errorf("%s: %w", p.Token(), errEmptyToken)
	}
	return tok, nil
}
