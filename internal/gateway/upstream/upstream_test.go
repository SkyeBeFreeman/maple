package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SkyeBeFreeman/maple/internal/config"
	"github.com/SkyeBeFreeman/maple/internal/testutil"
)

const testPath = "/usr/bin:/bin"

func stdioSpec(t *testing.T, args ...string) Spec {
	t.Helper()
	return Spec{Name: "fake", Command: testutil.BuildFakeMCP(t), Args: args, Dir: t.TempDir(), PathEnv: testPath}
}

func dial(t *testing.T, s Spec) Session {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sess, err := Dial(ctx, s, Hooks{})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

func callText(t *testing.T, sess Session, name string, args any) string {
	t.Helper()
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("CallTool %s: bad result %+v", name, res)
	}
	return testutil.Text(t, res)
}

func TestStdioDialListCall(t *testing.T) {
	sess := dial(t, stdioSpec(t, "-extra", "3", "-name", "fx"))
	tools, err := sess.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 7 {
		t.Fatalf("got %d tools, want 7", len(tools))
	}
	if got := callText(t, sess, "echo", map[string]any{"text": "hi"}); got != "hi" {
		t.Fatalf("echo = %q", got)
	}
	if sess.PID() <= 0 {
		t.Fatalf("PID = %d", sess.PID())
	}
	if sess.Info().ServerInfo.Name != "fx" {
		t.Fatalf("Info = %+v", sess.Info().ServerInfo)
	}
}

func TestStdioEnvAndDir(t *testing.T) {
	s := stdioSpec(t)
	s.Env = []string{"K=v"}
	t.Setenv("MAPLE_LEAK_CHECK", "x")
	sess := dial(t, s)
	var got struct {
		Cwd string            `json:"cwd"`
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal([]byte(callText(t, sess, "env", map[string]any{})), &got); err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(s.Dir)
	cwd, _ := filepath.EvalSymlinks(got.Cwd)
	if cwd != want {
		t.Fatalf("cwd = %q, want %q", got.Cwd, s.Dir)
	}
	if got.Env["K"] != "v" || got.Env["PATH"] != testPath {
		t.Fatalf("env = %v", got.Env)
	}
	if _, ok := got.Env["MAPLE_LEAK_CHECK"]; ok {
		t.Fatal("maple environment leaked into child")
	}
}

func TestStdioRelativeCommandResolvedAgainstDir(t *testing.T) {
	s := stdioSpec(t)
	if err := os.Link(s.Command, filepath.Join(s.Dir, "server")); err != nil {
		t.Fatal(err)
	}
	s.Command = "./server"
	sess := dial(t, s)
	if got := callText(t, sess, "echo", map[string]any{"text": "rel"}); got != "rel" {
		t.Fatalf("echo = %q", got)
	}
}

func TestStdioCrashReportsStderr(t *testing.T) {
	sess := dial(t, stdioSpec(t))
	pid := sess.PID()
	_, _ = sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "crash", Arguments: map[string]any{"code": 3}})
	select {
	case <-sess.Done():
	case <-time.After(time.Second):
		t.Fatal("Done not closed after crash")
	}
	err := sess.Err()
	if err == nil || !strings.Contains(err.Error(), "code=3") || !strings.Contains(err.Error(), "last stderr: boom") {
		t.Fatalf("Err = %v", err)
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("Close after crash: %v", err)
	}
	testutil.WaitGone(t, pid, 2*time.Second)
}

func TestStdioCloseKillsGroup(t *testing.T) {
	sess := dial(t, stdioSpec(t))
	pid := sess.PID()
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("Kill(pid, 0) = %v, want ESRCH", err)
	}
	select {
	case <-sess.Done():
	case <-time.After(time.Second):
		t.Fatal("Done not closed after Close")
	}
}

func TestStdioDialFailsWithStderr(t *testing.T) {
	s := stdioSpec(t, "-stderr", "bad token", "-exit-on-start", "4")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := Dial(ctx, s, Hooks{})
	if err == nil || !strings.Contains(err.Error(), "bad token") {
		t.Fatalf("Dial err = %v", err)
	}
}

func TestStdioDialMissingCommand(t *testing.T) {
	s := stdioSpec(t)
	s.Command = "maple-no-such-command"
	if _, err := Dial(context.Background(), s, Hooks{}); err == nil {
		t.Fatal("Dial succeeded")
	}
}

func TestStdioDialRespectsContext(t *testing.T) {
	// sleep neither answers initialize nor exits when stdin closes, so only
	// ctx can end the handshake, and the SDK alone would wait 2s to SIGTERM.
	start := filepath.Join(t.TempDir(), "pid")
	s := Spec{Name: "hang", Command: "/bin/sh", Args: []string{"-c", `echo $$ > "$0"; exec sleep 30`, start}, Dir: t.TempDir(), PathEnv: testPath}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	begin := time.Now()
	if _, err := Dial(ctx, s, Hooks{}); err == nil {
		t.Fatal("Dial succeeded")
	}
	if d := time.Since(begin); d > 1500*time.Millisecond {
		t.Fatalf("Dial took %v", d)
	}
	b, err := os.ReadFile(start)
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(b))), &pid); err != nil {
		t.Fatal(err)
	}
	testutil.WaitGone(t, pid, 2*time.Second)
}

func TestStdioProgressHook(t *testing.T) {
	got := make(chan float64, 100)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sess, err := Dial(ctx, stdioSpec(t), Hooks{OnProgress: func(p *mcp.ProgressNotificationParams) { got <- p.Progress }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	p := &mcp.CallToolParams{Name: "sleep", Arguments: map[string]any{"ms": 50}}
	p.SetProgressToken("tok")
	if _, err := sess.CallTool(ctx, p); err != nil {
		t.Fatal(err)
	}
	select {
	case <-got:
	case <-time.After(time.Second):
		t.Fatal("no progress notification")
	}
}

func TestRemoteHTTPAndSSE(t *testing.T) {
	for _, tr := range []string{config.TransportHTTP, config.TransportSSE} {
		t.Run(tr, func(t *testing.T) {
			ts := testutil.HTTPServer(t, tr, [2]string{"Authorization", "Bearer s3"}, testutil.FakeOptions{Extra: 2})
			sess := dial(t, Spec{Name: "r", URL: ts.URL, Transport: tr, Headers: map[string]string{"Authorization": "Bearer s3"}})
			tools, err := sess.ListTools(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(tools) != 6 {
				t.Fatalf("got %d tools", len(tools))
			}
			if got := callText(t, sess, "echo", map[string]any{"text": "r"}); got != "r" {
				t.Fatalf("echo = %q", got)
			}
			if sess.PID() != 0 {
				t.Fatalf("PID = %d", sess.PID())
			}
			if err := sess.Close(); err != nil {
				t.Fatal(err)
			}
			select {
			case <-sess.Done():
			case <-time.After(time.Second):
				t.Fatal("Done not closed")
			}
		})
	}
}

func TestRemoteMissingHeaderFails(t *testing.T) {
	ts := testutil.HTTPServer(t, "http", [2]string{"Authorization", "Bearer s3"}, testutil.FakeOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := Dial(ctx, Spec{Name: "r", URL: ts.URL, Transport: "http"}, Hooks{}); err == nil {
		t.Fatal("Dial succeeded without header")
	}
}

func TestSpecFromConfig(t *testing.T) {
	s := SpecFromConfig("a", &config.Server{Command: "x", Args: []string{"1"}, Env: map[string]string{"B": "2", "A": "1"}}, "/d", "/p", nil)
	if s.Name != "a" || s.Command != "x" || s.Dir != "/d" || s.PathEnv != "/p" || strings.Join(s.Env, ",") != "A=1,B=2" || s.Transport != "" {
		t.Fatalf("stdio spec = %+v", s)
	}
	r := SpecFromConfig("b", &config.Server{URL: "http://h", Headers: map[string]string{"H": "v"}}, "", "", nil)
	if r.URL != "http://h" || r.Transport != config.TransportHTTP || r.Headers["H"] != "v" {
		t.Fatalf("remote spec = %+v", r)
	}
}

func TestLastLineWriter(t *testing.T) {
	var w lastLine
	_, _ = w.Write([]byte("first\nsec"))
	_, _ = w.Write([]byte("ond\n\n  \n"))
	if got := w.String(); got != "second" {
		t.Fatalf("got %q", got)
	}
	_, _ = w.Write([]byte(strings.Repeat("x", 2000)))
	if got := w.String(); len(got) != maxLineLen {
		t.Fatalf("len = %d", len(got))
	}
	_, _ = w.Write([]byte("tail-no-newline"))
	if got := w.String(); len(got) != maxLineLen {
		t.Fatalf("partial line should keep growing to the cap, len = %d", len(got))
	}
}

func TestStdioCloseDuringCall(t *testing.T) {
	sess := dial(t, stdioSpec(t))
	pid := sess.PID()
	go func() {
		_, _ = sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "sleep", Arguments: map[string]any{"ms": 20000}})
	}()
	time.Sleep(100 * time.Millisecond)
	begin := time.Now()
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(begin); d > 5*time.Second {
		t.Fatalf("Close took %v", d)
	}
	testutil.WaitGone(t, pid, 2*time.Second)
}

func TestStdioKilledBySignal(t *testing.T) {
	sess := dial(t, stdioSpec(t))
	if err := syscall.Kill(sess.PID(), syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sess.Done():
	case <-time.After(time.Second):
		t.Fatal("Done not closed")
	}
	if err := sess.Err(); err == nil || err.Error() != "process exited (signal=SIGKILL)" {
		t.Fatalf("Err = %v", err)
	}
}

// A grandchild holding stderr open must neither delay Close for long nor
// survive it.
func TestStdioCloseKillsGrandchild(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	s := stdioSpec(t)
	s.Args = []string{"-c", `sleep 30 & echo $! > "$0"; exec "$1"`, pidFile, s.Command}
	s.Command = "/bin/sh"
	sess := dial(t, s)
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	var child int
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(b))), &child); err != nil {
		t.Fatal(err)
	}
	begin := time.Now()
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(begin); d > 1500*time.Millisecond {
		t.Fatalf("Close took %v", d)
	}
	testutil.WaitGone(t, child, 2*time.Second)
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestStdioFailingStderrTeeKeepsCapturing(t *testing.T) {
	s := stdioSpec(t, "-stderr", "first")
	s.Stderr = failWriter{}
	sess := dial(t, s)
	_, _ = sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "crash", Arguments: map[string]any{"code": 1}})
	<-sess.Done()
	if err := sess.Err(); err == nil || !strings.Contains(err.Error(), "last stderr: boom") {
		t.Fatalf("Err = %v", err)
	}
}

func TestStdioDialCancelledContextStartsNothing(t *testing.T) {
	start := filepath.Join(t.TempDir(), "started")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	begin := time.Now()
	_, err := Dial(ctx, stdioSpec(t, "-start-log", start), Hooks{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Dial err = %v, want context.Canceled", err)
	}
	if d := time.Since(begin); d > 500*time.Millisecond {
		t.Fatalf("Dial took %v", d)
	}
	if _, err := os.Stat(start); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("process was started: %v", err)
	}
}

func TestListToolsFollowsCursor(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "paged", Version: "1"}, &mcp.ServerOptions{PageSize: 2})
	for i := range 5 {
		srv.AddTool(&mcp.Tool{Name: fmt.Sprintf("p%d", i), InputSchema: map[string]any{"type": "object"}},
			func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{}, nil
			})
	}
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil))
	t.Cleanup(ts.Close)
	sess := dial(t, Spec{Name: "paged", URL: ts.URL, Transport: config.TransportHTTP})
	tools, err := sess.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 5 {
		t.Fatalf("got %d tools, want 5", len(tools))
	}
}
