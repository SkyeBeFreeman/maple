package pool

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SkyeBeFreeman/maple/internal/gateway/upstream"
	"github.com/SkyeBeFreeman/maple/internal/testutil"
)

type fixture struct {
	log string
	mu  sync.Mutex
	pid []int
}

func (f *fixture) pids() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.pid...)
}

func stdioPool(t *testing.T, opts Options, extra ...string) (*Pool, *fixture) {
	t.Helper()
	bin := testutil.BuildFakeMCP(t)
	dir := t.TempDir()
	f := &fixture{log: filepath.Join(dir, "starts.log")}
	args := append([]string{"-start-log", f.log}, extra...)
	dial := func(ctx context.Context, server string) (upstream.Session, error) {
		s, err := upstream.Dial(ctx, upstream.Spec{Name: server, Command: bin, Args: args, Dir: dir, PathEnv: "/usr/bin:/bin"}, upstream.Hooks{})
		if err == nil {
			f.mu.Lock()
			f.pid = append(f.pid, s.PID())
			f.mu.Unlock()
		}
		return s, err
	}
	p := New(opts, dial)
	t.Cleanup(func() {
		p.Close()
		for _, pid := range f.pids() {
			waitGone(t, pid)
		}
		p.mu.Lock()
		n := len(p.insts)
		p.mu.Unlock()
		if n != 0 {
			t.Errorf("%d instances left after Close", n)
		}
	})
	return p, f
}

func (f *fixture) starts(t *testing.T) int {
	t.Helper()
	b, err := os.ReadFile(f.log)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return len(strings.Fields(string(b)))
}

func fastOpts() Options {
	o := DefaultOptions()
	o.CallTimeout = 10 * time.Second
	return o
}

func waitGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		if time.Now().After(deadline) {
			t.Fatalf("pid %d still alive", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func echo(t *testing.T, p *Pool, mode, sid string) {
	t.Helper()
	res, err := p.Call(context.Background(), "fx", mode, sid, &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{"text": "hi"}})
	if err != nil {
		t.Fatalf("echo: %v", err)
	}
	if res.IsError || res.Content[0].(*mcp.TextContent).Text != "hi" {
		t.Fatalf("bad result %+v", res)
	}
}

func TestDefaultOptions(t *testing.T) {
	o := DefaultOptions()
	if o.CallTimeout != 50*time.Second || o.IdleTimeout != 10*time.Minute || o.CrashWindow != 60*time.Second || o.Cooldown != 30*time.Second || o.CrashLimit != 3 {
		t.Fatalf("%+v", o)
	}
}

func TestErrorFormat(t *testing.T) {
	if got := (&Error{Server: "a", Problem: "p", Fix: "f"}).Error(); got != "maple: a: p; f" {
		t.Fatal(got)
	}
	if got := (&Error{Server: "a", Problem: "p"}).Error(); got != "maple: a: p" {
		t.Fatal(got)
	}
}

func TestSharedSingleInstance(t *testing.T) {
	p, f := stdioPool(t, fastOpts())
	var wg sync.WaitGroup
	for _, sid := range []string{"a", "b"} {
		wg.Add(1)
		go func() { defer wg.Done(); echo(t, p, "shared", sid) }()
	}
	wg.Wait()
	if n := f.starts(t); n != 1 {
		t.Fatalf("starts=%d", n)
	}
	st := p.Status()["fx"]
	if st.State != "running" || st.PID != f.pids()[0] {
		t.Fatalf("%+v", st)
	}
}

func TestPerSessionSeparateInstances(t *testing.T) {
	p, f := stdioPool(t, fastOpts())
	echo(t, p, "per-session", "a")
	echo(t, p, "per-session", "b")
	pids := f.pids()
	if len(pids) != 2 || pids[0] == pids[1] {
		t.Fatalf("pids %v", pids)
	}
	if p.Status()["fx"].PID != 0 {
		t.Fatal("per-session PID should be 0")
	}
	p.EndSession("a")
	waitGone(t, pids[0])
	if !alive(pids[1]) {
		t.Fatal("b killed")
	}
}

func TestIdleReap(t *testing.T) {
	o := fastOpts()
	o.IdleTimeout = 100 * time.Millisecond
	p, f := stdioPool(t, o)
	echo(t, p, "shared", "")
	waitGone(t, f.pids()[0])
	deadline := time.Now().Add(2 * time.Second)
	for p.Status()["fx"].State != "idle" {
		if time.Now().After(deadline) {
			t.Fatalf("state %+v", p.Status()["fx"])
		}
		time.Sleep(10 * time.Millisecond)
	}
	echo(t, p, "shared", "")
	if n := f.starts(t); n != 2 {
		t.Fatalf("starts=%d", n)
	}
}

func TestNoReapDuringCall(t *testing.T) {
	o := fastOpts()
	o.IdleTimeout = 50 * time.Millisecond
	p, f := stdioPool(t, o)
	if _, err := p.Call(context.Background(), "fx", "shared", "", &mcp.CallToolParams{Name: "sleep", Arguments: map[string]any{"ms": 300}}); err != nil {
		t.Fatal(err)
	}
	if f.starts(t) != 1 {
		t.Fatal("reaped during call")
	}
}

func TestCallTimeoutCancelsUpstream(t *testing.T) {
	o := fastOpts()
	o.CallTimeout = 100 * time.Millisecond
	p, f := stdioPool(t, o)
	_, err := p.Call(context.Background(), "fx", "shared", "", &mcp.CallToolParams{Name: "sleep", Arguments: map[string]any{"ms": 5000}})
	var pe *Error
	if !errors.As(err, &pe) || !strings.Contains(pe.Problem, "timed out after 100ms") {
		t.Fatalf("err %v", err)
	}
	echo(t, p, "shared", "")
	if len(f.pids()) != 1 || f.starts(t) != 1 {
		t.Fatalf("restarted: %v", f.pids())
	}
}

func TestCallerCancelReturnsCtxErr(t *testing.T) {
	p, _ := stdioPool(t, fastOpts())
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := p.Call(ctx, "fx", "shared", "", &mcp.CallToolParams{Name: "sleep", Arguments: map[string]any{"ms": 5000}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err %v", err)
	}
}

func TestCrashRestart(t *testing.T) {
	p, f := stdioPool(t, fastOpts())
	echo(t, p, "shared", "")
	_, err := p.Call(context.Background(), "fx", "shared", "", &mcp.CallToolParams{Name: "crash", Arguments: map[string]any{"code": 3}})
	var pe *Error
	if !errors.As(err, &pe) || !strings.Contains(pe.Problem, "code=3") {
		t.Fatalf("crash err %v", err)
	}
	waitGone(t, f.pids()[0])
	deadline := time.Now().Add(2 * time.Second)
	for p.Status()["fx"].State != "stopped" {
		if time.Now().After(deadline) {
			t.Fatalf("state %+v", p.Status()["fx"])
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(p.Status()["fx"].LastError, "code=3") {
		t.Fatalf("%+v", p.Status()["fx"])
	}
	echo(t, p, "shared", "")
	pids := f.pids()
	if len(pids) != 2 || pids[0] == pids[1] {
		t.Fatalf("pids %v", pids)
	}
}

func TestCrashLoopCooldown(t *testing.T) {
	o := fastOpts()
	o.CrashWindow, o.CrashLimit, o.Cooldown = time.Second, 3, 300*time.Millisecond
	p, f := stdioPool(t, o, "-exit-on-start", "2")
	call := func() error {
		_, err := p.Call(context.Background(), "fx", "shared", "", &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{"text": "x"}})
		return err
	}
	for i := 0; i < 3; i++ {
		if call() == nil {
			t.Fatal("expected dial error")
		}
	}
	err := call()
	var pe *Error
	if !errors.As(err, &pe) || pe.Fix != "restarting after cooldown" {
		t.Fatalf("err %v", err)
	}
	if n := f.starts(t); n != 3 {
		t.Fatalf("starts=%d", n)
	}
	if p.Status()["fx"].State != "error" {
		t.Fatalf("%+v", p.Status()["fx"])
	}
	time.Sleep(350 * time.Millisecond)
	_ = call()
	if n := f.starts(t); n != 4 {
		t.Fatalf("starts=%d", n)
	}
}

func TestListPerSessionUsesTemporaryInstance(t *testing.T) {
	p, f := stdioPool(t, fastOpts())
	info, tools, err := p.List(context.Background(), "fx", "per-session")
	if err != nil || info == nil || len(tools) == 0 {
		t.Fatalf("%v %v %d", err, info, len(tools))
	}
	waitGone(t, f.pids()[0])
	if p.Status()["fx"].State == "running" {
		t.Fatal("still running")
	}
}

func TestListPerSessionCrashCounts(t *testing.T) {
	sess := &fakeSession{done: make(chan struct{}), listCrash: true}
	p := New(fastOpts(), func(context.Context, string) (upstream.Session, error) { return sess, nil })
	defer p.Close()
	_, _, err := p.List(context.Background(), "r", "per-session")
	var pe *Error
	if !errors.As(err, &pe) || pe.Problem != "process exited (code=1)" {
		t.Fatalf("%v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for p.Status()["r"].State != "stopped" {
		if time.Now().After(deadline) {
			t.Fatalf("%+v", p.Status()["r"])
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestListSharedReuses(t *testing.T) {
	p, f := stdioPool(t, fastOpts())
	if _, _, err := p.List(context.Background(), "fx", "shared"); err != nil {
		t.Fatal(err)
	}
	echo(t, p, "shared", "")
	if f.starts(t) != 1 {
		t.Fatal("not reused")
	}
}

func TestResetAndClose(t *testing.T) {
	p, f := stdioPool(t, fastOpts())
	echo(t, p, "shared", "")
	p.Reset("fx")
	waitGone(t, f.pids()[0])
	echo(t, p, "shared", "")
	p.Close()
	waitGone(t, f.pids()[1])
	if _, err := p.Call(context.Background(), "fx", "shared", "", &mcp.CallToolParams{Name: "echo"}); err == nil {
		t.Fatal("call after Close succeeded")
	}
}

type fakeSession struct {
	fail      bool
	listCrash bool // ListTools ends the session as if the process died
	listBlock bool // ListTools waits for ctx to end
	done      chan struct{}
	once      sync.Once
	closed    bool
	err       error
}

func (s *fakeSession) Info() *mcp.InitializeResult { return &mcp.InitializeResult{} }
func (s *fakeSession) ListTools(ctx context.Context) ([]*mcp.Tool, error) {
	if s.listBlock {
		<-ctx.Done()
		// Like the SDK, the error does not wrap ctx.Err.
		return nil, errors.New("calling \"tools/list\": request cancelled")
	}
	if s.listCrash {
		s.once.Do(func() { s.err = errors.New("process exited (code=1)"); close(s.done) })
		return nil, mcp.ErrConnectionClosed
	}
	return nil, nil
}
func (s *fakeSession) CallTool(context.Context, *mcp.CallToolParams) (*mcp.CallToolResult, error) {
	if s.fail {
		// The streamable transport fails the whole connection on a 404.
		err := fmt.Errorf("calling: %w", mcp.ErrSessionMissing)
		s.once.Do(func() { s.err = err; close(s.done) })
		time.Sleep(20 * time.Millisecond) // let the pool see Done first
		return nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
}
func (s *fakeSession) PID() int              { return 0 }
func (s *fakeSession) Done() <-chan struct{} { return s.done }
func (s *fakeSession) Err() error            { return s.err }
func (s *fakeSession) Close() error {
	s.once.Do(func() { s.closed = true; close(s.done) })
	return nil
}

func TestRemoteSessionGoneRetry(t *testing.T) {
	var mu sync.Mutex
	var made []*fakeSession
	p := New(fastOpts(), func(context.Context, string) (upstream.Session, error) {
		mu.Lock()
		defer mu.Unlock()
		s := &fakeSession{fail: len(made) == 0, done: make(chan struct{})}
		made = append(made, s)
		return s, nil
	})
	defer p.Close()
	res, err := p.Call(context.Background(), "r", "", "x", &mcp.CallToolParams{Name: "t"})
	if err != nil || res.Content[0].(*mcp.TextContent).Text != "ok" {
		t.Fatalf("%v %v", res, err)
	}
	p.mu.Lock()
	crashes := len(p.servers["r"].crashes)
	p.mu.Unlock()
	if crashes != 0 {
		t.Fatalf("session loss counted as %d crashes", crashes)
	}
	p.Close()
	mu.Lock()
	defer mu.Unlock()
	if len(made) != 2 || !made[1].closed {
		t.Fatalf("made %d", len(made))
	}
}

func TestDialErrorPassthrough(t *testing.T) {
	want := &Error{Server: "r", Problem: "missing secret", Fix: "set it"}
	p := New(fastOpts(), func(context.Context, string) (upstream.Session, error) { return nil, want })
	defer p.Close()
	_, err := p.Call(context.Background(), "r", "", "", &mcp.CallToolParams{Name: "t"})
	if err != want {
		t.Fatalf("%v", err)
	}
	p2 := New(fastOpts(), func(context.Context, string) (upstream.Session, error) { return nil, errors.New("boom") })
	defer p2.Close()
	_, err = p2.Call(context.Background(), "r", "", "", &mcp.CallToolParams{Name: "t"})
	var pe *Error
	if !errors.As(err, &pe) || pe.Problem != "boom" || pe.Server != "r" {
		t.Fatalf("%v", err)
	}
}

// blockingDialer lets a test hold a dial open and count attempts.
type blockingDialer struct {
	mu      sync.Mutex
	n       int
	release chan struct{}
	err     error
}

func (d *blockingDialer) dial(ctx context.Context, _ string) (upstream.Session, error) {
	d.mu.Lock()
	d.n++
	d.mu.Unlock()
	select {
	case <-d.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if d.err != nil {
		return nil, d.err
	}
	return &fakeSession{done: make(chan struct{})}, nil
}

func (d *blockingDialer) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.n
}

func TestCallerCancelDoesNotAbortSharedDial(t *testing.T) {
	d := &blockingDialer{release: make(chan struct{})}
	p := New(fastOpts(), d.dial)
	defer p.Close()
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() {
		_, err := p.Call(ctx, "r", "", "", &mcp.CallToolParams{Name: "t"})
		first <- err
	}()
	for d.count() == 0 {
		time.Sleep(time.Millisecond)
	}
	second := make(chan error, 1)
	go func() {
		_, err := p.Call(context.Background(), "r", "", "", &mcp.CallToolParams{Name: "t"})
		second <- err
	}()
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("first %v", err)
	}
	close(d.release)
	select {
	case err := <-second:
		if err != nil {
			t.Fatalf("second %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second call hung")
	}
	if d.count() != 1 {
		t.Fatalf("dials=%d", d.count())
	}
	if st := p.Status()["r"]; st.State != "running" || st.LastError != "" {
		t.Fatalf("%+v", st)
	}
}

func TestStartBoundedByCallTimeout(t *testing.T) {
	o := fastOpts()
	o.CallTimeout = 100 * time.Millisecond
	d := &blockingDialer{release: make(chan struct{})}
	p := New(o, d.dial)
	defer p.Close()
	_, err := p.Call(context.Background(), "r", "", "", &mcp.CallToolParams{Name: "t"})
	var pe *Error
	if !errors.As(err, &pe) || pe.Problem != "call timed out after 100ms" {
		t.Fatalf("%v", err)
	}
}

func TestCloseAbortsDial(t *testing.T) {
	d := &blockingDialer{release: make(chan struct{})}
	p := New(fastOpts(), d.dial)
	errc := make(chan error, 1)
	go func() {
		_, err := p.Call(context.Background(), "r", "", "", &mcp.CallToolParams{Name: "t"})
		errc <- err
	}()
	for d.count() == 0 {
		time.Sleep(time.Millisecond)
	}
	closed := make(chan struct{})
	go func() { p.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close hung on a pending dial")
	}
	if err := <-errc; err == nil {
		t.Fatal("call succeeded after Close")
	}
}

func TestConcurrentDialErrorCountsOnce(t *testing.T) {
	d := &blockingDialer{release: make(chan struct{}), err: errors.New("boom")}
	o := fastOpts()
	o.CrashLimit = 2
	p := New(o, d.dial)
	defer p.Close()
	var wg sync.WaitGroup
	errs := make([]error, 3)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = p.Call(context.Background(), "r", "", "", &mcp.CallToolParams{Name: "t"})
		}()
	}
	for d.count() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	close(d.release)
	wg.Wait()
	for _, err := range errs {
		var pe *Error
		if !errors.As(err, &pe) || pe.Problem != "boom" || pe.Fix != "" {
			t.Fatalf("%v", err)
		}
	}
	if d.count() != 1 {
		t.Fatalf("dials=%d", d.count())
	}
	if st := p.Status()["r"]; st.State != "stopped" || st.LastError != "boom" {
		t.Fatalf("%+v", st)
	}
}

func TestCooldownKeepsDialerErrorSinglePrefix(t *testing.T) {
	o := fastOpts()
	o.CrashLimit = 2
	want := &Error{Server: "r", Problem: "missing secret X", Fix: "run: maple secret set X"}
	p := New(o, func(context.Context, string) (upstream.Session, error) { return nil, want })
	defer p.Close()
	call := func() error {
		_, err := p.Call(context.Background(), "r", "", "", &mcp.CallToolParams{Name: "t"})
		return err
	}
	for range o.CrashLimit {
		if err := call(); err != want {
			t.Fatalf("start err %v", err)
		}
	}
	err := call()
	if err == nil || strings.Count(err.Error(), "maple: r:") != 1 || !strings.Contains(err.Error(), "missing secret X") {
		t.Fatalf("cooldown err %q", err)
	}
	if !strings.Contains(err.Error(), "run: maple secret set X") {
		t.Fatalf("cooldown err lost the fix: %q", err)
	}
	st := p.Status()["r"]
	if st.State != "error" || strings.Contains(st.LastError, "maple:") || st.LastError != "missing secret X" {
		t.Fatalf("%+v", st)
	}
}

func TestListCtxDoneReturnsCtxErr(t *testing.T) {
	list := func(t *testing.T, d Dialer, mode string) error {
		t.Helper()
		p := New(fastOpts(), d)
		defer p.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		_, _, err := p.List(ctx, "r", mode)
		return err
	}
	slowList := func(context.Context, string) (upstream.Session, error) {
		return &fakeSession{done: make(chan struct{}), listBlock: true}, nil
	}
	for _, mode := range []string{"shared", "per-session"} {
		t.Run("slow start "+mode, func(t *testing.T) {
			d := &blockingDialer{release: make(chan struct{})}
			if err := list(t, d.dial, mode); err != context.DeadlineExceeded {
				t.Fatalf("%v", err)
			}
		})
		t.Run("slow list "+mode, func(t *testing.T) {
			if err := list(t, slowList, mode); err != context.DeadlineExceeded {
				t.Fatalf("%v", err)
			}
		})
	}
}
