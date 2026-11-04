package manage

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SkyeBeFreeman/maple/internal/config"
	"github.com/SkyeBeFreeman/maple/internal/manage/agentconf"
	"github.com/SkyeBeFreeman/maple/internal/platform/keychain"
	"github.com/SkyeBeFreeman/maple/internal/platform/launchd"
	"github.com/SkyeBeFreeman/maple/internal/testutil"
)

func init() {
	// Tests never drive the real launchd unless explicitly asked to.
	if os.Getenv("MAPLE_TEST_LAUNCHD") != "1" {
		launchctl = nil
	}
}

// freshHome has the sample agent files but no ~/.maple yet.
func freshHome(t *testing.T) Env {
	t.Helper()
	e := setup(t, "")
	if err := os.RemoveAll(e.Paths.MapleDir()); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestInstallIdempotent(t *testing.T) {
	e := freshHome(t)
	ctx := context.Background()
	port := testutil.FreePort(t)
	res, err := Install(ctx, e, InstallOptions{Port: port, NoLaunchd: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Created) == 0 {
		t.Fatal("first install created nothing")
	}
	if got := loadConfig(t, e).Port; got != port {
		t.Errorf("port = %d, want %d", got, port)
	}
	tok, err := config.LoadToken(e.Paths)
	if err != nil {
		t.Fatal(err)
	}
	cfgBefore := readFile(t, e.Paths.Config())

	res, err = Install(ctx, e, InstallOptions{Port: port + 1, NoLaunchd: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Created) != 0 {
		t.Errorf("second Created = %v", res.Created)
	}
	if readFile(t, e.Paths.Config()) != cfgBefore {
		t.Error("config rewritten")
	}
	// An ignored --port must be said, not silently dropped.
	if !strings.Contains(res.Note, "port "+strconv.Itoa(port)+" unchanged") || !strings.Contains(res.Note, "maple sync") {
		t.Errorf("Note = %q", res.Note)
	}
	for _, p := range []int{0, port} {
		res, err = Install(ctx, e, InstallOptions{Port: p, NoLaunchd: true})
		if err != nil {
			t.Fatal(err)
		}
		if res.Note != "" {
			t.Errorf("--port %d: Note = %q, want none", p, res.Note)
		}
	}
	if tok2, _ := config.LoadToken(e.Paths); tok2 != tok {
		t.Error("token regenerated")
	}
	for _, a := range agentconf.All() {
		c, ok := entryArgs(t, e, a, agentconf.GatewayName)
		if !ok || !strings.Contains(c, "Bearer "+tok) || !strings.Contains(c, strconv.Itoa(port)) {
			t.Errorf("%s: gateway entry = %s", a.ID(), c)
		}
	}
	if readFile(t, e.Paths.SkillFile()) != string(skillMD) {
		t.Error("SKILL.md differs from embedded")
	}
}

func TestInstallPortInUse(t *testing.T) {
	e := freshHome(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port
	_, err = Install(context.Background(), e, InstallOptions{Port: port, NoLaunchd: true})
	if err == nil || !strings.Contains(err.Error(), "port "+strconv.Itoa(port)+" is in use; rerun with --port") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(e.Paths.Config()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("config written: %v", err)
	}
}

func TestUninstallRestoresUserFiles(t *testing.T) {
	e := freshHome(t)
	ctx := context.Background()
	before := snapshot(t, e)
	if _, err := Install(ctx, e, InstallOptions{Port: testutil.FreePort(t), NoLaunchd: true}); err != nil {
		t.Fatal(err)
	}
	if err := Uninstall(ctx, e, UninstallOptions{NoLaunchd: true}); err != nil {
		t.Fatal(err)
	}
	for f, want := range before {
		if got := readFile(t, f); got != want {
			t.Errorf("%s not restored:\n%s", f, got)
		}
	}
	if _, err := os.Stat(e.Paths.SkillFile()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("SKILL.md left: %v", err)
	}
	if _, err := os.Stat(e.Paths.Config()); err != nil {
		t.Errorf("config removed without purge: %v", err)
	}
}

func TestUninstallPurge(t *testing.T) {
	e := freshHome(t)
	ctx := context.Background()
	if _, err := Install(ctx, e, InstallOptions{Port: testutil.FreePort(t), NoLaunchd: true}); err != nil {
		t.Fatal(err)
	}
	kc := e.Keychain.(*keychain.Memory)
	_ = kc.Set("A", "1")
	if err := Uninstall(ctx, e, UninstallOptions{Purge: true, NoLaunchd: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(e.Paths.MapleDir()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("~/.maple left: %v", err)
	}
	if names, _ := kc.List(); len(names) != 0 {
		t.Errorf("keychain = %v", names)
	}
}

// fakeLaunchd records calls; its first Load fails like the bootout/bootstrap
// race does.
type fakeLaunchd struct {
	loads, unloads int
	loaded         bool
}

func useFakeLaunchd(t *testing.T) *fakeLaunchd {
	t.Helper()
	f := &fakeLaunchd{}
	prev, prevDelay := launchctl, bootstrapRetryDelay
	launchctl = &launchdOps{
		Load: func(context.Context, string) error {
			f.loads++
			if f.loads == 1 {
				return errors.New("launchctl bootstrap gui/501 x: exit status 5: Bootstrap failed: 5: Input/output error")
			}
			f.loaded = true
			return nil
		},
		Unload: func(context.Context) error {
			f.unloads++
			f.loaded = false
			return nil
		},
		Loaded: func(context.Context) (bool, error) { return f.loaded, nil },
	}
	bootstrapRetryDelay = time.Millisecond
	t.Cleanup(func() { launchctl, bootstrapRetryDelay = prev, prevDelay })
	return f
}

func TestInstallLaunchd(t *testing.T) {
	e := freshHome(t)
	f := useFakeLaunchd(t)
	ctx := context.Background()
	if _, err := Install(ctx, e, InstallOptions{Port: testutil.FreePort(t)}); err != nil {
		t.Fatal(err)
	}
	plist := launchd.PlistPath(e.Paths.Home)
	want := string(launchd.Plist(testBin, filepath.Join(e.Paths.LogDir(), "launchd.log")))
	if got := readFile(t, plist); got != want {
		t.Errorf("plist = %s", got)
	}
	if f.loads != 2 || !f.loaded {
		t.Errorf("loads = %d loaded = %v", f.loads, f.loaded)
	}
	res, err := Install(ctx, e, InstallOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if f.loads != 2 || len(res.Created) != 0 {
		t.Errorf("reinstall: loads = %d created = %v", f.loads, res.Created)
	}
	if err := Uninstall(ctx, e, UninstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if f.unloads != 1 || f.loaded {
		t.Errorf("unloads = %d", f.unloads)
	}
	if _, err := os.Stat(plist); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("plist left: %v", err)
	}
}

func TestSkillMD(t *testing.T) {
	s := string(skillMD)
	if n := strings.Count(s, "\n"); n > 40 {
		t.Errorf("SKILL.md has %d lines", n)
	}
	for _, want := range []string{"name: maple", "maple mcp list --json", "maple mcp add", "maple mcp rm", "maple status --json", "maple doctor", "maple secret set"} {
		if !strings.Contains(s, want) {
			t.Errorf("SKILL.md lacks %q", want)
		}
	}
}

func TestUninstallLeavesHandEdits(t *testing.T) {
	e := freshHome(t)
	ctx := context.Background()
	if _, err := Install(ctx, e, InstallOptions{Port: testutil.FreePort(t), NoLaunchd: true}); err != nil {
		t.Fatal(err)
	}
	path := e.Paths.ClaudeJSON()
	data, err := agentconf.Claude.Set([]byte(readFile(t, path)), agentconf.Entry{Name: agentconf.GatewayName, URL: "http://127.0.0.1:1/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(data))
	err = Uninstall(ctx, e, UninstallOptions{NoLaunchd: true})
	if err == nil || !strings.Contains(err.Error(), "claude/maple") {
		t.Fatalf("err = %v", err)
	}
	if _, ok := entryArgs(t, e, agentconf.Claude, agentconf.GatewayName); !ok {
		t.Error("hand-edited entry removed")
	}
	if _, ok := entryArgs(t, e, agentconf.Codex, agentconf.GatewayName); ok {
		t.Error("codex entry left")
	}
}

func TestUninstallNoLaunchdRemovesPlist(t *testing.T) {
	e := freshHome(t)
	plist := launchd.PlistPath(e.Paths.Home)
	writeFile(t, plist, "x")
	if err := Uninstall(context.Background(), e, UninstallOptions{NoLaunchd: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(plist); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("plist left: %v", err)
	}
}
