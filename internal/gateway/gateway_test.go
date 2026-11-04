package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SkyeBeFreeman/maple/internal/config"
	"github.com/SkyeBeFreeman/maple/internal/platform/keychain"
	"github.com/SkyeBeFreeman/maple/internal/testutil"
)

const testToken = "test-token-0123456789"

type env struct {
	t        *testing.T
	paths    config.Paths
	kc       *keychain.Memory
	bin      string
	startLog string
	url      string
	cancel   context.CancelFunc
	done     chan error
}

func newEnv(t *testing.T) *env {
	t.Helper()
	home := t.TempDir()
	e := &env{
		t:        t,
		paths:    config.Paths{Home: home},
		kc:       keychain.NewMemory(),
		bin:      testutil.BuildFakeMCP(t),
		startLog: filepath.Join(home, "starts.log"),
	}
	if err := os.MkdirAll(e.paths.MapleDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.paths.Token(), []byte(testToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Registered before any start so it runs after every gateway stopped.
	t.Cleanup(func() {
		for _, pid := range e.pids() {
			if !testutil.Gone(pid, 5*time.Second) {
				t.Errorf("pid %d still alive", pid)
			}
		}
	})
	return e
}

// fake is the YAML of a fakemcp server entry with extra lines appended.
func (e *env) fake(extraArgs string, extra string) string {
	args := fmt.Sprintf("[%q, %q%s]", "-start-log", e.startLog, extraArgs)
	return fmt.Sprintf("    command: %s\n    args: %s\n%s", e.bin, args, extra)
}

func (e *env) writeConfig(yaml string) {
	e.t.Helper()
	if err := os.WriteFile(e.paths.Config(), []byte(yaml), 0o600); err != nil {
		e.t.Fatal(err)
	}
}

func testOptions() Options {
	o := DefaultOptions()
	o.Pool.CallTimeout = 10 * time.Second
	o.Pool.IdleTimeout = time.Minute
	// Generous: a fakemcp's first start under a fully parallel -race run (or a
	// slow CI runner) can exceed 5s, and initialize would then answer without
	// instructions by design.
	o.ListWait = 20 * time.Second
	o.InitWait = 20 * time.Second
	o.Debounce = 20 * time.Millisecond
	o.Shell = ""
	o.Version = "test"
	return o
}

func (e *env) start() {
	e.t.Helper()
	e.startOn(listen(e.t), testOptions())
}

func listen(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return ln
}

func (e *env) startOn(ln net.Listener, opts Options) {
	e.t.Helper()
	e.url = "http://" + ln.Addr().String()
	gw := New(e.paths, e.kc, opts)
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	done := make(chan error, 1)
	e.done = done
	go func() { done <- gw.Serve(ctx, ln) }()
	e.t.Cleanup(e.stop)
}

func (e *env) stop() {
	if e.cancel == nil {
		return
	}
	e.cancel()
	e.cancel = nil
	select {
	case err := <-e.done:
		if err != nil {
			e.t.Errorf("Serve: %v", err)
		}
	case <-time.After(15 * time.Second):
		e.t.Fatal("Serve did not return")
	}
}

func (e *env) connect(opts *mcp.ClientOptions) *mcp.ClientSession {
	e.t.Helper()
	c := mcp.NewClient(&mcp.Implementation{Name: "agent", Version: "1"}, opts)
	tr := &mcp.StreamableClientTransport{
		Endpoint:   e.url + "/mcp",
		HTTPClient: &http.Client{Transport: testutil.HeaderTransport{"Authorization": "Bearer " + testToken}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cs, err := c.Connect(ctx, tr, nil)
	if err != nil {
		e.t.Fatalf("connect: %v", err)
	}
	e.t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func (e *env) pids() []int {
	b, err := os.ReadFile(e.startLog)
	if err != nil {
		return nil
	}
	var out []int
	for f := range strings.FieldsSeq(string(b)) {
		if n, err := strconv.Atoi(f); err == nil {
			out = append(out, n)
		}
	}
	return out
}

func (e *env) status(token string) (*http.Response, *config.Status) {
	e.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, e.url+"/status", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return resp, nil
	}
	var st config.Status
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		e.t.Fatal(err)
	}
	return resp, &st
}

func toolNames(t *testing.T, cs *mcp.ClientSession) map[string]*mcp.Tool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	m := map[string]*mcp.Tool{}
	for _, tl := range res.Tools {
		m[tl.Name] = tl
	}
	return m
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args any) (*mcp.CallToolResult, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
}

func TestListAndCall(t *testing.T) {
	e := newEnv(t)
	e.writeConfig("servers:\n  fake:\n" + e.fake("", "    disabled_tools: [crash]\n"))
	e.start()
	cs := e.connect(nil)

	if ins := cs.InitializeResult().Instructions; !strings.HasPrefix(ins, "Tools of ") {
		t.Fatalf("instructions = %q", ins)
	}
	tools := toolNames(t, cs)
	if tools["fake__echo"] == nil {
		t.Fatalf("fake__echo missing: %v", tools)
	}
	if tools["fake__crash"] != nil {
		t.Fatal("disabled fake__crash listed")
	}
	res, err := call(t, cs, "fake__echo", map[string]any{"text": "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || testutil.Text(t, res) != "hi" {
		t.Fatalf("result %+v", res)
	}
}

func TestUnknownToolIsProtocolError(t *testing.T) {
	e := newEnv(t)
	e.writeConfig("servers:\n  fake:\n" + e.fake("", ""))
	e.start()
	cs := e.connect(nil)
	_, err := call(t, cs, "nope__x", map[string]any{})
	var je *jsonrpc.Error
	if !errors.As(err, &je) || je.Code != jsonrpc.CodeInvalidParams {
		t.Fatalf("err = %v, want JSON-RPC -32602", err)
	}
}

func TestNonObjectSchemaIsNormalized(t *testing.T) {
	e := newEnv(t)
	e.writeConfig("servers:\n  fake:\n" + e.fake(`, "-bad-schema"`, ""))
	e.start()
	cs := e.connect(nil)
	bad := toolNames(t, cs)["fake__bad"]
	if bad == nil {
		t.Fatal("fake__bad missing")
	}
	b, _ := json.Marshal(bad.InputSchema)
	if string(b) != `{"type":"object"}` {
		t.Fatalf("schema = %s", b)
	}
	res, err := call(t, cs, "fake__echo", map[string]any{"text": "ok"})
	if err != nil || testutil.Text(t, res) != "ok" {
		t.Fatalf("echo after bad schema: %v %+v", err, res)
	}
}

func TestMissingSecret(t *testing.T) {
	e := newEnv(t)
	ts := testutil.HTTPServer(t, "http", [2]string{"Authorization", "Bearer x"}, testutil.FakeOptions{})
	e.writeConfig("servers:\n  h:\n    url: " + ts.URL + "\n    headers:\n      Authorization: Bearer ${NOPE}\n  fake:\n" + e.fake("", ""))
	// Cached tools keep h listed although it cannot start.
	cfg, _, err := config.Load(e.paths)
	if err != nil {
		t.Fatal(err)
	}
	c := emptyCache()
	c.Servers["h"] = CachedServer{
		Hash:  cfg.Servers["h"].ConnHash(),
		Tools: []*mcp.Tool{{Name: "echo", InputSchema: map[string]any{"type": "object"}}},
	}
	if err := c.Save(e.paths); err != nil {
		t.Fatal(err)
	}
	e.start()
	cs := e.connect(nil)

	res, err := call(t, cs, "h__echo", map[string]any{"text": "hi"})
	if err != nil {
		t.Fatal(err)
	}
	want := "maple: h: missing secret NOPE; run: maple secret set NOPE"
	if !res.IsError || testutil.Text(t, res) != want {
		t.Fatalf("result = %q (isError %v), want %q", testutil.Text(t, res), res.IsError, want)
	}
	res, err = call(t, cs, "fake__echo", map[string]any{"text": "hi"})
	if err != nil || res.IsError || testutil.Text(t, res) != "hi" {
		t.Fatalf("fake__echo: %v %+v", err, res)
	}
}

func TestReloadNotifiesClients(t *testing.T) {
	e := newEnv(t)
	base := "servers:\n  fake:\n" + e.fake("", "")
	e.writeConfig(base)
	e.start()
	changed := make(chan struct{}, 16)
	cs := e.connect(&mcp.ClientOptions{
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) {
			select {
			case changed <- struct{}{}:
			default:
			}
		},
	})
	if toolNames(t, cs)["fake__echo"] == nil {
		t.Fatal("fake__echo missing")
	}
	// Drop notifications from the initial fetch.
	time.Sleep(100 * time.Millisecond)
	for len(changed) > 0 {
		<-changed
	}

	e.writeConfig(base + "  more:\n" + e.fake("", ""))
	select {
	case <-changed:
	case <-time.After(time.Second):
		t.Fatal("no tools/list_changed within 1s")
	}
	testutil.Eventually(t, 5*time.Second, "more__echo", func() bool { return toolNames(t, cs)["more__echo"] != nil })

	e.writeConfig("servers: [oops\n")
	testutil.Eventually(t, 5*time.Second, "reload_error", func() bool {
		_, st := e.status(testToken)
		return st != nil && st.ReloadError != ""
	})
	res, err := call(t, cs, "fake__echo", map[string]any{"text": "still"})
	if err != nil || res.IsError || testutil.Text(t, res) != "still" {
		t.Fatalf("old tool after bad reload: %v %+v", err, res)
	}

	// Restoring the loaded content must clear the error, though the
	// loaded config itself does not change.
	e.writeConfig(base + "  more:\n" + e.fake("", ""))
	testutil.Eventually(t, 5*time.Second, "reload_error cleared", func() bool {
		_, st := e.status(testToken)
		return st != nil && st.ReloadError == ""
	})
}

// seedCache makes the named servers' tools known without starting them.
func (e *env) seedCache(names ...string) {
	e.t.Helper()
	cfg, _, err := config.Load(e.paths)
	if err != nil {
		e.t.Fatal(err)
	}
	c := emptyCache()
	for _, n := range names {
		c.Servers[n] = CachedServer{
			Hash:  cfg.Servers[n].ConnHash(),
			Tools: []*mcp.Tool{{Name: "echo", InputSchema: map[string]any{"type": "object"}}},
		}
	}
	if err := c.Save(e.paths); err != nil {
		e.t.Fatal(err)
	}
}

func TestRemoteErrors(t *testing.T) {
	e := newEnv(t)
	ts := testutil.HTTPServer(t, "http", [2]string{"Authorization", "Bearer right"}, testutil.FakeOptions{})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := "http://" + ln.Addr().String() + "/mcp"
	_ = ln.Close()
	if err := e.kc.Set("TOK", "wrong-token"); err != nil {
		t.Fatal(err)
	}
	e.writeConfig("servers:\n  auth:\n    url: " + ts.URL + "\n    headers:\n      Authorization: Bearer ${TOK}\n" +
		"  gone:\n    url: " + closed + "\n")
	e.seedCache("auth", "gone")
	e.start()
	cs := e.connect(nil)

	res, err := call(t, cs, "auth__echo", map[string]any{"text": "hi"})
	if err != nil {
		t.Fatal(err)
	}
	want := "maple: auth: authentication failed; check the secrets it uses: ${TOK}; run: maple secret set TOK"
	if !res.IsError || testutil.Text(t, res) != want {
		t.Fatalf("auth result = %q, want %q", testutil.Text(t, res), want)
	}
	res, err = call(t, cs, "gone__echo", map[string]any{"text": "hi"})
	if err != nil {
		t.Fatal(err)
	}
	prefix := "maple: gone: cannot reach " + closed + ": "
	if got := testutil.Text(t, res); !res.IsError || !strings.HasPrefix(got, prefix) || !strings.HasSuffix(got, "connection refused") || strings.Contains(got, "initialize") {
		t.Fatalf("unreachable result = %q, want prefix %q and connection refused", got, prefix)
	}
}

// R16: neither a description edit nor disabling a tool restarts the
// upstream or drops its cache; both still reach the agent.
func TestEditKeepsUpstream(t *testing.T) {
	e := newEnv(t)
	e.writeConfig("servers:\n  fake:\n" + e.fake("", ""))
	e.start()
	changed := make(chan struct{}, 16)
	cs := e.connect(&mcp.ClientOptions{
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) {
			select {
			case changed <- struct{}{}:
			default:
			}
		},
	})
	if toolNames(t, cs)["fake__sleep"] == nil {
		t.Fatal("fake__sleep missing")
	}
	before := e.pids()

	e.writeConfig("servers:\n  fake:\n" + e.fake("", "    description: Edited purpose\n    disabled_tools: [sleep]\n"))
	testutil.Eventually(t, 5*time.Second, "fake__sleep removed", func() bool { return toolNames(t, cs)["fake__sleep"] == nil })
	if after := e.pids(); len(after) != len(before) {
		t.Fatalf("starts went from %d to %d", len(before), len(after))
	}
	for _, pid := range before {
		if syscall.Kill(pid, 0) != nil {
			t.Fatalf("pid %d was stopped", pid)
		}
	}
	if ins := e.connect(nil).InitializeResult().Instructions; !strings.Contains(ins, "Edited purpose") || strings.Contains(ins, "sleep") {
		t.Fatalf("instructions not rebuilt: %q", ins)
	}
}

func TestCacheAvoidsStart(t *testing.T) {
	e := newEnv(t)
	e.writeConfig("servers:\n  fake:\n" + e.fake("", ""))
	e.start()
	cs := e.connect(nil)
	if toolNames(t, cs)["fake__echo"] == nil {
		t.Fatal("fake__echo missing")
	}
	_ = cs.Close()
	e.stop()
	before := len(e.pids())
	if before == 0 {
		t.Fatal("first run did not start fakemcp")
	}

	e.start()
	cs = e.connect(nil)
	if toolNames(t, cs)["fake__echo"] == nil {
		t.Fatal("fake__echo missing after restart")
	}
	if after := len(e.pids()); after != before {
		t.Fatalf("starts went from %d to %d", before, after)
	}
}

func TestProgressForwarded(t *testing.T) {
	e := newEnv(t)
	e.writeConfig("servers:\n  fake:\n" + e.fake("", ""))
	e.start()
	tokens := make(chan any, 64)
	cs := e.connect(&mcp.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, r *mcp.ProgressNotificationClientRequest) {
			select {
			case tokens <- r.Params.ProgressToken:
			default:
			}
		},
	})
	toolNames(t, cs)
	p := &mcp.CallToolParams{Name: "fake__sleep", Arguments: map[string]any{"ms": 50}}
	p.SetProgressToken("t1")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	res, err := cs.CallTool(ctx, p)
	if err != nil || res.IsError {
		t.Fatalf("sleep: %v %+v", err, res)
	}
	select {
	case tok := <-tokens:
		if tok != "t1" {
			t.Fatalf("progress token = %v, want t1", tok)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no progress notification")
	}
}

func TestPerSessionEndsWithClient(t *testing.T) {
	e := newEnv(t)
	e.writeConfig("servers:\n  fake:\n" + e.fake("", "    mode: per-session\n"))
	e.start()
	cs := e.connect(nil)
	toolNames(t, cs)
	before := len(e.pids())
	res, err := call(t, cs, "fake__echo", map[string]any{"text": "hi"})
	if err != nil || res.IsError {
		t.Fatalf("echo: %v %+v", err, res)
	}
	pids := e.pids()
	if len(pids) != before+1 {
		t.Fatalf("starts %d -> %d, want one more", before, len(pids))
	}
	pid := pids[len(pids)-1]
	if err := cs.Close(); err != nil {
		t.Fatal(err)
	}
	testutil.WaitGone(t, pid, time.Second)
}

func TestSecretsNotLogged(t *testing.T) {
	const secret = "s3cr3t-value"
	e := newEnv(t)
	if err := e.kc.Set("SEC", secret); err != nil {
		t.Fatal(err)
	}
	e.writeConfig("servers:\n  fake:\n" + e.fake(`, "-stderr", "${SEC}"`, "    env:\n      TOKEN: ${SEC}\n"))
	e.start()
	cs := e.connect(nil)
	toolNames(t, cs)
	res, err := call(t, cs, "fake__crash", map[string]any{"code": 3})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatalf("crash result not an error: %+v", res)
	}
	if strings.Contains(testutil.Text(t, res), secret) {
		t.Fatalf("result leaks secret: %q", testutil.Text(t, res))
	}
	_ = cs.Close()
	e.stop()

	files, _ := filepath.Glob(filepath.Join(e.paths.LogDir(), "*"))
	if len(files) == 0 {
		t.Fatal("no log files written")
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), secret) {
			t.Fatalf("%s leaks secret:\n%s", f, b)
		}
	}
}

func TestStatus(t *testing.T) {
	e := newEnv(t)
	e.writeConfig("servers:\n  fake:\n" + e.fake("", "") + "  off:\n" + e.fake("", "    enabled: false\n"))
	e.start()
	cs := e.connect(nil)
	toolNames(t, cs)

	resp, _ := e.status("")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: status %d", resp.StatusCode)
	}
	_, st := e.status(testToken)
	if st == nil {
		t.Fatal("status failed")
	}
	raw, _ := os.ReadFile(e.paths.Config())
	if st.ConfigHash != config.ContentHash(raw) || st.Version != "test" || st.ReloadError != "" {
		t.Fatalf("status %+v", st)
	}
	got := map[string]config.ServerStatus{}
	for _, s := range st.Servers {
		got[s.Name] = s
	}
	if s := got["fake"]; s.State != config.StateRunning || s.Tools == 0 || s.PID == 0 {
		t.Fatalf("fake status %+v", s)
	}
	if s := got["off"]; s.State != config.StateStopped || s.Tools != 0 {
		t.Fatalf("off status %+v", s)
	}
}

// A rewritten token file must take effect without a restart: the old token
// stops working and the new one starts.
func TestTokenRotation(t *testing.T) {
	e := newEnv(t)
	e.writeConfig("servers: {}\n")
	e.start()
	if resp, _ := e.status(testToken); resp.StatusCode != http.StatusOK {
		t.Fatalf("initial token: status %d", resp.StatusCode)
	}
	const rotated = "rotated-token-9876543210"
	if err := os.WriteFile(e.paths.Token(), []byte(rotated+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testutil.Eventually(t, 5*time.Second, "new token accepted", func() bool {
		resp, _ := e.status(rotated)
		return resp.StatusCode == http.StatusOK
	})
	if resp, _ := e.status(testToken); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old token: status %d, want 401", resp.StatusCode)
	}
}

func TestPortChangeStopsServe(t *testing.T) {
	e := newEnv(t)
	ln := listen(t)
	opts := testOptions()
	opts.Port = ln.Addr().(*net.TCPAddr).Port
	e.writeConfig(fmt.Sprintf("port: %d\nservers:\n  fake:\n%s", opts.Port, e.fake("", "")))
	e.startOn(ln, opts)
	cs := e.connect(nil)
	if toolNames(t, cs)["fake__echo"] == nil {
		t.Fatal("fake__echo missing")
	}
	if len(e.pids()) == 0 {
		t.Fatal("no upstream started")
	}

	other := opts.Port + 1
	if other > 65535 {
		other = opts.Port - 1
	}
	e.writeConfig(fmt.Sprintf("port: %d\nservers:\n  fake:\n%s", other, e.fake("", "")))
	select {
	case err := <-e.done:
		e.cancel()
		e.cancel = nil
		if !errors.Is(err, ErrPortChanged) {
			t.Fatalf("Serve = %v, want ErrPortChanged", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve kept running after the port changed")
	}
	for _, pid := range e.pids() {
		if !testutil.Gone(pid, 5*time.Second) {
			t.Errorf("pid %d still alive", pid)
		}
	}
}

// cachedAt is when cache.json says name was last listed.
func (e *env) cachedAt(name string) time.Time {
	e.t.Helper()
	cs, ok := LoadCache(e.paths).Servers[name]
	if !ok {
		e.t.Fatalf("%s not in cache.json", name)
	}
	return cs.UpdatedAt
}

// An upstream that never announces tool changes, as remote ones cannot with
// their standalone SSE stream off, is relisted once it reconnects.
func TestRelistOnReconnect(t *testing.T) {
	e := newEnv(t)
	extra := filepath.Join(e.paths.Home, "extra")
	setExtra := func(n int) {
		t.Helper()
		if err := os.WriteFile(extra, []byte(strconv.Itoa(n)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	setExtra(1)
	e.writeConfig("servers:\n  fake:\n" + e.fake(fmt.Sprintf(", %q, %q", "-extra-file", extra), ""))
	opts := testOptions()
	// Long enough that the relist reuses the instance the call started,
	// short enough to wait for its reaping.
	opts.Pool.IdleTimeout = 300 * time.Millisecond
	opts.RelistAfter = time.Millisecond
	e.startOn(listen(t), opts)
	changed := make(chan struct{}, 16)
	cs := e.connect(&mcp.ClientOptions{
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) {
			select {
			case changed <- struct{}{}:
			default:
			}
		},
	})
	if tools := toolNames(t, cs); tools["fake__t0"] == nil || tools["fake__t1"] != nil {
		t.Fatalf("tools = %v, want t0 and no t1", slices.Sorted(maps.Keys(tools)))
	}
	listedAt := e.cachedAt("fake")
	pids := e.pids()
	if len(pids) != 1 {
		t.Fatalf("starts = %d, want 1", len(pids))
	}

	setExtra(3)
	testutil.WaitGone(t, pids[0], 5*time.Second)
	// The first listing's own connection must not have relisted.
	if at := e.cachedAt("fake"); !at.Equal(listedAt) {
		t.Fatalf("cache rewritten at %v after the listing at %v", at, listedAt)
	}
	for len(changed) > 0 {
		<-changed
	}

	res, err := call(t, cs, "fake__echo", map[string]any{"text": "hi"})
	if err != nil || res.IsError || testutil.Text(t, res) != "hi" {
		t.Fatalf("echo: %v %+v", err, res)
	}
	select {
	case <-changed:
	case <-time.After(5 * time.Second):
		t.Fatal("no tools/list_changed after the reconnect")
	}
	testutil.Eventually(t, 5*time.Second, "fake__t2", func() bool { return toolNames(t, cs)["fake__t2"] != nil })
	if n := len(e.pids()); n != 2 {
		t.Fatalf("starts = %d, want 2", n)
	}
}

// Every rule of relisting on connect; TestRelistOnReconnect covers the one
// case that relists end to end.
func TestDialErrorTimeoutNamesURLOnce(t *testing.T) {
	g := New(config.Paths{Home: t.TempDir()}, nil, Options{Logger: slog.New(slog.DiscardHandler)})
	t.Cleanup(g.cancelBg)
	s := &config.Server{URL: "http://127.0.0.1:1/mcp"}
	err := g.dialError("slow", s, fmt.Errorf("connect %s: %w", s.URL, context.DeadlineExceeded))
	want := "maple: slow: cannot reach http://127.0.0.1:1/mcp: no answer within the connect timeout"
	if err.Error() != want {
		t.Fatalf("dialError = %q, want %q", err.Error(), want)
	}
}

func TestRelistDue(t *testing.T) {
	g := New(config.Paths{Home: t.TempDir()}, nil, Options{Logger: slog.New(slog.DiscardHandler), RelistAfter: time.Minute})
	t.Cleanup(g.cancelBg)
	cfg, err := config.Parse([]byte("servers:\n" +
		"  stale:\n    url: http://127.0.0.1:1/a\n" +
		"  fresh:\n    url: http://127.0.0.1:1/b\n" +
		"  busy:\n    url: http://127.0.0.1:1/c\n" +
		"  uncached:\n    url: http://127.0.0.1:1/d\n" +
		"  edited:\n    url: http://127.0.0.1:1/e\n" +
		"  local:\n    command: fakemcp\n" +
		"  per:\n    command: fakemcp\n    mode: per-session\n"))
	if err != nil {
		t.Fatal(err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.setConfigLocked(cfg)
	old := time.Now().Add(-time.Hour)
	for name, at := range map[string]time.Time{"stale": old, "fresh": time.Now(), "busy": old, "local": old, "per": old} {
		g.cache.Servers[name] = CachedServer{Hash: g.connHashes[name], UpdatedAt: at}
	}
	g.cache.Servers["edited"] = CachedServer{Hash: "listed under an older config", UpdatedAt: old}
	g.fetching["busy"] = make(chan struct{})
	for name, want := range map[string]bool{
		"stale": true, "local": true,
		"fresh": false, "busy": false, "uncached": false, "edited": false, "per": false, "unknown": false,
	} {
		if got := g.relistDueLocked(name); got != want {
			t.Errorf("%s: relist = %v, want %v", name, got, want)
		}
	}
}
