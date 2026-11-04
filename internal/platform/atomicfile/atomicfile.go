// Package atomicfile replaces files atomically without breaking symlinks and
// provides an advisory cross-process file lock.
package atomicfile

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// ErrModified reports that the destination changed after the caller read it;
// the caller should re-read, recompute and retry.
var ErrModified = errors.New("file modified concurrently")

// Same limit as MAXSYMLINKS on macOS, so a chain the kernel would open is
// never rejected here.
const maxSymlinks = 32

// WriteFile replaces the file at path with data. Readers see either the old or
// the new content, never a partial write. If path is a symlink (or a chain of
// them, possibly dangling), the final target is replaced and the links are
// kept. The new file gets exactly perm, regardless of umask and of the old
// file's mode.
func WriteFile(path string, data []byte, perm fs.FileMode) error {
	return replace(path, data, perm, nil)
}

// WriteFileIfDifferent is WriteFile that leaves a file already holding data
// alone, so reruns report no change and do not touch its mtime. Missing
// parent directories are created with dirPerm. It reports whether it wrote.
func WriteFileIfDifferent(path string, data []byte, perm, dirPerm fs.FileMode) (bool, error) {
	cur, err := os.ReadFile(path)
	if err == nil && bytes.Equal(cur, data) {
		return false, nil
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		return false, err
	}
	return true, WriteFile(path, data, perm)
}

// WriteFileIfUnchanged is WriteFile that refuses, with ErrModified, to replace
// a file whose ModTime or Size differs from prev. prev must come from os.Stat
// (not Lstat) on path; nil means the file did not exist, so finding it present
// is a modification too. The check runs just before the rename, which narrows
// but cannot close the race window.
func WriteFileIfUnchanged(path string, data []byte, perm fs.FileMode, prev fs.FileInfo) error {
	return replace(path, data, perm, func(target string) error {
		if unchanged(target, prev) {
			return nil
		}
		return &fs.PathError{Op: "write", Path: path, Err: ErrModified}
	})
}

func unchanged(target string, prev fs.FileInfo) bool {
	cur, err := os.Stat(target)
	if prev == nil {
		return errors.Is(err, fs.ErrNotExist)
	}
	return err == nil && cur.Size() == prev.Size() && cur.ModTime().Equal(prev.ModTime())
}

func replace(path string, data []byte, perm fs.FileMode, beforeRename func(target string) error) (err error) {
	target, err := resolveTarget(path)
	if err != nil {
		return err
	}
	// Same directory as the target: rename is only atomic within one
	// filesystem.
	tmp, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".*.tmp")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if err = tmp.Chmod(perm); err != nil {
		return err
	}
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	// Without the sync a crash after rename can leave an empty file in place
	// of both the old and the new content.
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if beforeRename != nil {
		if err = beforeRename(target); err != nil {
			return err
		}
	}
	return os.Rename(tmp.Name(), target)
}

// resolveTarget returns the file a write through path must replace. Unlike
// filepath.EvalSymlinks it accepts a dangling final link, so a symlinked config
// can be created on first write.
func resolveTarget(path string) (string, error) {
	cur := path
	for range maxSymlinks {
		fi, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			return cur, nil
		}
		if err != nil {
			return "", err
		}
		if fi.Mode()&fs.ModeSymlink == 0 {
			return cur, nil
		}
		dest, err := os.Readlink(cur)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(dest) {
			// Resolve the link's directory first: joining lexically would
			// mis-handle a ".." in dest when that directory is itself reached
			// through a symlink.
			dir, err := filepath.EvalSymlinks(filepath.Dir(cur))
			if err != nil {
				return "", err
			}
			dest = filepath.Join(dir, dest)
		}
		cur = dest
	}
	return "", &fs.PathError{Op: "resolve", Path: path, Err: syscall.ELOOP}
}
