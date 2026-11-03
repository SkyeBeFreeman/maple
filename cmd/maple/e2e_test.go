package main

import (
	"bytes"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"github.com/SkyeBeFreeman/maple/internal/testutil"
)

type e2e struct {
	t    *testing.T
	bin  string
	home string
}

func (e *e2e) run(stdin string, args ...string) (string, error) {
	e.t.Helper()
	cmd := exec.Command(e.bin, args...)
	// A minimal environment: nothing from the developer's HOME may leak in.
	cmd.Env = []string{
		"HOME=" + e.home,
		"MAPLE_KEYCHAIN=file",
		"PATH=" + os.Getenv("PATH"),
		"SHELL=/bin/sh",
	}
	cmd.Stdin = strings.NewReader(stdin)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err := cmd.Run()
	if err != nil {
		e.t.Logf("maple %v: %v\nstderr: %s", args, err, errOut.String())
	}
	return out.String(), err
}

func (e *e2e) mustRun(stdin string, args ...string) string {
	e.t.Helper()
	out, err := e.run(stdin, args...)
	if err != nil {
		e.t.Fatalf("maple %v failed: %v\nstdout: %s", args, err, out)
	}
	return out
}

func copyFile(t *testing.T, src, dst string) []byte {
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
	return data
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestEndToEnd(t *testing.T) {
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "maple")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build maple: %v\n%s", err, out)
	}
	realBin, err := filepath.EvalSymlinks(bin)
	if err != nil {
		t.Fatal(err)
	}
	fake := testutil.BuildFakeMCP(t)
	home := filepath.Join(tmp, "home")
	td := filepath.Join("..", "..", "internal", "manage", "agentconf", "testdata")
	claudeOrig := copyFile(t, filepath.Join(td, "claude", "input.json"), filepath.Join(home, ".claude.json"))
	codexOrig := copyFile(t, filepath.Join(td, "codex", "input.toml"), filepath.Join(home, ".codex", "config.toml"))
	cbOrig := copyFile(t, filepath.Join(td, "codebuddy", "input.json"), filepath.Join(home, ".codebuddy", "mcp.json"))
	agentFiles := []string{
		filepath.Join(home, ".claude.json"),
		filepath.Join(home, ".codex", "config.toml"),
		filepath.Join(home, ".codebuddy", "mcp.json"),
	}
	e := &e2e{t: t, bin: bin, home: home}

	// A free port, so a gateway the developer runs on 47321 cannot interfere.
	out := e.mustRun("", "install", "--no-launchd", "--port", strconv.Itoa(freePort(t)), "--json")
	if !strings.Contains(out, "created") {
		t.Fatalf("install output lacks created: %s", out)
	}

	e.mustRun("", "mcp", "add", "demo", "--json", "--", fake)
	if out := e.mustRun("", "mcp", "list", "--json"); !strings.Contains(out, `"demo"`) {
		t.Fatalf("mcp list lacks demo: %s", out)
	}

	e.mustRun("", "mcp", "add", "d2", "--mode", "direct", "--", fake)
	for _, f := range agentFiles {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(data, []byte("d2")) || !bytes.Contains(data, []byte(realBin)) {
			t.Fatalf("%s lacks direct entry d2 running %s:\n%s", f, realBin, data)
		}
	}

	setOut := e.mustRun("v", "secret", "set", "X", "--stdin")
	listOut := e.mustRun("", "secret", "list", "--json")
	var names []string
	if err := json.Unmarshal([]byte(listOut), &names); err != nil || len(names) != 1 || names[0] != "X" {
		t.Fatalf("secret list = %q, want [\"X\"] (err %v)", listOut, err)
	}
	if strings.Contains(setOut+listOut, "v") {
		t.Fatalf("secret value leaked: set=%q list=%q", setOut, listOut)
	}

	out = e.mustRun("", "mcp", "import", "fetch", "--remove-originals", "--json")
	var imp struct{ Imported []string }
	if err := json.Unmarshal([]byte(out), &imp); err != nil || len(imp.Imported) != 1 || imp.Imported[0] != "fetch" {
		t.Fatalf("import output = %s (err %v)", out, err)
	}
	cb, _ := os.ReadFile(agentFiles[2])
	if gjson.GetBytes(cb, "mcpServers.fetch").Exists() {
		t.Fatalf("fetch was not removed from CodeBuddy:\n%s", cb)
	}

	e.mustRun("", "sync", "--json")
	e.mustRun("", "mcp", "rm", "d2")
	e.mustRun("", "uninstall", "--no-launchd")

	for _, c := range []struct {
		path string
		want []byte
	}{{agentFiles[0], claudeOrig}, {agentFiles[1], codexOrig}} {
		got, _ := os.ReadFile(c.path)
		if !bytes.Equal(got, c.want) {
			t.Errorf("%s changed after uninstall:\n%s\nwant:\n%s", c.path, got, c.want)
		}
	}
	cb, _ = os.ReadFile(agentFiles[2])
	if got, want := gjson.GetBytes(cb, "mcpServers.goland").Raw, gjson.GetBytes(cbOrig, "mcpServers.goland").Raw; got != want {
		t.Errorf("CodeBuddy goland entry changed: %s, want %s", got, want)
	}
	if gjson.GetBytes(cb, "mcpServers.maple").Exists() || gjson.GetBytes(cb, "mcpServers.d2").Exists() {
		t.Errorf("maple entries left in CodeBuddy:\n%s", cb)
	}
}
