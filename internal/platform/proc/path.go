package proc

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	pathMarker = "__MAPLE_PATH__"
	// Markers delimit the value because startup files may print banners or
	// errors to stdout before and after it.
	pathProbe         = `printf "` + pathMarker + `%s` + pathMarker + `" "$PATH"`
	loginShellTimeout = 5 * time.Second
	// Something started by a startup file can inherit stdout and keep the pipe
	// open after the shell is gone; stop waiting for it after this long.
	pipeCloseDelay = 500 * time.Millisecond
)

// Homebrew's bin directories on Apple silicon and Intel: where most tools
// live that launchd's default PATH does not cover.
var fallbackDirs = []string{"/opt/homebrew/bin", "/usr/local/bin"}

// LoginShellPath returns the PATH that shell ends up with after reading the
// user's login and interactive startup files, as in a new terminal. launchd
// starts maple with a minimal PATH, which would hide tools installed through
// Homebrew or nvm (npx, uvx, ...).
//
// If shell is empty, fails, runs longer than 5s or prints no PATH, the result
// is the current PATH followed by the Homebrew directories, without
// duplicates or empty entries.
func LoginShellPath(ctx context.Context, shell string) string {
	if path, ok := loginShellPath(ctx, shell); ok {
		return path
	}
	return fallbackPath()
}

func loginShellPath(ctx context.Context, shell string) (string, bool) {
	if shell == "" {
		return "", false
	}
	ctx, cancel := context.WithTimeout(ctx, loginShellTimeout)
	defer cancel()

	var stdout bytes.Buffer
	cmd := exec.CommandContext(ctx, shell, "-ilc", pathProbe)
	cmd.Stdout = &stdout
	// Without a controlling terminal an interactive shell cannot take the
	// terminal away from maple's caller, nor stop on SIGTTOU trying to.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	// The session leader also leads a new process group, so this takes down
	// whatever the startup files spawned, not just the shell.
	cmd.Cancel = func() error { return KillGroup(cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = pipeCloseDelay
	// The exit status is deliberately ignored: complete markers prove printf
	// ran, and a failing logout file must not throw away a good PATH.
	_ = cmd.Run()

	_, rest, ok := strings.Cut(stdout.String(), pathMarker)
	if !ok {
		return "", false
	}
	path, _, ok := strings.Cut(rest, pathMarker)
	if !ok || path == "" {
		return "", false
	}
	return path, true
}

func fallbackPath() string {
	dirs := append(filepath.SplitList(os.Getenv("PATH")), fallbackDirs...)
	seen := make(map[string]bool, len(dirs))
	kept := dirs[:0]
	for _, dir := range dirs {
		// An empty entry means the working directory, which is meaningless for
		// a daemon; dropping it also avoids a leading ":" when PATH is unset.
		if dir == "" || seen[dir] {
			continue
		}
		seen[dir] = true
		kept = append(kept, dir)
	}
	return strings.Join(kept, string(filepath.ListSeparator))
}

// LookPath finds the executable file in the directories of pathEnv, a PATH
// value that need not be maple's own. The result is then absolute: relative
// and empty entries are skipped because they would resolve against maple's
// working directory (the hazard exec.ErrDot exists for). A file containing a
// slash is returned as given if it is executable, without searching pathEnv.
//
// When nothing is found, the error satisfies errors.Is(err, exec.ErrNotFound).
func LookPath(file, pathEnv string) (string, error) {
	if strings.Contains(file, "/") {
		if err := checkExecutable(file); err != nil {
			return "", &exec.Error{Name: file, Err: err}
		}
		return file, nil
	}
	for _, dir := range filepath.SplitList(pathEnv) {
		if !filepath.IsAbs(dir) {
			continue
		}
		path := filepath.Join(dir, file)
		if checkExecutable(path) == nil {
			return path, nil
		}
	}
	return "", &exec.Error{Name: file, Err: exec.ErrNotFound}
}

func checkExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() || info.Mode().Perm()&0o111 == 0 {
		return fs.ErrPermission
	}
	return nil
}
