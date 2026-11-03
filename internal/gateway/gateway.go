package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/SkyeBeFreeman/maple/internal/config"
	"github.com/SkyeBeFreeman/maple/internal/gateway/pool"
	"github.com/SkyeBeFreeman/maple/internal/platform/keychain"
	"github.com/SkyeBeFreeman/maple/internal/platform/proc"
)

const (
	logMaxSizeMB  = 10
	logMaxBackups = 3
)

// Options tunes the gateway; DefaultOptions holds the values from the spec.
type Options struct {
	Pool pool.Options
	// ListWait bounds how long tools/list waits for servers being listed,
	// InitWait how long initialize waits for servers without a cache entry,
	// Debounce how long config changes settle before a reload.
	ListWait, InitWait, Debounce time.Duration
	Version                      string
	// Shell is the login shell whose PATH stdio servers get.
	Shell string
	// Logger replaces the gateway.log logger, e.g. in tests. Records still
	// pass through the Redactor first.
	Logger *slog.Logger
}

// DefaultOptions returns the spec values: 40s list wait, 5s initialize wait
// and a 200ms debounce. Version and Shell are left to the caller.
func DefaultOptions() Options {
	return Options{
		Pool:     pool.DefaultOptions(),
		ListWait: 40 * time.Second,
		InitWait: 5 * time.Second,
		Debounce: 200 * time.Millisecond,
	}
}

// Gateway serves every configured upstream's tools through one MCP endpoint.
type Gateway struct {
	paths  config.Paths
	kc     keychain.Store
	opts   Options
	red    *Redactor
	log    *slog.Logger
	server *mcp.Server
	pool   *pool.Pool

	// bg bounds background work (tool listing); cancelled on shutdown.
	bg       context.Context
	cancelBg context.CancelFunc
	wg       sync.WaitGroup
	// pathEnv is written once before anything can dial.
	pathEnv string

	mu        sync.Mutex
	closed    bool
	cfg       *config.Config
	cfgHash   string
	reloadErr string
	token     string
	cache     *Cache
	fetching  map[string]chan struct{}
	refetch   map[string]bool
	tools     map[string]exposed
	logs      map[string]*redactWriter
	logFile   io.Closer // gateway.log, nil with Options.Logger

	progMu   sync.Mutex
	progSeq  uint64
	progress map[string]chan *mcp.ProgressNotificationParams
}

// New returns a gateway for the maple directory under p.Home. Nothing runs
// until Serve.
func New(p config.Paths, kc keychain.Store, opts Options) *Gateway {
	g := &Gateway{
		paths:    p,
		kc:       kc,
		opts:     opts,
		red:      NewRedactor(nil),
		cfg:      emptyConfig(),
		cache:    emptyCache(),
		fetching: map[string]chan struct{}{},
		refetch:  map[string]bool{},
		tools:    map[string]exposed{},
		logs:     map[string]*redactWriter{},
		progress: map[string]chan *mcp.ProgressNotificationParams{},
	}
	g.bg, g.cancelBg = context.WithCancel(context.Background())
	var h slog.Handler
	if opts.Logger != nil {
		h = opts.Logger.Handler()
	} else {
		f := rotating(filepath.Join(p.LogDir(), "gateway.log"))
		g.logFile = f
		h = slog.NewTextHandler(f, nil)
	}
	g.log = slog.New(redactHandler{h: h, red: g.red})
	g.server = mcp.NewServer(&mcp.Implementation{Name: "maple", Version: opts.Version}, &mcp.ServerOptions{
		// Declared up front: with an empty catalog the SDK would omit the
		// tools capability, and agents would never ask again.
		Capabilities:       &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{ListChanged: true}},
		InitializedHandler: g.sessionStarted,
	})
	g.server.AddReceivingMiddleware(g.middleware)
	g.pool = pool.New(opts.Pool, g.dial)
	return g
}

func emptyConfig() *config.Config {
	return &config.Config{Port: config.DefaultPort, Servers: map[string]*config.Server{}}
}

func rotating(path string) *lumberjack.Logger {
	return &lumberjack.Logger{Filename: path, MaxSize: logMaxSizeMB, MaxBackups: logMaxBackups}
}

// Serve answers on ln until ctx ends, then stops every upstream and returns.
func (g *Gateway) Serve(ctx context.Context, ln net.Listener) error {
	defer g.closeLogs()
	// Before the first log line: lumberjack would create it world-readable.
	if err := os.MkdirAll(g.paths.LogDir(), 0o700); err != nil {
		return err
	}
	g.pathEnv = proc.LoginShellPath(ctx, g.opts.Shell)
	g.loadInitial()

	stopWatch, err := g.watch()
	if err != nil {
		// Without a watcher the gateway still serves; edits need a restart.
		g.log.Error("cannot watch config directory", "err", err.Error())
		stopWatch = func() {}
	}

	port := 0
	if a, ok := ln.Addr().(*net.TCPAddr); ok {
		port = a.Port
	}
	mux := http.NewServeMux()
	// The SDK's own localhost check stays on: Guard admits only loopback
	// Host values, which that check accepts too, so it never rejects a
	// request Guard let through.
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return g.server }, nil)
	mux.Handle("/mcp", g.guard(port, mcpHandler))
	mux.Handle("/status", g.guard(port, http.HandlerFunc(g.serveStatus)))
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	g.log.Info("gateway listening", "addr", ln.Addr().String(), "version", g.opts.Version)

	var result error
	select {
	case <-ctx.Done():
	case err := <-serveErr:
		result = err
	}

	// No graceful drain: agents keep an SSE stream open, so it would always
	// run to its deadline, and they reconnect anyway.
	_ = srv.Close()
	stopWatch()
	g.mu.Lock()
	g.closed = true
	g.mu.Unlock()
	g.cancelBg()
	// Stopping upstreams first fails calls in flight at once; closing a
	// session waits for its handlers to return.
	g.pool.Close()
	for ss := range g.server.Sessions() {
		_ = ss.Close()
	}
	g.wg.Wait()
	g.log.Info("gateway stopped")
	if errors.Is(result, http.ErrServerClosed) {
		result = nil
	}
	return result
}

// guard applies Guard with the current token, which a reload may change.
func (g *Gateway) guard(port int, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		tok := g.token
		g.mu.Unlock()
		Guard(port, tok, next).ServeHTTP(w, r)
	})
}

func (g *Gateway) serveStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(g.status())
}

// sessionStarted ends an agent's per-session upstreams when its session ends.
func (g *Gateway) sessionStarted(_ context.Context, req *mcp.InitializedRequest) {
	ss := req.Session
	id := ss.ID()
	if id == "" {
		return
	}
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return
	}
	g.wg.Add(1)
	g.mu.Unlock()
	go func() {
		defer g.wg.Done()
		_ = ss.Wait()
		g.pool.EndSession(id)
	}()
}

// serverLog is the rotating, redacted stderr log of a stdio server.
func (g *Gateway) serverLog(name string) io.Writer {
	g.mu.Lock()
	defer g.mu.Unlock()
	w := g.logs[name]
	if w == nil {
		w = newRedactWriter(g.red, rotating(filepath.Join(g.paths.LogDir(), name+".log")))
		g.logs[name] = w
	}
	return w
}

func (g *Gateway) closeLogs() {
	g.mu.Lock()
	logs := g.logs
	g.logs = map[string]*redactWriter{}
	g.mu.Unlock()
	for _, w := range logs {
		_ = w.Close()
	}
	if g.logFile != nil {
		_ = g.logFile.Close()
	}
}

// redactHandler masks secrets in every record, whichever handler formats it.
type redactHandler struct {
	h   slog.Handler
	red *Redactor
}

func (h redactHandler) Enabled(ctx context.Context, l slog.Level) bool { return h.h.Enabled(ctx, l) }

func (h redactHandler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, h.red.Redact(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(h.attr(a))
		return true
	})
	return h.h.Handle(ctx, out)
}

func (h redactHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		out[i] = h.attr(a)
	}
	return redactHandler{h: h.h.WithAttrs(out), red: h.red}
}

func (h redactHandler) WithGroup(name string) slog.Handler {
	return redactHandler{h: h.h.WithGroup(name), red: h.red}
}

func (h redactHandler) attr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, h.red.Redact(v.String()))
	case slog.KindAny:
		// Formatted here, since the handler would format it unredacted.
		return slog.String(a.Key, h.red.Redact(fmt.Sprint(v.Any())))
	case slog.KindGroup:
		group := v.Group()
		out := make([]slog.Attr, len(group))
		for i, x := range group {
			out[i] = h.attr(x)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(out...)}
	default:
		return a
	}
}

// redactWriter masks secrets line by line, so a secret is caught even when a
// process writes it in several chunks, as long as it stays on one line.
type redactWriter struct {
	mu  sync.Mutex
	red *Redactor
	w   io.WriteCloser
	buf []byte
}

// maxPending flushes an endless line rather than buffering it forever.
const maxPending = 64 << 10

func newRedactWriter(red *Redactor, w io.WriteCloser) *redactWriter {
	return &redactWriter{red: red, w: w}
}

func (w *redactWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.emit(w.buf[:i+1])
		w.buf = w.buf[i+1:]
	}
	if len(w.buf) > maxPending {
		w.emit(w.buf)
		w.buf = nil
	}
	return len(p), nil
}

func (w *redactWriter) emit(b []byte) {
	_, _ = io.WriteString(w.w, w.red.Redact(string(b)))
}

func (w *redactWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buf) > 0 {
		w.emit(w.buf)
		w.buf = nil
	}
	return w.w.Close()
}
