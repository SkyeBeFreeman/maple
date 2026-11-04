// Package testutil provides fake MCP upstreams and shared helpers for tests.
package testutil

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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
	// HoldStandaloneGET leaves every GET without response headers until the
	// request ends, like gateways that hold the standalone SSE stream. It
	// also rejects server/discover as pre-2026-07-28 servers do: only the
	// legacy handshake opens that stream. "http" only.
	HoldStandaloneGET bool
	// HoldInitialize never answers the handshake, i.e. initialize and the
	// server/discover that newer clients send first.
	HoldInitialize bool
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
	if opts.HoldStandaloneGET && transport != "http" {
		t.Fatalf("HTTPServer: HoldStandaloneGET needs transport http, not %q", transport)
	}
	// release ends held requests before ts.Close, which would otherwise wait
	// for them forever when a client never gives up.
	release := make(chan struct{})
	hold := func(r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if wantHeader[0] != "" && r.Header.Get(wantHeader[0]) != wantHeader[1] {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if opts.HoldStandaloneGET && r.Method == http.MethodGet {
			hold(r)
			return
		}
		if (opts.HoldStandaloneGET || opts.HoldInitialize) && r.Method == http.MethodPost {
			id, method, ok := peekCall(r)
			if !ok {
				http.Error(w, "cannot read body", http.StatusBadRequest)
				return
			}
			switch {
			case opts.HoldInitialize && (method == "initialize" || method == "server/discover"):
				hold(r)
				return
			case opts.HoldStandaloneGET && method == "server/discover":
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"method not found"}}`, id)
				return
			}
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	t.Cleanup(func() { close(release) })
	return ts
}

// peekCall returns the id and method of a single JSON-RPC message in r's
// body and leaves the body readable for the real handler.
func peekCall(r *http.Request) (id json.RawMessage, method string, ok bool) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, "", false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	var msg struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	// Anything else (a batch, a response) is passed on untouched.
	_ = json.Unmarshal(body, &msg)
	return msg.ID, msg.Method, true
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
