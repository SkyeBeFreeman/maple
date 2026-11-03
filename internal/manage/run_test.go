package manage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type execCall struct {
	argv0 string
	argv  []string
	env   []string
}

func captureExec(t *testing.T) *[]execCall {
	t.Helper()
	var calls []execCall
	old := execFn
	execFn = func(argv0 string, argv, env []string) error {
		calls = append(calls, execCall{argv0, argv, env})
		return nil
	}
	t.Cleanup(func() { execFn = old })
	return &calls
}

// fakeShell prints a login PATH containing dir, the way proc expects.
func fakeShell(t *testing.T, path string) string {
	t.Helper()
	sh := filepath.Join(t.TempDir(), "fakesh")
	writeFile(t, sh, "#!/bin/sh\nprintf '__MAPLE_PATH__%s__MAPLE_PATH__' '"+path+"'\n")
	if err := os.Chmod(sh, 0o755); err != nil {
		t.Fatal(err)
	}
	return sh
}

func TestRunExecs(t *testing.T) {
	e := setup(t, "servers:\n  d:\n    command: srv\n    args: [--key, \"${K}\"]\n    mode: direct\n    env:\n      TOKEN: x-${K}\n      PATH: /override\n  s:\n    command: srv\n  off:\n    command: srv\n    mode: direct\n    enabled: false\n  m:\n    command: srv\n    mode: direct\n    env:\n      T: ${MISSING}\n")
	bin := t.TempDir()
	srv := filepath.Join(bin, "srv")
	writeFile(t, srv, "#!/bin/sh\n")
	if err := os.Chmod(srv, 0o755); err != nil {
		t.Fatal(err)
	}
	loginPath := "/nonexistent:" + bin
	e.Shell = fakeShell(t, loginPath)
	if err := e.Keychain.Set("K", "secret"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAPLE_RUN_TEST", "inherited")
	calls := captureExec(t)

	if err := Run(context.Background(), e, "d"); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("calls = %d", len(*calls))
	}
	c := (*calls)[0]
	if c.argv0 != srv {
		t.Errorf("argv0 = %q, want %q", c.argv0, srv)
	}
	if want := []string{srv, "--key", "secret"}; !slices.Equal(c.argv, want) {
		t.Errorf("argv = %q, want %q", c.argv, want)
	}
	for _, want := range []string{"TOKEN=x-secret", "MAPLE_RUN_TEST=inherited", "PATH=/override"} {
		if !slices.Contains(c.env, want) {
			t.Errorf("env missing %q", want)
		}
	}
	// One value per key, or the child might read the stale one.
	n := 0
	for _, kv := range c.env {
		if strings.HasPrefix(kv, "PATH=") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("PATH appears %d times", n)
	}

	// Without an override the login shell's PATH is passed on.
	*calls = nil
	e2 := setup(t, "servers:\n  d:\n    command: srv\n    mode: direct\n")
	e2.Shell = e.Shell
	if err := Run(context.Background(), e2, "d"); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || !slices.Contains((*calls)[0].env, "PATH="+loginPath) {
		t.Errorf("env = %v, want PATH=%s", (*calls)[0].env, loginPath)
	}

	*calls = nil
	for name, want := range map[string]string{
		"s":    "not in direct mode",
		"off":  "disabled",
		"nope": "not found",
		"m":    "missing secret MISSING; run: maple secret set MISSING",
	} {
		err := Run(context.Background(), e, name)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Run(%s) err = %v, want %q", name, err, want)
		}
	}
	if len(*calls) != 0 {
		t.Errorf("execFn called %d times for rejected servers", len(*calls))
	}
}

func TestRunCommandNotFound(t *testing.T) {
	e := setup(t, "servers:\n  d:\n    command: no-such-cmd\n    mode: direct\n")
	e.Shell = fakeShell(t, t.TempDir())
	calls := captureExec(t)
	err := Run(context.Background(), e, "d")
	if err == nil || !strings.Contains(err.Error(), "no-such-cmd") {
		t.Fatalf("err = %v", err)
	}
	if len(*calls) != 0 {
		t.Error("execFn called")
	}
}

func TestRunExecError(t *testing.T) {
	e := setup(t, "servers:\n  d:\n    command: /bin/sh\n    mode: direct\n")
	boom := errors.New("boom")
	old := execFn
	execFn = func(string, []string, []string) error { return boom }
	t.Cleanup(func() { execFn = old })
	if err := Run(context.Background(), e, "d"); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}
