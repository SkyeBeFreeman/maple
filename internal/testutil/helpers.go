package testutil

import (
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SkyeBeFreeman/maple/internal/config"
)

const pollInterval = 10 * time.Millisecond

// Gone reports whether kill(pid, 0) says ESRCH within d; a negative pid
// checks a whole process group. An exited process lingers as a zombie until
// its parent reaps it, so a short wait is needed even after it exited.
func Gone(pid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(pollInterval)
	}
	return true
}

// WaitGone fails the test unless pid disappears within d; a lingering pid
// means a process or one of its children leaked.
func WaitGone(t testing.TB, pid int, d time.Duration) {
	t.Helper()
	if !Gone(pid, d) {
		t.Fatalf("pid %d still exists after %v", pid, d)
	}
}

// Eventually fails the test unless cond becomes true within d.
func Eventually(t testing.TB, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(pollInterval)
	}
}

// FreePort returns a loopback port that nothing listens on.
func FreePort(t testing.TB) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

// Text returns the text of a tool result that must hold exactly one text
// content item.
func Text(t testing.TB, res *mcp.CallToolResult) string {
	t.Helper()
	if len(res.Content) != 1 {
		t.Fatalf("content = %d items, want 1", len(res.Content))
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content type %T, want text", res.Content[0])
	}
	return tc.Text
}

// HeaderTransport sets its headers on every request it sends through
// http.DefaultTransport.
type HeaderTransport map[string]string

// RoundTrip implements http.RoundTripper.
func (h HeaderTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range h {
		r.Header.Set(k, v)
	}
	return http.DefaultTransport.RoundTrip(r)
}

// AgentHome returns a temporary HOME holding the sample agent files from
// testdataDir (agentconf/testdata).
func AgentHome(t testing.TB, testdataDir string) config.Paths {
	t.Helper()
	p := config.Paths{Home: t.TempDir()}
	CopyFile(t, filepath.Join(testdataDir, "claude", "input.json"), p.ClaudeJSON())
	CopyFile(t, filepath.Join(testdataDir, "codex", "input.toml"), p.CodexTOML())
	CopyFile(t, filepath.Join(testdataDir, "codebuddy", "input.json"), p.CodeBuddyJSON())
	return p
}

// CopyFile copies src to dst with mode 0600, creating dst's directory.
func CopyFile(t testing.TB, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
