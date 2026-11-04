package upstream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SkyeBeFreeman/maple/internal/platform/proc"
)

const (
	terminateDuration = 2 * time.Second
	// A child forking while the group is being killed can escape one signal
	// on macOS, so the kill is repeated until the group is empty.
	killInterval = 20 * time.Millisecond
	killTimeout  = time.Second
	// closeGrace covers the SDK's TerminateDuration plus its SIGTERM wait.
	closeGrace = terminateDuration + time.Second
	maxLineLen = 512
	// stderrDrain bounds how long reaping waits for a grandchild that still
	// holds the stderr pipe after the leader exited.
	stderrDrain = 500 * time.Millisecond
)

// inheritedEnv are the only variables passed through from maple's own
// environment, so secrets maple holds never reach unrelated servers.
var inheritedEnv = []string{"HOME", "USER", "LANG", "TMPDIR"}

func childEnv(s Spec) []string {
	env := []string{"PATH=" + s.PathEnv}
	for _, k := range inheritedEnv {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	// exec.Cmd keeps the last value of a duplicated key, so Spec.Env wins.
	return append(env, s.Env...)
}

// lastLine remembers the last non-empty stderr line, capped so a chatty
// server cannot grow it without bound.
type lastLine struct {
	mu   sync.Mutex
	cur  []byte // line being written, not yet terminated
	last string
}

func (w *lastLine) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, b := range p {
		if b == '\n' {
			if l := strings.TrimSpace(string(w.cur)); l != "" {
				w.last = l
			}
			w.cur = w.cur[:0]
			continue
		}
		if len(w.cur) < maxLineLen {
			w.cur = append(w.cur, b)
		}
	}
	return len(p), nil
}

// tee copies stderr to the log while ignoring its errors: an error would end
// exec's copy goroutine, and the server would then die of SIGPIPE.
type tee struct {
	last *lastLine
	log  io.Writer
}

func (t *tee) Write(p []byte) (int, error) {
	_, _ = t.last.Write(p)
	_, _ = t.log.Write(p)
	return len(p), nil
}

// String includes an unterminated trailing line, since a crashing server
// often dies before writing the newline.
func (w *lastLine) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if l := strings.TrimSpace(string(w.cur)); l != "" {
		return l
	}
	return w.last
}

// pidTransport records the leader pid once the SDK has started the command,
// and kills its group if the Dial context ends before the handshake does. The
// SDK's own cleanup of a failed handshake would first wait TerminateDuration
// for a server that may never read stdin.
type pidTransport struct {
	mcp.CommandTransport
	mu     sync.Mutex
	pid    int
	disarm func() bool
}

func (t *pidTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c, err := t.CommandTransport.Connect(ctx)
	if err != nil {
		return nil, err
	}
	pid := t.Command.Process.Pid
	t.mu.Lock()
	t.pid = pid
	t.disarm = context.AfterFunc(ctx, func() { killGroup(pid) })
	t.mu.Unlock()
	return c, nil
}

func (t *pidTransport) PID() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.pid
}

// handshakeDone stops the ctx watch and reports whether the group is still
// alive, i.e. ctx did not end first.
func (t *pidTransport) handshakeDone() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.disarm == nil || t.disarm()
}

type stdioSession struct {
	*session
	pid    int
	cmd    *exec.Cmd
	stderr *lastLine
}

func resolveCommand(s Spec) (string, error) {
	name := s.Command
	// exec resolves a relative path against cmd.Dir, but LookPath would check
	// it against maple's working directory.
	if strings.Contains(name, "/") && !filepath.IsAbs(name) && s.Dir != "" {
		name = filepath.Join(s.Dir, name)
	}
	return proc.LookPath(name, s.PathEnv)
}

func dialStdio(ctx context.Context, s Spec, h Hooks) (Session, error) {
	path, err := resolveCommand(s)
	if err != nil {
		return nil, fmt.Errorf("find command %q: %w", s.Command, err)
	}
	cmd := proc.Command(path, s.Args, childEnv(s), s.Dir)
	ll := &lastLine{}
	cmd.Stderr = ll
	if s.Stderr != nil {
		cmd.Stderr = &tee{ll, s.Stderr}
	}
	cmd.WaitDelay = stderrDrain
	t := &pidTransport{CommandTransport: mcp.CommandTransport{Command: cmd, TerminateDuration: terminateDuration}}

	cs, err := newClient(h).Connect(ctx, t, nil)
	if !t.handshakeDone() && err == nil {
		_ = cs.Close()
		err = ctx.Err()
	}
	if err != nil {
		if pid := t.PID(); pid > 0 {
			killGroup(pid)
		}
		if l := ll.String(); l != "" {
			return nil, fmt.Errorf("start %s: %w; last stderr: %s", s.Command, err, l)
		}
		return nil, fmt.Errorf("start %s: %w", s.Command, err)
	}
	ss := &stdioSession{session: newSession(cs), pid: t.PID(), cmd: cmd, stderr: ll}
	ss.watch(func(waitErr error) error {
		// The leader may be gone while children it spawned live on.
		killGroup(ss.pid)
		return ss.exitError(waitErr)
	})
	return ss, nil
}

// exitError explains the end of the connection from the error of cs.Wait,
// which carries the result of the SDK's cmd.Wait. ProcessState is read only
// when that error proves cmd.Wait has returned: on its timeout paths the SDK
// returns while cmd.Wait is still running, and reading then would race.
func (s *stdioSession) exitError(waitErr error) error {
	var ps *os.ProcessState
	var ee *exec.ExitError
	switch {
	case errors.As(waitErr, &ee):
		ps = ee.ProcessState
	case waitErr == nil || errors.Is(waitErr, exec.ErrWaitDelay):
		ps = s.cmd.ProcessState
	}
	var msg string
	if ps == nil {
		msg = "connection lost"
		if waitErr != nil {
			msg += ": " + waitErr.Error()
		}
	} else if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		msg = fmt.Sprintf("process exited (signal=%s)", signalName(ws.Signal()))
	} else {
		msg = fmt.Sprintf("process exited (code=%d)", ps.ExitCode())
	}
	if l := s.stderr.String(); l != "" {
		return errors.New(msg + ", last stderr: " + l)
	}
	return errors.New(msg)
}

func signalName(sig syscall.Signal) string {
	switch sig {
	case syscall.SIGKILL:
		return "SIGKILL"
	case syscall.SIGTERM:
		return "SIGTERM"
	case syscall.SIGINT:
		return "SIGINT"
	case syscall.SIGSEGV:
		return "SIGSEGV"
	case syscall.SIGABRT:
		return "SIGABRT"
	case syscall.SIGPIPE:
		return "SIGPIPE"
	}
	return sig.String()
}

func (s *stdioSession) PID() int { return s.pid }

func (s *stdioSession) Close() error {
	s.closeOnce.Do(func() {
		s.beginClose()
		// Closing the session closes stdin and lets the SDK reap the leader
		// (SIGTERM after TerminateDuration); its exit status does not matter
		// once we chose to stop it. If that stalls anyway, killing the group
		// breaks the connection and lets it finish.
		go func() { _ = s.cs.Close() }()
		select {
		case <-s.done:
		case <-time.After(closeGrace):
		}
		killGroup(s.pid)
		<-s.done
	})
	// The process was stopped on purpose, so how it exited is not an error.
	return nil
}

// killGroup SIGKILLs the group until it is empty or only zombies remain
// (EPERM on macOS), giving up after killTimeout.
func killGroup(pid int) {
	deadline := time.Now().Add(killTimeout)
	for {
		_ = proc.KillGroup(pid, syscall.SIGKILL)
		err := syscall.Kill(-pid, 0)
		if errors.Is(err, syscall.ESRCH) || errors.Is(err, syscall.EPERM) || time.Now().After(deadline) {
			return
		}
		time.Sleep(killInterval)
	}
}
