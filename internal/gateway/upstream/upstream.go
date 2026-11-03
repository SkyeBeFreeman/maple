// Package upstream connects the gateway to one user MCP server, either a
// local process speaking stdio or a remote streamable HTTP / SSE endpoint.
package upstream

import (
	"context"
	"errors"
	"io"
	"sort"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SkyeBeFreeman/maple/internal/config"
)

// clientVersion is reported to upstreams in initialize.
const clientVersion = "0.1.0"

// Spec describes how to reach one upstream. All ${NAME} references are
// already expanded; Env holds KEY=VALUE entries.
type Spec struct {
	Name, URL, Transport string
	Headers              map[string]string
	Command              string
	Args, Env            []string
	Dir, PathEnv         string
	Stderr               io.Writer
}

// Hooks receive upstream notifications; nil fields are ignored. They run on
// the connection's goroutines and must not block, or Close waits for them.
type Hooks struct {
	OnToolsChanged func()
	OnProgress     func(*mcp.ProgressNotificationParams)
	// Logging is deprecated in the newest protocol version, but upstreams on
	// older versions still send it and the gateway logs it.
	OnLog func(*mcp.LoggingMessageParams) //nolint:staticcheck
}

// Session is an initialized connection to one upstream.
type Session interface {
	Info() *mcp.InitializeResult
	ListTools(ctx context.Context) ([]*mcp.Tool, error)
	CallTool(ctx context.Context, p *mcp.CallToolParams) (*mcp.CallToolResult, error)
	// PID is the process group leader for stdio and 0 for remote upstreams.
	PID() int
	// Done is closed once the connection has ended for any reason. For stdio
	// the whole process group is gone by then.
	Done() <-chan struct{}
	// Err explains why the connection ended. It is nil while the connection
	// is up and when Close ended it.
	Err() error
	// Close ends the connection, cancelling calls still in flight. It is
	// idempotent and returns once Done is closed.
	Close() error
}

// SpecFromConfig builds a Spec from a server whose secrets are already
// expanded. Env is sorted so that equal configs start identical processes.
func SpecFromConfig(name string, s *config.Server, dir, pathEnv string, stderr io.Writer) Spec {
	env := make([]string, 0, len(s.Env))
	for k, v := range s.Env {
		env = append(env, k+"="+v)
	}
	sort.Strings(env)
	return Spec{
		Name:      name,
		URL:       s.URL,
		Transport: s.EffectiveTransport(),
		Headers:   s.Headers,
		Command:   s.Command,
		Args:      s.Args,
		Env:       env,
		Dir:       dir,
		PathEnv:   pathEnv,
		Stderr:    stderr,
	}
}

// Dial starts or connects to the upstream and completes initialize within
// ctx. ctx only bounds the handshake, not the lifetime of the session.
func Dial(ctx context.Context, s Spec, h Hooks) (Session, error) {
	if s.Command != "" {
		return dialStdio(ctx, s, h)
	}
	if s.URL == "" {
		return nil, errors.New("upstream has neither command nor url")
	}
	return dialRemote(ctx, s, h)
}

func newClient(h Hooks) *mcp.Client {
	// No sampling/elicitation/roots handlers: the SDK then answers such
	// reverse requests with method not found, as the spec requires.
	opts := &mcp.ClientOptions{}
	if h.OnToolsChanged != nil {
		opts.ToolListChangedHandler = func(context.Context, *mcp.ToolListChangedRequest) { h.OnToolsChanged() }
	}
	if h.OnProgress != nil {
		opts.ProgressNotificationHandler = func(_ context.Context, r *mcp.ProgressNotificationClientRequest) { h.OnProgress(r.Params) }
	}
	if h.OnLog != nil {
		opts.LoggingMessageHandler = func(_ context.Context, r *mcp.LoggingMessageRequest) { h.OnLog(r.Params) } //nolint:staticcheck // see Hooks.OnLog
	}
	return mcp.NewClient(&mcp.Implementation{Name: "maple", Version: clientVersion}, opts)
}

// session holds what stdio and remote sessions share.
type session struct {
	cs   *mcp.ClientSession
	done chan struct{}
	// calls is cancelled by Close: the SDK's Close waits for outgoing calls to
	// finish, which a slow or wedged upstream might never do.
	calls       context.Context
	cancelCalls context.CancelFunc

	mu      sync.Mutex
	err     error
	closing bool

	closeOnce sync.Once
	closeErr  error
}

func newSession(cs *mcp.ClientSession) *session {
	calls, cancel := context.WithCancel(context.Background())
	return &session{cs: cs, done: make(chan struct{}), calls: calls, cancelCalls: cancel}
}

// watch closes done once the connection has ended and cleanup has run, so
// that Err is final and resources are released by the time Done fires.
func (s *session) watch(cleanup func(waitErr error) error) {
	go func() {
		err := cleanup(s.cs.Wait())
		s.cancelCalls()
		s.mu.Lock()
		if !s.closing {
			s.err = err
		}
		s.mu.Unlock()
		close(s.done)
	}()
}

// beginClose marks the end as intentional and cancels calls in flight.
func (s *session) beginClose() {
	s.mu.Lock()
	s.closing = true
	s.mu.Unlock()
	s.cancelCalls()
}

func (s *session) bind(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.calls, cancel)
	return ctx, func() { stop(); cancel() }
}

func (s *session) Info() *mcp.InitializeResult { return s.cs.InitializeResult() }

func (s *session) ListTools(ctx context.Context) ([]*mcp.Tool, error) {
	ctx, cancel := s.bind(ctx)
	defer cancel()
	var tools []*mcp.Tool
	params := &mcp.ListToolsParams{}
	for {
		res, err := s.cs.ListTools(ctx, params)
		if err != nil {
			return nil, err
		}
		tools = append(tools, res.Tools...)
		if res.NextCursor == "" {
			return tools, nil
		}
		params = &mcp.ListToolsParams{Cursor: res.NextCursor}
	}
}

func (s *session) CallTool(ctx context.Context, p *mcp.CallToolParams) (*mcp.CallToolResult, error) {
	ctx, cancel := s.bind(ctx)
	defer cancel()
	return s.cs.CallTool(ctx, p)
}

func (s *session) Done() <-chan struct{} { return s.done }

func (s *session) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}
