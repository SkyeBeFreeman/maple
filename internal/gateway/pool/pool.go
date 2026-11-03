// Package pool owns the upstream connections of the gateway: it starts them
// lazily, shares or isolates them per agent session, reaps idle ones and
// stops restarting servers that crash in a loop.
package pool

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SkyeBeFreeman/maple/internal/gateway/upstream"
)

// Modes a server can run in; "" (remote) behaves like ModeShared.
const (
	ModeShared     = "shared"
	ModePerSession = "per-session"
)

// Values of State.State.
const (
	StateRunning = "running"
	StateIdle    = "idle"
	StateStopped = "stopped"
	StateError   = "error"
)

const (
	fixCooldown = "restarting after cooldown"
	fixRestart  = "the next call restarts the server"
	// exitWait bounds how long a failed stdio call waits for the process exit
	// to be reported, so the model sees the exit code and stderr instead of a
	// bare "connection closed".
	exitWait = time.Second
	// maxAcquire bounds retries when an instance vanishes between its start
	// and its use (reaped, reset, crashed).
	maxAcquire = 3
)

// Options tunes the pool; DefaultOptions holds the values from the spec.
type Options struct {
	CallTimeout, IdleTimeout, CrashWindow, Cooldown time.Duration
	CrashLimit                                      int
}

// DefaultOptions returns the spec values: 50s calls, 10m idle, and a 30s
// cooldown after 3 crashes within 60s.
func DefaultOptions() Options {
	return Options{
		CallTimeout: 50 * time.Second,
		IdleTimeout: 10 * time.Minute,
		CrashWindow: 60 * time.Second,
		Cooldown:    30 * time.Second,
		CrashLimit:  3,
	}
}

// Dialer starts or connects to the named server. ctx bounds only the
// handshake.
type Dialer func(ctx context.Context, server string) (upstream.Session, error)

// State is the pool's view of one server.
type State struct {
	State     string
	PID       int
	LastError string
}

type instance struct {
	key, server string
	sessionID   string // agent session of a per-session instance
	shared      bool
	ready       chan struct{} // closed once the start finished
	sess        upstream.Session
	startErr    error
	inflight    int
	timer       *time.Timer
	// dropped marks an instance the pool stopped on purpose, so its end is
	// not counted as a crash.
	dropped bool
}

type serverState struct {
	crashes  []time.Time
	cooldown time.Time // no starts before this
	lastErr  string    // never carries the "maple: <server>:" prefix
	lastFix  string    // a Dialer's own advice, shown during cooldown
	crashed  bool
}

// Pool hands out upstream sessions; it is safe for concurrent use.
type Pool struct {
	opts Options
	dial Dialer
	// base outlives callers: a start is shared by every caller waiting for
	// it, so one caller giving up must not abort it. Close cancels it.
	base       context.Context
	cancelBase context.CancelFunc

	mu      sync.Mutex
	closed  bool
	seq     int
	insts   map[string]*instance
	servers map[string]*serverState
	wg      sync.WaitGroup // starts, watchers and background closes
}

// errRetry tells waiters that the start they waited for was discarded and
// they should look again.
var errRetry = errors.New("instance discarded while starting")

// New returns an empty pool; nothing starts until the first Call or List.
func New(opts Options, dial Dialer) *Pool {
	base, cancel := context.WithCancel(context.Background())
	return &Pool{
		opts: opts, dial: dial, base: base, cancelBase: cancel,
		insts: map[string]*instance{}, servers: map[string]*serverState{},
	}
}

func (p *Pool) state(server string) *serverState {
	s := p.servers[server]
	if s == nil {
		s = &serverState{}
		p.servers[server] = s
	}
	return s
}

// gate refuses to start a server while the pool is closed or the server is
// cooling down. Caller holds p.mu.
func (p *Pool) gate(server string) error {
	if p.closed {
		return &Error{Server: server, Problem: "gateway is shutting down"}
	}
	if s := p.servers[server]; s != nil && time.Now().Before(s.cooldown) {
		fix := fixCooldown
		if s.lastFix != "" {
			fix = s.lastFix + "; " + fixCooldown
		}
		return &Error{Server: server, Problem: s.lastErr, Fix: fix}
	}
	return nil
}

// recordCrash counts a crash or failed start. Caller holds p.mu.
func (p *Pool) recordCrash(server string, err error) {
	s := p.state(server)
	now := time.Now()
	s.lastErr, s.lastFix = err.Error(), ""
	// A Dialer's *Error is already formatted for the model; storing its
	// Error() would repeat the prefix when gate wraps it again.
	var pe *Error
	if errors.As(err, &pe) {
		s.lastErr, s.lastFix = pe.Problem, pe.Fix
	}
	s.crashed = true
	// History is pruned by the window, not cleared on cooldown, so a server
	// still broken after its cooldown gets one try per cooldown, not three.
	kept := s.crashes[:0]
	for _, t := range s.crashes {
		if now.Sub(t) < p.opts.CrashWindow {
			kept = append(kept, t)
		}
	}
	s.crashes = append(kept, now)
	if len(s.crashes) >= p.opts.CrashLimit {
		s.cooldown = now.Add(p.opts.Cooldown)
	}
}

// acquire returns a live instance with one call counted in flight, starting
// it if needed. Concurrent callers for the same key share one start.
func (p *Pool) acquire(ctx context.Context, server, key, sessionID string, shared bool) (*instance, error) {
	for range maxAcquire {
		p.mu.Lock()
		inst := p.insts[key]
		if inst != nil && inst.sess != nil {
			select {
			case <-inst.sess.Done():
				// Ended but not yet seen by watch, which still records it.
				delete(p.insts, key)
				inst = nil
			default:
			}
		}
		if inst == nil {
			if err := p.gate(server); err != nil {
				p.mu.Unlock()
				return nil, err
			}
			inst = &instance{key: key, server: server, sessionID: sessionID, shared: shared, ready: make(chan struct{})}
			p.insts[key] = inst
			p.wg.Add(1)
			go p.start(inst)
		}
		p.mu.Unlock()

		select {
		case <-inst.ready:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if errors.Is(inst.startErr, errRetry) {
			continue
		}
		if inst.startErr != nil {
			return nil, inst.startErr
		}
		p.mu.Lock()
		if p.insts[key] == inst {
			inst.inflight++
			inst.timer.Stop()
			p.mu.Unlock()
			return inst, nil
		}
		p.mu.Unlock()
	}
	return nil, &Error{Server: server, Problem: "connection kept ending while starting", Fix: "retry the call"}
}

func (p *Pool) start(inst *instance) {
	defer p.wg.Done()
	ctx, cancel := context.WithTimeout(p.base, p.opts.CallTimeout)
	sess, err := p.dial(ctx, inst.server)
	cancel()
	p.mu.Lock()
	switch {
	case inst.dropped:
		// Reset, EndSession or Close ran meanwhile; the outcome belongs to a
		// config or session that no longer exists.
		inst.startErr = errRetry
		if sess != nil {
			p.closeLater(sess)
		}
	case err != nil:
		delete(p.insts, inst.key)
		if p.base.Err() == nil {
			p.recordCrash(inst.server, err)
		}
		inst.startErr = wrapStartErr(inst.server, err)
	default:
		inst.sess = sess
		s := p.state(inst.server)
		s.crashed, s.lastErr, s.lastFix = false, "", ""
		// Armed now so that a start whose callers all gave up is still reaped.
		inst.timer = time.AfterFunc(p.opts.IdleTimeout, func() { p.reap(inst) })
		p.wg.Add(1)
		go p.watch(inst)
	}
	p.mu.Unlock()
	close(inst.ready)
}

func wrapStartErr(server string, err error) error {
	var pe *Error
	if errors.As(err, &pe) {
		return err
	}
	return &Error{Server: server, Problem: err.Error()}
}

// watch turns an unexpected end of the connection into a crash record.
func (p *Pool) watch(inst *instance) {
	defer p.wg.Done()
	<-inst.sess.Done()
	p.mu.Lock()
	defer p.mu.Unlock()
	inst.timer.Stop()
	if p.insts[inst.key] == inst {
		delete(p.insts, inst.key)
	}
	if inst.dropped {
		return
	}
	err := inst.sess.Err()
	if err == nil {
		err = errors.New("connection ended")
	}
	// The server dropping our remote session is routine (restart, expiry);
	// the next call just opens a new one.
	if errors.Is(err, mcp.ErrSessionMissing) {
		return
	}
	p.recordCrash(inst.server, err)
}

// closeLater closes outside p.mu because Close can take seconds. Caller
// holds p.mu.
func (p *Pool) closeLater(sess upstream.Session) {
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		_ = sess.Close()
	}()
}

// drop removes inst on purpose and returns its session (nil while still
// starting) for the caller to close outside p.mu. Caller holds p.mu.
func (p *Pool) drop(inst *instance) upstream.Session {
	if p.insts[inst.key] == inst {
		delete(p.insts, inst.key)
	}
	inst.dropped = true
	if inst.sess != nil {
		select {
		case <-inst.sess.Done():
			// It ended by itself first; leave the crash to watch.
			inst.dropped = false
		default:
		}
	}
	if inst.timer != nil {
		inst.timer.Stop()
	}
	return inst.sess
}

func (p *Pool) release(inst *instance) {
	p.mu.Lock()
	defer p.mu.Unlock()
	inst.inflight--
	if inst.inflight == 0 && p.insts[inst.key] == inst {
		inst.timer.Reset(p.opts.IdleTimeout)
	}
}

func (p *Pool) reap(inst *instance) {
	p.mu.Lock()
	defer p.mu.Unlock()
	// Once closed, Close owns the instance; adding to wg then could race
	// with its Wait.
	if p.closed || inst.inflight > 0 || p.insts[inst.key] != inst {
		return
	}
	p.closeLater(p.drop(inst))
}

// discard drops inst, or whatever still starts under key when inst is nil,
// and waits for it to end. Callers must not hold p.mu.
func (p *Pool) discard(inst *instance, key string) {
	p.mu.Lock()
	if inst == nil {
		inst = p.insts[key]
	}
	var sess upstream.Session
	if inst != nil {
		sess = p.drop(inst)
	}
	p.mu.Unlock()
	if sess != nil {
		_ = sess.Close()
	}
}

// Call runs one tools/call. mode "" (remote) shares one connection like
// ModeShared; ModePerSession keeps one instance per agent session. The whole
// call, including a start, is bounded by CallTimeout.
func (p *Pool) Call(ctx context.Context, server, mode, sessionID string, params *mcp.CallToolParams) (*mcp.CallToolResult, error) {
	key, shared := server, true
	if mode == ModePerSession {
		key, shared = server+"\x00"+sessionID, false
	} else {
		sessionID = ""
	}
	cctx, cancel := context.WithTimeout(ctx, p.opts.CallTimeout)
	defer cancel()
	var res *mcp.CallToolResult
	err := p.with(cctx, server, key, sessionID, shared, func(s upstream.Session) error {
		var err error
		res, err = s.CallTool(cctx, params)
		return err
	})
	switch {
	case err == nil:
		return res, nil
	case ctx.Err() != nil:
		return nil, ctx.Err()
	case expired(ctx):
		return nil, context.DeadlineExceeded
	case expired(cctx):
		return nil, &Error{Server: server, Problem: "call timed out after " + p.opts.CallTimeout.String()}
	}
	return nil, err
}

// expired goes by the clock rather than ctx.Err: a start bounded by the same
// timeout can fail a moment before ctx's own timer marks it done.
func expired(ctx context.Context) bool {
	dl, ok := ctx.Deadline()
	return ctx.Err() != nil || ok && !time.Now().Before(dl)
}

// with runs fn on the instance for key. A remote server that forgot our
// session (HTTP 404) gets a new session and one more try.
func (p *Pool) with(ctx context.Context, server, key, sessionID string, shared bool, fn func(upstream.Session) error) error {
	for retried := false; ; retried = true {
		inst, err := p.acquire(ctx, server, key, sessionID, shared)
		if err != nil {
			return err
		}
		err = fn(inst.sess)
		p.release(inst)
		if err == nil || ctx.Err() != nil {
			return err
		}
		if errors.Is(err, mcp.ErrSessionMissing) && !retried {
			p.discard(inst, "")
			continue
		}
		return callError(server, inst.sess, err)
	}
}

// callError forwards upstream JSON-RPC errors unchanged and reports a dead
// connection with the reason it ended.
func callError(server string, sess upstream.Session, err error) error {
	var we *jsonrpc.Error
	if errors.As(err, &we) {
		return err
	}
	ended := func() bool {
		select {
		case <-sess.Done():
			return true
		default:
			return false
		}
	}
	// Only a process exit is worth waiting for; a remote error does not
	// necessarily end the connection.
	if !ended() && sess.PID() != 0 {
		t := time.NewTimer(exitWait)
		defer t.Stop()
		select {
		case <-sess.Done():
		case <-t.C:
		}
	}
	if ended() {
		if e := sess.Err(); e != nil {
			return &Error{Server: server, Problem: e.Error(), Fix: fixRestart}
		}
	}
	return &Error{Server: server, Problem: err.Error()}
}

// List returns the server's initialize result and tools. Shared and remote
// servers use (and start if needed) their shared instance; per-session
// servers get a throwaway instance so that no agent session is involved.
func (p *Pool) List(ctx context.Context, server, mode string) (*mcp.InitializeResult, []*mcp.Tool, error) {
	info, tools, err := p.list(ctx, server, mode)
	if err != nil && ctx.Err() != nil {
		// The gateway tells "too slow for its list wait" from a failure by
		// the ctx error; the SDK's own wording does not wrap it.
		return nil, nil, ctx.Err()
	}
	return info, tools, err
}

func (p *Pool) list(ctx context.Context, server, mode string) (*mcp.InitializeResult, []*mcp.Tool, error) {
	var info *mcp.InitializeResult
	var tools []*mcp.Tool
	list := func(s upstream.Session) error {
		var err error
		tools, err = s.ListTools(ctx)
		info = s.Info()
		return err
	}
	if mode != ModePerSession {
		if err := p.with(ctx, server, server, "", true, list); err != nil {
			return nil, nil, err
		}
		return info, tools, nil
	}

	p.mu.Lock()
	p.seq++
	key := server + "\x01" + strconv.Itoa(p.seq)
	p.mu.Unlock()
	inst, err := p.acquire(ctx, server, key, "", false)
	if err != nil {
		// The start goes on without us; drop it so it ends once up.
		p.discard(nil, key)
		return nil, nil, err
	}
	err = list(inst.sess)
	if err != nil && ctx.Err() == nil {
		// Before discard, so that a crash is seen as one.
		err = callError(server, inst.sess, err)
	}
	p.discard(inst, "")
	if err != nil {
		return nil, nil, err
	}
	return info, tools, nil
}

func (p *Pool) closeMatching(match func(*instance) bool) {
	p.mu.Lock()
	var sessions []upstream.Session
	for _, inst := range p.insts {
		if match(inst) {
			if s := p.drop(inst); s != nil {
				sessions = append(sessions, s)
			}
		}
	}
	p.mu.Unlock()
	var wg sync.WaitGroup
	for _, s := range sessions {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.Close()
		}()
	}
	wg.Wait()
}

// Reset stops every instance of server and forgets its crash history, e.g.
// after its config changed.
func (p *Pool) Reset(server string) {
	p.closeMatching(func(i *instance) bool { return i.server == server })
	p.mu.Lock()
	delete(p.servers, server)
	p.mu.Unlock()
}

// EndSession stops the per-session instances of an agent session.
func (p *Pool) EndSession(sessionID string) {
	p.closeMatching(func(i *instance) bool { return !i.shared && i.sessionID == sessionID && sessionID != "" })
}

// Status reports every server the pool has started or tried to start.
// PID is set only for a shared stdio instance.
func (p *Pool) Status() map[string]State {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[string]State{}
	for name, s := range p.servers {
		st := State{State: StateIdle, LastError: s.lastErr}
		if s.crashed {
			st.State = StateStopped
		}
		out[name] = st
	}
	for _, inst := range p.insts {
		if inst.sess == nil {
			continue
		}
		st := out[inst.server]
		st.State = StateRunning
		if inst.shared {
			st.PID = inst.sess.PID()
		}
		out[inst.server] = st
	}
	now := time.Now()
	for name, s := range p.servers {
		if now.Before(s.cooldown) {
			st := out[name]
			st.State = StateError
			out[name] = st
		}
	}
	return out
}

// Close stops every instance and pending start; later calls fail.
func (p *Pool) Close() {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	p.cancelBase()
	p.closeMatching(func(*instance) bool { return true })
	p.wg.Wait()
}
