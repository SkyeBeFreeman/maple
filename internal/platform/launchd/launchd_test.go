package launchd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestPlistGolden(t *testing.T) {
	got := Plist("/opt/maple bin/maple", "/h/.maple/logs/launchd.log")
	want, err := os.ReadFile(filepath.Join("testdata", "maple.plist.golden"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("plist mismatch\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestPlistEscapes(t *testing.T) {
	got := string(Plist(`/a&b/<m>"`, "/l"))
	if !strings.Contains(got, "/a&amp;b/&lt;m&gt;&#34;") {
		t.Fatalf("binary not escaped:\n%s", got)
	}
}

// TestPlistParsesWithPlutil checks that escaped values decode back to the
// original strings in Apple's own parser, not just in encoding/xml.
func TestPlistParsesWithPlutil(t *testing.T) {
	plutil, err := exec.LookPath("plutil")
	if err != nil {
		t.Skip("plutil not available")
	}
	bin, logFile := "/opt/a&b <c>/\"q'/maple", "/h/l\tog's.log"
	p := filepath.Join(t.TempDir(), "x.plist")
	if err := os.WriteFile(p, Plist(bin, logFile), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(plutil, "-convert", "json", "-o", "-", p).Output()
	if err != nil {
		t.Fatalf("plutil: %v", err)
	}
	var got struct {
		Label             string
		ProgramArguments  []string
		RunAtLoad         bool
		KeepAlive         bool
		StandardOutPath   string
		StandardErrorPath string
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode %s: %v", out, err)
	}
	if got.Label != Label || !reflect.DeepEqual(got.ProgramArguments, []string{bin, "serve"}) ||
		!got.RunAtLoad || !got.KeepAlive || got.StandardOutPath != logFile || got.StandardErrorPath != logFile {
		t.Fatalf("decoded plist = %+v", got)
	}
}

func TestPlistPath(t *testing.T) {
	want := "/h/Library/LaunchAgents/io.github.skyebefreeman.maple.plist"
	if got := PlistPath("/h"); got != want {
		t.Fatalf("PlistPath = %q, want %q", got, want)
	}
}

// exitErr returns a genuine *exec.ExitError with the given status.
func exitErr(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("/bin/sh", "-c", "exit "+strconv.Itoa(code)).Run()
	if err == nil {
		t.Fatal("expected exit error")
	}
	return err
}

type call struct {
	args []string
	out  string
	err  error
}

func fakeLaunchctl(t *testing.T, script []call) *[][]string {
	t.Helper()
	var got [][]string
	orig := runLaunchctl
	t.Cleanup(func() { runLaunchctl = orig })
	runLaunchctl = func(_ context.Context, args ...string) ([]byte, error) {
		i := len(got)
		got = append(got, args)
		if i >= len(script) {
			t.Fatalf("unexpected launchctl call %v", args)
		}
		if !reflect.DeepEqual(args, script[i].args) {
			t.Fatalf("call %d argv = %v, want %v", i, args, script[i].args)
		}
		return []byte(script[i].out), script[i].err
	}
	return &got
}

func target() string { return "gui/" + strconv.Itoa(os.Getuid()) + "/" + Label }
func domain() string { return "gui/" + strconv.Itoa(os.Getuid()) }

func TestLoadArgv(t *testing.T) {
	cases := map[string]call{
		"exit3":     {err: exitErr(t, 3)},
		"nosuch":    {out: "Boot-out failed: 3: No such process", err: exitErr(t, 1)},
		"notfound":  {out: "Could not find service", err: exitErr(t, 113)},
		"wasloaded": {},
	}
	for name, bo := range cases {
		t.Run(name, func(t *testing.T) {
			bo.args = []string{"bootout", target()}
			got := fakeLaunchctl(t, []call{bo, {args: []string{"bootstrap", domain(), "/p.plist"}}})
			if err := Load(context.Background(), "/p.plist"); err != nil {
				t.Fatalf("Load: %v", err)
			}
			if len(*got) != 2 {
				t.Fatalf("calls = %v", *got)
			}
		})
	}
}

func TestLoadBootoutFailure(t *testing.T) {
	fakeLaunchctl(t, []call{{args: []string{"bootout", target()}, out: "boom", err: exitErr(t, 5)}})
	err := Load(context.Background(), "/p.plist")
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("Load err = %v", err)
	}
}

func TestLoadBootstrapFailure(t *testing.T) {
	fakeLaunchctl(t, []call{
		{args: []string{"bootout", target()}},
		{args: []string{"bootstrap", domain(), "/p.plist"}, out: "Bootstrap failed: 5", err: exitErr(t, 5)},
	})
	if err := Load(context.Background(), "/p.plist"); err == nil {
		t.Fatal("Load succeeded, want error")
	}
}

func TestUnload(t *testing.T) {
	fakeLaunchctl(t, []call{{args: []string{"bootout", target()}, err: exitErr(t, 3)}})
	if err := Unload(context.Background()); err != nil {
		t.Fatalf("Unload not loaded: %v", err)
	}
	fakeLaunchctl(t, []call{{args: []string{"bootout", target()}, out: "x", err: exitErr(t, 1)}})
	if err := Unload(context.Background()); err == nil {
		t.Fatal("Unload succeeded, want error")
	}
}

func TestLoaded(t *testing.T) {
	fakeLaunchctl(t, []call{{args: []string{"print", target()}}})
	if ok, err := Loaded(context.Background()); !ok || err != nil {
		t.Fatalf("Loaded = %v, %v", ok, err)
	}
	fakeLaunchctl(t, []call{{args: []string{"print", target()}, err: exitErr(t, 113)}})
	if ok, err := Loaded(context.Background()); ok || err != nil {
		t.Fatalf("Loaded = %v, %v", ok, err)
	}
	fakeLaunchctl(t, []call{{args: []string{"print", target()}, err: errors.New("exec: not found")}})
	if _, err := Loaded(context.Background()); err == nil {
		t.Fatal("Loaded: want error when launchctl cannot run")
	}
}

// TestLoadUnload drives the real gui domain, so it only runs on request.
func TestLoadUnload(t *testing.T) {
	if os.Getenv("MAPLE_TEST_LAUNCHD") != "1" {
		t.Skip("set MAPLE_TEST_LAUNCHD=1 to touch the real launchd gui domain")
	}
	ctx := context.Background()
	// The label is fixed, so loading here would replace a real install.
	if ok, err := Loaded(ctx); err != nil || ok {
		t.Skipf("maple agent already loaded or state unknown (%v); not replacing it", err)
	}
	dir := t.TempDir()
	p := filepath.Join(dir, Label+".plist")
	if err := os.WriteFile(p, Plist("/usr/bin/true", filepath.Join(dir, "log")), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Unload(context.Background()) })
	if err := Load(ctx, p); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if ok, err := Loaded(ctx); !ok || err != nil {
		t.Fatalf("Loaded after Load = %v, %v", ok, err)
	}
	if err := Unload(ctx); err != nil {
		t.Fatalf("Unload: %v", err)
	}
	if ok, err := Loaded(ctx); ok || err != nil {
		t.Fatalf("Loaded after Unload = %v, %v", ok, err)
	}
	if err := Unload(ctx); err != nil {
		t.Fatalf("second Unload: %v", err)
	}
}
