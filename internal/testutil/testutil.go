// Package testutil provides fake MCP upstreams and shared helpers for tests.
package testutil

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SkyeBeFreeman/maple/internal/testutil/fakemcp/tools"
)

// FakeOptions configures HTTPServer.
type FakeOptions struct {
	Name, Instructions, Description string
	Extra                           int
}

var (
	buildOnce sync.Once
	buildPath string
	buildErr  error
	buildOut  []byte
)

// BuildFakeMCP builds the fakemcp binary once per test process. The output
// directory is not t.TempDir because the binary outlives the first test.
func BuildFakeMCP(t testing.TB) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "maple-fakemcp-")
		if err != nil {
			buildErr = err
			return
		}
		buildPath = filepath.Join(dir, "fakemcp")
		cmd := exec.Command("go", "build", "-o", buildPath, "github.com/SkyeBeFreeman/maple/internal/testutil/fakemcp")
		buildOut, buildErr = cmd.CombinedOutput()
	})
	if buildErr != nil {
		t.Fatalf("build fakemcp: %v\n%s", buildErr, buildOut)
	}
	return buildPath
}

// HTTPServer serves the fake tool set over "http" (streamable) or "sse".
// Requests whose wantHeader[0] header differs from wantHeader[1] get 401.
func HTTPServer(t testing.TB, transport string, wantHeader [2]string, opts FakeOptions) *httptest.Server {
	t.Helper()
	srv := tools.NewServer(tools.Options{Name: opts.Name, Instructions: opts.Instructions, Description: opts.Description, Extra: opts.Extra})
	get := func(*http.Request) *mcp.Server { return srv }
	var h http.Handler
	switch transport {
	case "http":
		h = mcp.NewStreamableHTTPHandler(get, nil)
	case "sse":
		h = mcp.NewSSEHandler(get, nil)
	default:
		t.Fatalf("HTTPServer: unknown transport %q", transport)
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if wantHeader[0] != "" && r.Header.Get(wantHeader[0]) != wantHeader[1] {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts
}

// OAuthServer answers every request with an OAuth protected-resource challenge.
func OAuthServer(t testing.TB) *httptest.Server {
	t.Helper()
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+ts.URL+`/.well-known/oauth-protected-resource"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(ts.Close)
	return ts
}
