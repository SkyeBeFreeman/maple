package testutil

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func newClient() *mcp.Client {
	return mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
}

func TestFakeMCPRoundTrip(t *testing.T) {
	bin := BuildFakeMCP(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.Command(bin, "-name", "fake", "-extra", "2", "-bad-schema")
	sess, err := newClient().Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	if got := sess.InitializeResult().ServerInfo.Name; got != "fake" {
		t.Errorf("server name = %q", got)
	}
	lt, err := sess.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range lt.Tools {
		names = append(names, tl.Name)
	}
	for _, want := range []string{"echo", "env", "sleep", "crash", "t0", "t1", "bad"} {
		if !slices.Contains(names, want) {
			t.Errorf("tools %v missing %q", names, want)
		}
	}
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{"text": "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := Text(t, res); got != "hi" {
		t.Errorf("echo = %q", got)
	}
	res, err = sess.CallTool(ctx, &mcp.CallToolParams{Name: "bad", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if got := Text(t, res); got != "bad" {
		t.Errorf("bad = %q", got)
	}
}

func TestHTTPServerRequiresHeader(t *testing.T) {
	for _, tr := range []string{"http", "sse"} {
		t.Run(tr, func(t *testing.T) {
			srv := HTTPServer(t, tr, [2]string{"X-Token", "secret"}, FakeOptions{Name: "h"})
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			mk := func(hc *http.Client) mcp.Transport {
				if tr == "sse" {
					return &mcp.SSEClientTransport{Endpoint: srv.URL, HTTPClient: hc}
				}
				return &mcp.StreamableClientTransport{Endpoint: srv.URL, HTTPClient: hc, DisableStandaloneSSE: true}
			}
			if s, err := newClient().Connect(ctx, mk(http.DefaultClient), nil); err == nil {
				_ = s.Close()
				t.Fatal("connect without header succeeded")
			}
			hc := &http.Client{Transport: HeaderTransport{"X-Token": "secret"}}
			s, err := newClient().Connect(ctx, mk(hc), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			res, err := s.CallTool(ctx, &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{"text": "yo"}})
			if err != nil {
				t.Fatal(err)
			}
			if got := Text(t, res); got != "yo" {
				t.Errorf("echo = %q", got)
			}
		})
	}
}

func TestOAuthServer(t *testing.T) {
	srv := OAuthServer(t)
	resp, err := http.Get(srv.URL + "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 401 || resp.Header.Get("WWW-Authenticate") != `Bearer resource_metadata="`+srv.URL+`/.well-known/oauth-protected-resource"` {
		t.Errorf("got %d %q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}
}

func TestFakeMCPStartFlags(t *testing.T) {
	bin := BuildFakeMCP(t)
	log := filepath.Join(t.TempDir(), "start.log")
	for range 2 {
		cmd := exec.Command(bin, "-start-log", log, "-stderr", "hello", "-exit-on-start", "3")
		var stderr strings.Builder
		cmd.Stderr = &stderr
		err := cmd.Run()
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 3 {
			t.Fatalf("exit = %v", err)
		}
		if stderr.String() != "hello\n" {
			t.Errorf("stderr = %q", stderr.String())
		}
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Split(strings.TrimSpace(string(b)), "\n"); len(lines) != 2 {
		t.Errorf("start log = %q", b)
	}
}

func TestFakeMCPTools(t *testing.T) {
	bin := BuildFakeMCP(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var progress atomic.Int32
	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, &mcp.ClientOptions{
		ProgressNotificationHandler: func(context.Context, *mcp.ProgressNotificationClientRequest) { progress.Add(1) },
	})
	cmd := exec.Command(bin, "-instructions", "use me", "-description", "desc")
	cmd.Env = append(os.Environ(), "FAKEMCP_PROBE=42")
	cmd.Dir = t.TempDir()
	var stderr syncBuf
	cmd.Stderr = &stderr
	sess, err := c.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	ir := sess.InitializeResult()
	if ir.Instructions != "use me" || ir.ServerInfo.Description != "desc" {
		t.Errorf("init = %q %q", ir.Instructions, ir.ServerInfo.Description)
	}

	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "env", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Cwd string            `json:"cwd"`
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal([]byte(Text(t, res)), &got); err != nil {
		t.Fatal(err)
	}
	wantDir, _ := filepath.EvalSymlinks(cmd.Dir)
	if gotDir, _ := filepath.EvalSymlinks(got.Cwd); gotDir != wantDir || got.Env["FAKEMCP_PROBE"] != "42" {
		t.Errorf("env = %+v", got)
	}

	p := &mcp.CallToolParams{Name: "sleep", Arguments: map[string]any{"ms": 50}}
	p.SetProgressToken("tok")
	if res, err = sess.CallTool(ctx, p); err != nil || res.IsError {
		t.Fatalf("sleep = %v %v", res, err)
	}
	if progress.Load() == 0 {
		t.Error("no progress notifications")
	}

	short, cancelShort := context.WithTimeout(ctx, 50*time.Millisecond)
	start := time.Now()
	_, err = sess.CallTool(short, &mcp.CallToolParams{Name: "sleep", Arguments: map[string]any{"ms": 10000}})
	cancelShort()
	if err == nil || time.Since(start) > 5*time.Second {
		t.Errorf("cancelled sleep: err=%v after %v", err, time.Since(start))
	}

	if _, err = sess.CallTool(ctx, &mcp.CallToolParams{Name: "crash", Arguments: map[string]any{"code": 7}}); err == nil {
		t.Error("crash call succeeded")
	}
	_ = sess.Wait()
	if cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != 7 {
		t.Errorf("exit state = %v", cmd.ProcessState)
	}
	if !strings.Contains(stderr.String(), "boom\n") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

type syncBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestFakeMCPExtraFile(t *testing.T) {
	bin := BuildFakeMCP(t)
	file := filepath.Join(t.TempDir(), "extra")
	count := func() int {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		sess, err := newClient().Connect(ctx, &mcp.CommandTransport{Command: exec.Command(bin, "-extra", "5", "-extra-file", file)}, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = sess.Close() }()
		lt, err := sess.ListTools(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		return len(lt.Tools)
	}
	if n := count(); n != 4 {
		t.Fatalf("missing file: %d tools, want 4", n)
	}
	if err := os.WriteFile(file, []byte("2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 6 {
		t.Fatalf("file with 2: %d tools, want 6", n)
	}
}

// post sends one JSON-RPC message the way the SDK client does.
func post(ctx context.Context, url, body string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	return http.DefaultClient.Do(req)
}

func TestHTTPServerHolds(t *testing.T) {
	const discover = `{"jsonrpc":"2.0","id":7,"method":"server/discover","params":{}}`
	const initialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`
	held := func(t *testing.T, do func(ctx context.Context) (*http.Response, error)) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		resp, err := do(ctx)
		if err == nil {
			_ = resp.Body.Close()
			t.Fatalf("answered with %d, want held", resp.StatusCode)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want deadline exceeded", err)
		}
	}

	t.Run("standalone GET", func(t *testing.T) {
		srv := HTTPServer(t, "http", [2]string{}, FakeOptions{HoldStandaloneGET: true})
		held(t, func(ctx context.Context) (*http.Response, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
			if err != nil {
				return nil, err
			}
			req.Header.Set("Accept", "text/event-stream")
			return http.DefaultClient.Do(req)
		})
		// Rejecting discover is what sends the client down the legacy path.
		resp, err := post(context.Background(), srv.URL, discover)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var got struct {
			ID    int `json:"id"`
			Error struct {
				Code int `json:"code"`
			} `json:"error"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK || got.ID != 7 || got.Error.Code != -32601 {
			t.Fatalf("discover = %d %+v, want 200 with method not found for id 7", resp.StatusCode, got)
		}
	})

	t.Run("initialize", func(t *testing.T) {
		srv := HTTPServer(t, "http", [2]string{}, FakeOptions{HoldInitialize: true})
		for _, body := range []string{discover, initialize} {
			held(t, func(ctx context.Context) (*http.Response, error) { return post(ctx, srv.URL, body) })
		}
	})
}
