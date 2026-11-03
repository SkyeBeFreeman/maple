package atomicfile

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"syscall"
	"time"
)

const lockRetryInterval = 50 * time.Millisecond

// LockedError reports that the lock was still held when ctx ended.
type LockedError struct {
	// PID of the holder as recorded in the lock file; 0 if it could not be read.
	PID int
}

func (e *LockedError) Error() string {
	if e.PID == 0 {
		return "locked by another process"
	}
	return fmt.Sprintf("locked by process %d", e.PID)
}

// Lock takes an exclusive flock on path, creating the file with mode 0600 if
// needed, and polls until it succeeds or ctx ends, in which case it returns a
// *LockedError. The holder's pid is written into the file so that waiters can
// name it. unlock must be called exactly once.
func Lock(ctx context.Context, path string) (unlock func() error, err error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	ticker := time.NewTicker(lockRetryInterval)
	defer ticker.Stop()
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			_ = f.Close()
			return nil, &fs.PathError{Op: "flock", Path: path, Err: err}
		}
		select {
		case <-ctx.Done():
			pid := readPID(f)
			_ = f.Close()
			return nil, &LockedError{PID: pid}
		case <-ticker.C:
		}
	}

	unlock = func() error {
		// Explicit LOCK_UN: a forked child that still shares the descriptor
		// would otherwise keep the lock alive after Close.
		return errors.Join(syscall.Flock(int(f.Fd()), syscall.LOCK_UN), f.Close())
	}
	if err := writePID(f); err != nil {
		_ = unlock()
		return nil, &fs.PathError{Op: "write pid", Path: path, Err: err}
	}
	return unlock, nil
}

// writePID overwrites before truncating so that a concurrent readPID, which
// only parses the first line, sees either the old or the new pid in full.
func writePID(f *os.File) error {
	b := []byte(strconv.Itoa(os.Getpid()) + "\n")
	if _, err := f.WriteAt(b, 0); err != nil {
		return err
	}
	return f.Truncate(int64(len(b)))
}

func readPID(f *os.File) int {
	buf := make([]byte, 32)
	n, _ := f.ReadAt(buf, 0)
	line, _, _ := bytes.Cut(buf[:n], []byte("\n"))
	pid, err := strconv.Atoi(string(bytes.TrimSpace(line)))
	if err != nil {
		return 0
	}
	return pid
}
