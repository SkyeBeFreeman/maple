package proc_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/SkyeBeFreeman/maple/internal/platform/proc"
	"github.com/SkyeBeFreeman/maple/internal/testutil"
)

// fakeLoginPrelude makes a fake shell behave like `<shell> -ilc <cmd>` whose
// startup files set PATH: it rejects any other flags, so a wrong invocation
// shows up as the fallback PATH instead of the expected one.
const fakeLoginPrelude = `[ "$1" = "-ilc" ] || exit 64
PATH=/fake/bin:/usr/bin
export PATH
`

func TestLoginShellPath(t *testing.T) {
	t.Setenv("PATH", "/cur/bin:/usr/bin")
	shell := writeShell(t, fakeLoginPrelude+`exec /bin/sh -c "$2"`)

	if got, want := proc.LoginShellPath(t.Context(), shell), "/fake/bin:/usr/bin"; got != want {
		t.Fatalf("LoginShellPath = %q, want %q", got, want)
	}
}

func TestLoginShellPathNoisyShell(t *testing.T) {
	t.Run("banner and rc errors", func(t *testing.T) {
		t.Setenv("PATH", "/cur/bin:/usr/bin")
		shell := writeShell(t, `echo 'Welcome!'
echo 'rc error' >&2
`+fakeLoginPrelude+`/bin/sh -c "$2"
echo 'Goodbye'`)

		if got, want := proc.LoginShellPath(t.Context(), shell), "/fake/bin:/usr/bin"; got != want {
			t.Fatalf("LoginShellPath = %q, want %q", got, want)
		}
	})

	t.Run("failing shell", func(t *testing.T) {
		t.Setenv("PATH", "/cur/bin:/usr/bin")
		shell := writeShell(t, "exit 1")

		got := proc.LoginShellPath(t.Context(), shell)
		if !strings.HasPrefix(got, os.Getenv("PATH")) || !strings.Contains(got, "/opt/homebrew/bin") {
			t.Fatalf("LoginShellPath = %q, want current PATH followed by /opt/homebrew/bin", got)
		}
		if want := "/cur/bin:/usr/bin:/opt/homebrew/bin:/usr/local/bin"; got != want {
			t.Fatalf("LoginShellPath = %q, want %q", got, want)
		}
	})
}

func TestLoginShellPathFallback(t *testing.T) {
	tests := []struct {
		name    string
		curPath string
		shell   func(t *testing.T) string
		want    string
	}{
		{
			name:    "empty shell",
			curPath: "/cur/bin",
			shell:   func(*testing.T) string { return "" },
			want:    "/cur/bin:/opt/homebrew/bin:/usr/local/bin",
		},
		{
			name:    "missing shell",
			curPath: "/cur/bin",
			shell:   func(t *testing.T) string { return filepath.Join(t.TempDir(), "no-such-shell") },
			want:    "/cur/bin:/opt/homebrew/bin:/usr/local/bin",
		},
		{
			name:    "empty login PATH",
			curPath: "/cur/bin",
			shell: func(t *testing.T) string {
				return writeShell(t, "PATH=\nexport PATH\n"+`exec /bin/sh -c "$2"`)
			},
			want: "/cur/bin:/opt/homebrew/bin:/usr/local/bin",
		},
		{
			name:    "unterminated marker",
			curPath: "/cur/bin",
			shell: func(t *testing.T) string {
				return writeShell(t, "printf '__MAPLE_PATH__/half/bin'")
			},
			want: "/cur/bin:/opt/homebrew/bin:/usr/local/bin",
		},
		{
			name:    "duplicates and empty entries removed",
			curPath: "/cur/bin:/usr/local/bin::/cur/bin",
			shell:   func(*testing.T) string { return "" },
			want:    "/cur/bin:/usr/local/bin:/opt/homebrew/bin",
		},
		{
			name:    "unset PATH",
			curPath: "",
			shell:   func(*testing.T) string { return "" },
			want:    "/opt/homebrew/bin:/usr/local/bin",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shell := tt.shell(t)
			t.Setenv("PATH", tt.curPath)

			if got := proc.LoginShellPath(t.Context(), shell); got != tt.want {
				t.Fatalf("LoginShellPath = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLoginShellPathKillsHungShell(t *testing.T) {
	t.Setenv("PATH", "/cur/bin")
	pidFile := filepath.Join(t.TempDir(), "pid")
	// The background sleep inherits stdout, so it also stands for anything an
	// rc file leaves running with the pipe open.
	shell := writeShell(t, "/bin/sleep 100 &\necho $! > '"+pidFile+"'\nwait")
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	start := time.Now()
	got := proc.LoginShellPath(ctx, shell)
	elapsed := time.Since(start)

	if want := "/cur/bin:/opt/homebrew/bin:/usr/local/bin"; got != want {
		t.Fatalf("LoginShellPath = %q, want %q", got, want)
	}
	// Below the 5s probe timeout, so only ctx can have ended the call.
	if elapsed > 4*time.Second {
		t.Fatalf("LoginShellPath took %v, want it to stop soon after ctx expires", elapsed)
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read background pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("parse background pid %q: %v", data, err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	testutil.WaitGone(t, pid, time.Second)
}

func TestLookPath(t *testing.T) {
	t.Run("found in second dir", func(t *testing.T) {
		first, second := t.TempDir(), t.TempDir()
		want := writeFile(t, filepath.Join(second, "tool"), 0o755)

		got, err := proc.LookPath("tool", first+":"+second)
		if err != nil {
			t.Fatalf("LookPath: %v", err)
		}
		if got != want || !filepath.IsAbs(got) {
			t.Fatalf("LookPath = %q, want %q", got, want)
		}
	})

	t.Run("skips non-executables, directories and relative entries", func(t *testing.T) {
		work := t.TempDir()
		t.Chdir(work)
		// Both would match if "" (the working directory) or "rel" were searched.
		writeFile(t, filepath.Join(work, "tool"), 0o755)
		writeFile(t, filepath.Join(work, "rel", "tool"), 0o755)
		noExec, isDir, target := t.TempDir(), t.TempDir(), t.TempDir()
		writeFile(t, filepath.Join(noExec, "tool"), 0o644)
		if err := os.Mkdir(filepath.Join(isDir, "tool"), 0o755); err != nil {
			t.Fatal(err)
		}
		want := writeFile(t, filepath.Join(target, "tool"), 0o755)

		got, err := proc.LookPath("tool", strings.Join([]string{"", "rel", noExec, isDir, target}, ":"))
		if err != nil {
			t.Fatalf("LookPath: %v", err)
		}
		if got != want {
			t.Fatalf("LookPath = %q, want %q", got, want)
		}
	})

	t.Run("not found", func(t *testing.T) {
		dir := t.TempDir()
		for _, pathEnv := range []string{dir, ""} {
			if got, err := proc.LookPath("tool", pathEnv); !errors.Is(err, exec.ErrNotFound) {
				t.Errorf("LookPath(%q) = %q, %v; want exec.ErrNotFound", pathEnv, got, err)
			}
		}
	})

	t.Run("path with slash is not searched", func(t *testing.T) {
		dir, other := t.TempDir(), t.TempDir()
		exe := writeFile(t, filepath.Join(dir, "tool"), 0o755)
		noExec := writeFile(t, filepath.Join(dir, "data"), 0o644)
		writeFile(t, filepath.Join(other, "tool"), 0o755)

		if got, err := proc.LookPath(exe, other); err != nil || got != exe {
			t.Errorf("LookPath(%q) = %q, %v; want itself", exe, got, err)
		}
		if got, err := proc.LookPath(noExec, other); err == nil {
			t.Errorf("LookPath(%q) = %q, want error for non-executable", noExec, got)
		}
		missing := filepath.Join(t.TempDir(), "tool")
		if got, err := proc.LookPath(missing, other); err == nil {
			t.Errorf("LookPath(%q) = %q, want error instead of searching pathEnv", missing, got)
		}
	})
}

// writeShell writes an executable fake shell and runs it once. macOS spends
// ~0.35s (more under load) checking a freshly written executable on its first
// run, which would otherwise eat into the timeouts of the call under test.
func writeShell(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "shell")
	script := "#!/bin/sh\n[ \"$1\" = warm ] && exit 0\n" + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command(path, "warm").Run(); err != nil {
		t.Fatalf("warm up fake shell: %v", err)
	}
	return path
}

func writeFile(t *testing.T, path string, perm os.FileMode) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), perm); err != nil {
		t.Fatal(err)
	}
	return path
}
