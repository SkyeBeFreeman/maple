package config

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SkyeBeFreeman/maple/internal/platform/atomicfile"
)

func mode(t *testing.T, path string) fs.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return fi.Mode().Perm()
}

func TestSaveLoadRoundTrip(t *testing.T) {
	p := Paths{Home: t.TempDir()}
	in, err := Parse([]byte(specExample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := Save(p, in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, raw, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Errorf("round trip mismatch:\n in=%+v\nout=%+v", in, out)
	}
	disk, _ := os.ReadFile(p.Config())
	if string(raw) != string(disk) {
		t.Error("Load raw bytes differ from file content")
	}
	// Same indentation as the spec example, so hand edits look familiar.
	if !strings.Contains(string(disk), "\n  demo-http:\n    url: ") {
		t.Errorf("config.yaml not indented by 2:\n%s", disk)
	}
	if got := mode(t, p.Config()); got != 0o600 {
		t.Errorf("config.yaml mode = %o, want 600", got)
	}
	if got := mode(t, p.MapleDir()); got != 0o700 {
		t.Errorf("~/.maple mode = %o, want 700", got)
	}
}

func TestSaveRejectsInvalid(t *testing.T) {
	p := Paths{Home: t.TempDir()}
	c := &Config{Port: DefaultPort, Servers: map[string]*Server{"x": {}}}
	if err := Save(p, c); err == nil {
		t.Fatal("Save accepted an invalid config")
	}
	if _, err := os.Stat(p.Config()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("invalid config was written: %v", err)
	}
}

func TestLoadMissing(t *testing.T) {
	_, _, err := Load(Paths{Home: t.TempDir()})
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Load missing: err = %v, want fs.ErrNotExist", err)
	}
}

func TestLoadInvalidHasLine(t *testing.T) {
	p := Paths{Home: t.TempDir()}
	if err := os.MkdirAll(p.MapleDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Config(), []byte("servers:\n  x:\n    url: u\n    mode: shared\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, raw, err := Load(p)
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Line != 4 {
		t.Fatalf("Load err = %v, want ValidationError at line 4", err)
	}
	// The gateway hashes what it tried to load even when it is invalid.
	if len(raw) == 0 {
		t.Error("Load returned no raw bytes for an invalid file")
	}
}

func TestLock(t *testing.T) {
	p := Paths{Home: t.TempDir()}
	unlock, err := Lock(context.Background(), p)
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if got := mode(t, p.MapleDir()); got != 0o700 {
		t.Errorf("~/.maple mode = %o, want 700", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = Lock(ctx, p)
	var le *atomicfile.LockedError
	if !errors.As(err, &le) {
		t.Fatalf("second Lock: err = %v, want *LockedError", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("Lock ignored the caller's shorter deadline")
	}
	if err := unlock(); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	if _, err := os.Stat(filepath.Join(p.MapleDir(), "lock")); err != nil {
		t.Errorf("lock file: %v", err)
	}
}

// Without a deadline from the caller, Lock must still give up on its own.
func TestLockTimesOutWithoutCallerDeadline(t *testing.T) {
	p := Paths{Home: t.TempDir()}
	unlock, err := Lock(context.Background(), p)
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	defer func() { _ = unlock() }()

	old := lockTimeout
	lockTimeout = 100 * time.Millisecond
	defer func() { lockTimeout = old }()
	start := time.Now()
	_, err = Lock(context.Background(), p)
	var le *atomicfile.LockedError
	if !errors.As(err, &le) {
		t.Fatalf("Lock while held: err = %v, want *LockedError", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("Lock took %v, want about lockTimeout", d)
	}
}

func TestLockTimeoutIs10s(t *testing.T) {
	if lockTimeout != 10*time.Second {
		t.Errorf("lockTimeout = %v, want 10s", lockTimeout)
	}
}
