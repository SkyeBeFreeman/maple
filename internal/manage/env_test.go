package manage

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SkyeBeFreeman/maple/internal/config"
	"github.com/SkyeBeFreeman/maple/internal/platform/keychain"
	"github.com/SkyeBeFreeman/maple/internal/testutil"
)

const (
	testBin   = "/opt/maple/bin/maple"
	testToken = "test-token"
)

func init() {
	// Keep "gateway up but never reloads" cases fast.
	reloadTimeout = 300 * time.Millisecond
	reloadPoll = 10 * time.Millisecond
}

// setup builds a HOME with the sample agent files, a token and config.yaml
// whose port has no listener, so no gateway is running by default.
func setup(t *testing.T, configYAML string) Env {
	t.Helper()
	p := testutil.AgentHome(t, filepath.Join("agentconf", "testdata"))
	writeFile(t, p.Token(), testToken+"\n")
	writeFile(t, p.Config(), "port: "+strconv.Itoa(testutil.FreePort(t))+"\n"+configYAML)
	return Env{Paths: p, Keychain: keychain.NewMemory(), MapleBin: testBin}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func agentFiles(e Env) []string {
	return []string{e.Paths.ClaudeJSON(), e.Paths.CodexTOML(), e.Paths.CodeBuddyJSON()}
}

func snapshot(t *testing.T, e Env) map[string]string {
	t.Helper()
	m := map[string]string{}
	for _, f := range agentFiles(e) {
		m[f] = readFile(t, f)
	}
	return m
}

func loadConfig(t *testing.T, e Env) *config.Config {
	t.Helper()
	c, _, err := config.Load(e.Paths)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// fakeGateway serves /status on the port of e's config, checking the token
// and Host like the real gateway. status is called per request.
func fakeGateway(t *testing.T, e Env, status func() config.Status) *atomic.Int32 {
	t.Helper()
	port := loadConfig(t, e).Port
	var hits atomic.Int32
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/status" || r.Host != "127.0.0.1:"+strconv.Itoa(port) ||
			r.Header.Get("Authorization") != "Bearer "+testToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(status())
	}))
	_ = srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	return &hits
}

// currentHash is what a gateway that has reloaded reports.
func currentHash(t *testing.T, e Env) string {
	return config.ContentHash([]byte(readFile(t, e.Paths.Config())))
}
