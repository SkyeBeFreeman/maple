// Package proc runs child processes in their own process group and resolves
// commands against the PATH of the user's login shell.
package proc

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
)

// Command prepares name to run in a new process group, so KillGroup can later
// signal it together with everything it spawned (npx, node, ...). There is no
// context: exec.CommandContext would kill only the direct child, so the caller
// owns the lifetime and ends it with KillGroup.
//
// name should already be resolved with LookPath against the child's PATH;
// otherwise exec.Command searches maple's own PATH, which under launchd lacks
// the user's tool directories. A nil env inherits maple's environment.
func Command(name string, args, env []string, dir string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.Env = env
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd
}

// KillGroup sends sig to every process in the group led by pid, which must be
// a started Command. A group that no longer exists counts as success, so
// shutdown paths can kill unconditionally.
//
// On macOS, a group whose members have all exited but are not yet reaped
// reports EPERM; reap the leader (cmd.Wait) first if it may have exited.
func KillGroup(pid int, sig syscall.Signal) error {
	// kill(0, sig) targets maple's own group and kill(-1, sig) every process
	// maple may signal; a bad pid must never turn into either.
	if pid <= 1 {
		return fmt.Errorf("kill process group %d: %w", pid, syscall.EINVAL)
	}
	err := syscall.Kill(-pid, sig)
	if err == nil || errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return fmt.Errorf("kill process group %d: %w", pid, err)
}
