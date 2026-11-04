package atomicfile_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/SkyeBeFreeman/maple/internal/platform/atomicfile"
)

func TestWriteFileReplacesAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	// Different modes per write prove perm is applied on every replacement,
	// not just inherited from the temp file's creation mode.
	if err := atomicfile.WriteFile(path, []byte("first\n"), 0o644); err != nil {
		t.Fatalf("first WriteFile: %v", err)
	}
	assertPerm(t, path, 0o644)
	if err := atomicfile.WriteFile(path, []byte("second\n"), 0o600); err != nil {
		t.Fatalf("second WriteFile: %v", err)
	}

	assertContent(t, path, "second\n")
	assertPerm(t, path, 0o600)
	assertDirEntries(t, dir, "config.yaml")
}

func TestWriteFileIfDifferent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sub")
	path := filepath.Join(dir, "f")
	for i, tc := range []struct {
		data  string
		wrote bool
	}{{"a", true}, {"a", false}, {"b", true}} {
		wrote, err := atomicfile.WriteFileIfDifferent(path, []byte(tc.data), 0o600, 0o700)
		if err != nil || wrote != tc.wrote {
			t.Fatalf("write %d = %v, %v; want %v", i, wrote, err, tc.wrote)
		}
		assertContent(t, path, tc.data)
	}
	assertPerm(t, dir, 0o700)
	assertPerm(t, path, 0o600)
}

func TestWriteFileCreatesMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.json")

	if err := atomicfile.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	assertContent(t, path, "{}")
	assertPerm(t, path, 0o600)
	assertDirEntries(t, dir, "new.json")
}

func TestWriteFileFollowsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real", "target.json")
	mkdir(t, filepath.Dir(target))
	writeRaw(t, target, "old")
	link := filepath.Join(dir, "link")
	linkDest := filepath.Join("real", "target.json")
	if err := os.Symlink(linkDest, link); err != nil {
		t.Fatal(err)
	}

	if err := atomicfile.WriteFile(link, []byte("new"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	assertSymlink(t, link, linkDest)
	assertContent(t, target, "new")
	assertDirEntries(t, dir, "link", "real")
	assertDirEntries(t, filepath.Dir(target), "target.json")
}

func TestWriteFileDanglingSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real", "target.json")
	mkdir(t, filepath.Dir(target))
	link := filepath.Join(dir, "link")
	linkDest := filepath.Join("real", "target.json")
	if err := os.Symlink(linkDest, link); err != nil {
		t.Fatal(err)
	}

	if err := atomicfile.WriteFile(link, []byte("new"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	assertSymlink(t, link, linkDest)
	assertContent(t, target, "new")
}

func TestWriteFileSymlinkChain(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	writeRaw(t, target, "old")
	first := filepath.Join(dir, "first")
	second := filepath.Join(dir, "second")
	if err := os.Symlink(target, second); err != nil { // absolute
		t.Fatal(err)
	}
	if err := os.Symlink("second", first); err != nil { // relative
		t.Fatal(err)
	}

	if err := atomicfile.WriteFile(first, []byte("new"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	assertSymlink(t, first, "second")
	assertSymlink(t, second, target)
	assertContent(t, target, "new")
}

// The link sits in a directory reached through a symlink, so ".." in its
// destination must be taken from the real directory, as the kernel does.
func TestWriteFileSymlinkDotDotInLinkedDir(t *testing.T) {
	dir := t.TempDir()
	mkdir(t, filepath.Join(dir, "x", "y"))
	if err := os.Symlink(filepath.Join("x", "y"), filepath.Join(dir, "d")); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "d", "link")
	if err := os.Symlink(filepath.Join("..", "t"), link); err != nil {
		t.Fatal(err)
	}

	if err := atomicfile.WriteFile(link, []byte("new"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	assertContent(t, filepath.Join(dir, "x", "t"), "new")
	assertDirEntries(t, dir, "d", "x")
}

func TestWriteFileSymlinkLoop(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	if err := os.Symlink("b", a); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a", filepath.Join(dir, "b")); err != nil {
		t.Fatal(err)
	}

	err := atomicfile.WriteFile(a, []byte("x"), 0o600)
	if !errors.Is(err, syscall.ELOOP) {
		t.Fatalf("WriteFile on a symlink loop: err = %v, want ELOOP", err)
	}
	assertDirEntries(t, dir, "a", "b")
}

func TestWriteFileIfUnchangedDetectsChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	writeRaw(t, path, "{}")
	prev, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Another writer changes the file between our Stat and our write.
	writeRaw(t, path, `{"user":true}`)

	err = atomicfile.WriteFileIfUnchanged(path, []byte(`{"maple":true}`), 0o600, prev)
	if !errors.Is(err, atomicfile.ErrModified) {
		t.Fatalf("err = %v, want ErrModified", err)
	}

	assertContent(t, path, `{"user":true}`)
	assertDirEntries(t, dir, "mcp.json")
}

func TestWriteFileIfUnchanged(t *testing.T) {
	tests := []struct {
		name string
		// setup prepares dir and returns the path to write and the prev info.
		setup       func(t *testing.T, dir string) (path string, prev fs.FileInfo)
		wantErr     error
		wantContent string // "" means the file must not exist
	}{
		{
			name: "unchanged file is replaced",
			setup: func(t *testing.T, dir string) (string, fs.FileInfo) {
				path := filepath.Join(dir, "f")
				writeRaw(t, path, "old")
				return path, stat(t, path)
			},
			wantContent: "new",
		},
		{
			name: "unchanged symlink target is replaced",
			setup: func(t *testing.T, dir string) (string, fs.FileInfo) {
				writeRaw(t, filepath.Join(dir, "target"), "old")
				link := filepath.Join(dir, "f")
				if err := os.Symlink("target", link); err != nil {
					t.Fatal(err)
				}
				return link, stat(t, link)
			},
			wantContent: "new",
		},
		{
			name: "nil prev creates missing file",
			setup: func(_ *testing.T, dir string) (string, fs.FileInfo) {
				return filepath.Join(dir, "f"), nil
			},
			wantContent: "new",
		},
		{
			name: "nil prev but file appeared",
			setup: func(t *testing.T, dir string) (string, fs.FileInfo) {
				path := filepath.Join(dir, "f")
				writeRaw(t, path, "theirs")
				return path, nil
			},
			wantErr:     atomicfile.ErrModified,
			wantContent: "theirs",
		},
		{
			name: "file removed after stat",
			setup: func(t *testing.T, dir string) (string, fs.FileInfo) {
				path := filepath.Join(dir, "f")
				writeRaw(t, path, "old")
				prev := stat(t, path)
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				return path, prev
			},
			wantErr: atomicfile.ErrModified,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path, prev := tt.setup(t, dir)

			err := atomicfile.WriteFileIfUnchanged(path, []byte("new"), 0o600, prev)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}

			if tt.wantContent == "" {
				if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("Stat(%s): err = %v, want not exist", path, err)
				}
				return
			}
			assertContent(t, path, tt.wantContent)
		})
	}
}

func mkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
}

func writeRaw(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func stat(t *testing.T, path string) fs.FileInfo {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi
}

func assertContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("content of %s = %q, want %q", path, got, want)
	}
}

func assertPerm(t *testing.T, path string, want fs.FileMode) {
	t.Helper()
	if got := stat(t, path).Mode().Perm(); got != want {
		t.Errorf("perm of %s = %v, want %v", path, got, want)
	}
}

func assertSymlink(t *testing.T, link, wantDest string) {
	t.Helper()
	got, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("Readlink(%s): %v", link, err)
	}
	if got != wantDest {
		t.Errorf("Readlink(%s) = %q, want %q", link, got, wantDest)
	}
}

// assertDirEntries catches leftover temp files from a write.
func assertDirEntries(t *testing.T, dir string, want ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(entries))
	for _, e := range entries {
		got = append(got, e.Name())
	}
	if len(got) != len(want) {
		t.Fatalf("entries of %s = %q, want %q", dir, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("entries of %s = %q, want %q", dir, got, want)
		}
	}
}
