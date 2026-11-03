package atomicfile_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/SkyeBeFreeman/maple/internal/platform/atomicfile"
)

func TestLockContention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")

	// flock binds to the open file description, so two Lock calls in one
	// process contend exactly like two processes would.
	unlockA, err := atomicfile.Lock(context.Background(), path)
	if err != nil {
		t.Fatalf("Lock A: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = atomicfile.Lock(ctx, path)
	var locked *atomicfile.LockedError
	if !errors.As(err, &locked) {
		t.Fatalf("Lock B while A holds it: err = %v, want *LockedError", err)
	}
	if locked.PID != os.Getpid() {
		t.Errorf("LockedError.PID = %d, want %d", locked.PID, os.Getpid())
	}

	if err := unlockA(); err != nil {
		t.Fatalf("unlock A: %v", err)
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	unlockB, err := atomicfile.Lock(ctx2, path)
	if err != nil {
		t.Fatalf("Lock B after A released: %v", err)
	}
	if err := unlockB(); err != nil {
		t.Fatalf("unlock B: %v", err)
	}
}

func TestLockRecordsPID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")

	unlock, err := atomicfile.Lock(context.Background(), path)
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	t.Cleanup(func() { _ = unlock() })

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := strconv.Itoa(os.Getpid()) + "\n"; string(got) != want {
		t.Errorf("lock file content = %q, want %q", got, want)
	}
	assertPerm(t, path, 0o600)
}
