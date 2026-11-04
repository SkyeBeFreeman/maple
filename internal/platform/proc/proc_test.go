package proc_test

import (
	"bufio"
	"errors"
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

func TestCommandSetsEnvAndDir(t *testing.T) {
	dir := t.TempDir()
	cmd := proc.Command("/bin/sh", []string{"-c", `pwd -P; printf '%s\n' "$MAPLE_PROBE"`},
		[]string{"MAPLE_PROBE=probe-value"}, dir)

	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("Output: %v", err)
	}

	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := realDir + "\nprobe-value\n"; string(out) != want {
		t.Fatalf("output = %q, want %q", out, want)
	}
}

func TestKillGroupKillsChildren(t *testing.T) {
	// "wait", not a second "sleep 100": nothing may fork once the pid is
	// printed, because on macOS a process forked while its group is being
	// signalled escapes the signal, and the kill below would race it.
	cmd := proc.Command("sh", []string{"-c", "sleep 100 & echo $! ; wait"}, nil, "")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	leader := cmd.Process.Pid
	child := 0
	reaped := false
	t.Cleanup(func() {
		if !reaped {
			// Safe to signal the group: until the leader is reaped its pid,
			// and so the group id, cannot be reused.
			_ = syscall.Kill(-leader, syscall.SIGKILL)
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		// Only on failure: once the test has seen the child gone, its pid may
		// already belong to an unrelated process.
		if t.Failed() && child > 0 {
			_ = syscall.Kill(child, syscall.SIGKILL)
		}
	})

	if pgid, err := syscall.Getpgid(leader); err != nil || pgid != leader {
		t.Fatalf("Getpgid(%d) = %d, %v; want its own group", leader, pgid, err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("read child pid: %v", err)
	}
	if child, err = strconv.Atoi(strings.TrimSpace(line)); err != nil {
		t.Fatalf("parse child pid %q: %v", line, err)
	}

	if err := proc.KillGroup(leader, syscall.SIGKILL); err != nil {
		t.Fatalf("KillGroup: %v", err)
	}

	var exitErr *exec.ExitError
	err = cmd.Wait()
	reaped = true
	if !errors.As(err, &exitErr) || exitErr.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
		t.Fatalf("leader Wait = %v, want killed by SIGKILL", err)
	}
	testutil.WaitGone(t, child, time.Second)
	testutil.WaitGone(t, -leader, time.Second) // no member of the group survived
}

func TestKillGroupGoneGroupIsSuccess(t *testing.T) {
	cmd := proc.Command("/bin/sh", []string{"-c", "exit 0"}, nil, "")
	if err := cmd.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Signal 0: the pid is free again and could, in theory, be reused already.
	if err := proc.KillGroup(cmd.Process.Pid, 0); err != nil {
		t.Fatalf("KillGroup on reaped group = %v, want nil", err)
	}
}

func TestKillGroupRejectsReservedPIDs(t *testing.T) {
	// Signal 0 keeps this harmless even if the guard is missing: kill(0) would
	// target the test's own group and kill(-1) every process it may signal.
	for _, pid := range []int{-1, 0, 1} {
		if err := proc.KillGroup(pid, 0); !errors.Is(err, syscall.EINVAL) {
			t.Errorf("KillGroup(%d) = %v, want EINVAL", pid, err)
		}
	}
}
