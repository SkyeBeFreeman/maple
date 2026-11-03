package config

import (
	"errors"
	"io/fs"
	"os"
	"regexp"
	"testing"
)

func TestEnsureTokenOnce(t *testing.T) {
	p := Paths{Home: t.TempDir()}
	tok, created, err := EnsureToken(p)
	if err != nil || !created {
		t.Fatalf("EnsureToken first = %q, %v, %v; want created", tok, created, err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(tok) {
		t.Errorf("token %q is not 64 lowercase hex characters", tok)
	}
	if got := mode(t, p.Token()); got != 0o600 {
		t.Errorf("token mode = %o, want 600", got)
	}
	if got := mode(t, p.MapleDir()); got != 0o700 {
		t.Errorf("~/.maple mode = %o, want 700", got)
	}
	tok2, created2, err := EnsureToken(p)
	if err != nil || created2 || tok2 != tok {
		t.Errorf("EnsureToken second = %q, %v, %v; want same token, created false", tok2, created2, err)
	}
	loaded, err := LoadToken(p)
	if err != nil || loaded != tok {
		t.Errorf("LoadToken = %q, %v; want %q", loaded, err, tok)
	}
}

func TestLoadTokenTrims(t *testing.T) {
	p := Paths{Home: t.TempDir()}
	_ = os.MkdirAll(p.MapleDir(), 0o700)
	_ = os.WriteFile(p.Token(), []byte("  abc\n"), 0o600)
	if got, err := LoadToken(p); err != nil || got != "abc" {
		t.Errorf("LoadToken = %q, %v; want abc", got, err)
	}
	if _, err := LoadToken(Paths{Home: t.TempDir()}); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("LoadToken missing: err = %v, want fs.ErrNotExist", err)
	}
}

// An empty token would let an empty bearer through the gateway's compare.
func TestEmptyTokenRejectedAndReplaced(t *testing.T) {
	p := Paths{Home: t.TempDir()}
	_ = os.MkdirAll(p.MapleDir(), 0o700)
	_ = os.WriteFile(p.Token(), []byte("\n"), 0o600)
	if _, err := LoadToken(p); err == nil {
		t.Error("LoadToken accepted an empty token file")
	}
	tok, created, err := EnsureToken(p)
	if err != nil || !created || len(tok) != 64 {
		t.Errorf("EnsureToken over empty file = %q, %v, %v; want new token", tok, created, err)
	}
}
