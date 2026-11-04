package agentconf

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/SkyeBeFreeman/maple/internal/config"
	"github.com/SkyeBeFreeman/maple/internal/testutil"
)

const testBin = "/opt/maple/bin/maple"

// setupHome copies the sample agent files into a temporary HOME.
func setupHome(t *testing.T) config.Paths {
	t.Helper()
	return testutil.AgentHome(t, "testdata")
}

func emptyApplied() *config.Applied {
	return &config.Applied{Version: 1, Agents: map[string]*config.AgentApplied{}}
}

func testConfig() *config.Config {
	off := false
	return &config.Config{Port: 47321, Servers: map[string]*config.Server{
		"serena": {Command: "uvx", Mode: config.ModeDirect},
		"alpha":  {Command: "npx", Mode: config.ModeDirect},
		"off":    {Command: "npx", Mode: config.ModeDirect, Enabled: &off},
		"shared": {Command: "npx"},
		"remote": {URL: "https://example.com/mcp"},
	}}
}

func canon(t *testing.T, p config.Paths, a Agent, name string) ([]byte, bool) {
	t.Helper()
	data, err := os.ReadFile(a.Path(p))
	if err != nil {
		t.Fatal(err)
	}
	c, ok, err := a.Canonical(data, name)
	if err != nil {
		t.Fatalf("%s: %v", a.ID(), err)
	}
	return c, ok
}

func mustApply(t *testing.T, p config.Paths, applied *config.Applied, desired []Entry, opts ApplyOptions) []Result {
	t.Helper()
	res, err := Apply(p, applied, desired, opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return res
}

func TestDesiredSkipsDisabledDirect(t *testing.T) {
	got := Desired(testConfig(), "tok", testBin)
	want := []Entry{
		{Name: "maple", URL: "http://127.0.0.1:47321/mcp", Headers: map[string]string{"Authorization": "Bearer tok"}},
		{Name: "alpha", Command: testBin, Args: []string{"run", "alpha"}},
		{Name: "serena", Command: testBin, Args: []string{"run", "serena"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Desired = %+v\nwant %+v", got, want)
	}
}

func TestApplyWritesAndRecords(t *testing.T) {
	p := setupHome(t)
	applied := emptyApplied()
	desired := Desired(testConfig(), "tok", testBin)
	res := mustApply(t, p, applied, desired, ApplyOptions{})
	if len(res) != 3 {
		t.Fatalf("got %d results", len(res))
	}
	for _, a := range All() {
		if _, ok := canon(t, p, a, "maple"); !ok {
			t.Errorf("%s: no maple entry", a.ID())
		}
		h := applied.Agents[a.ID()].Entries["maple"]
		if !strings.HasPrefix(h, "sha256:") {
			t.Errorf("%s: hash %q", a.ID(), h)
		}
		if _, ok := canon(t, p, a, "goland"); a.ID() != "codex" && !ok {
			t.Errorf("%s: user entry lost", a.ID())
		}
	}
	for _, r := range res {
		if !reflect.DeepEqual(r.Written, []string{"maple", "alpha", "serena"}) {
			t.Errorf("%s: Written = %v", r.Agent, r.Written)
		}
	}
	res = mustApply(t, p, applied, desired, ApplyOptions{})
	for _, r := range res {
		if len(r.Written) != 0 || len(r.Removed) != 0 || len(r.Skipped) != 0 {
			t.Errorf("%s: second apply not a no-op: %+v", r.Agent, r)
		}
	}
}

func TestApplyKeepsFileMode(t *testing.T) {
	p := setupHome(t)
	if err := os.Chmod(p.ClaudeJSON(), 0o644); err != nil {
		t.Fatal(err)
	}
	mustApply(t, p, emptyApplied(), Desired(testConfig(), "tok", testBin), ApplyOptions{Agents: []Agent{Claude}})
	fi, err := os.Stat(p.ClaudeJSON())
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %v", fi.Mode().Perm())
	}
}

func TestApplySkipsUserConflict(t *testing.T) {
	p := setupHome(t)
	applied := emptyApplied()
	before, _ := canon(t, p, Claude, "goland")
	desired := []Entry{{Name: "goland", Command: testBin, Args: []string{"run", "goland"}}}
	res := mustApply(t, p, applied, desired, ApplyOptions{Agents: []Agent{Claude}, Force: true})
	want := []Skip{{Name: "goland", Reason: "conflicts with your own entry; rename it or run maple mcp import"}}
	if !reflect.DeepEqual(res[0].Skipped, want) {
		t.Fatalf("Skipped = %+v", res[0].Skipped)
	}
	after, _ := canon(t, p, Claude, "goland")
	if string(before) != string(after) {
		t.Fatal("user entry changed")
	}
	if applied.Agents["claude"] != nil {
		t.Fatalf("nothing owned yet recorded: %+v", applied.Agents["claude"])
	}
}

func handEdit(t *testing.T, p config.Paths, a Agent, e Entry) {
	t.Helper()
	data, err := os.ReadFile(a.Path(p))
	if err != nil {
		t.Fatal(err)
	}
	out, err := a.Set(data, e)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.Path(p), out, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestApplySkipsModifiedUnlessForce(t *testing.T) {
	p := setupHome(t)
	applied := emptyApplied()
	desired := Desired(testConfig(), "tok", testBin)
	opts := ApplyOptions{Agents: []Agent{Codex}}
	mustApply(t, p, applied, desired, opts)
	edited := Entry{Name: "maple", URL: "http://127.0.0.1:1/mcp"}
	handEdit(t, p, Codex, edited)
	mine, _ := canon(t, p, Codex, "maple")

	res := mustApply(t, p, applied, desired, opts)
	want := []Skip{{Name: "maple", Reason: "modified by you; rerun with --force to overwrite"}}
	if !reflect.DeepEqual(res[0].Skipped, want) {
		t.Fatalf("Skipped = %+v", res[0].Skipped)
	}
	if got, _ := canon(t, p, Codex, "maple"); string(got) != string(mine) {
		t.Fatal("modified entry overwritten without force")
	}

	// Removal is protected the same way.
	res = mustApply(t, p, applied, desired[1:], opts)
	if !reflect.DeepEqual(res[0].Skipped, want) || len(res[0].Removed) != 0 {
		t.Fatalf("removal not protected: %+v", res[0])
	}

	opts.Force = true
	res = mustApply(t, p, applied, desired, opts)
	if !reflect.DeepEqual(res[0].Written, []string{"maple"}) {
		t.Fatalf("force: %+v", res[0])
	}
	got, _ := canon(t, p, Codex, "maple")
	if config.ContentHash(got) != applied.Agents["codex"].Entries["maple"] {
		t.Fatal("hash not updated after force")
	}
}

func TestApplyRemovesStaleDirect(t *testing.T) {
	p := setupHome(t)
	applied := emptyApplied()
	desired := Desired(testConfig(), "tok", testBin)
	mustApply(t, p, applied, desired, ApplyOptions{})
	before, _ := canon(t, p, Claude, "goland")

	var kept []Entry
	for _, e := range desired {
		if e.Name != "serena" {
			kept = append(kept, e)
		}
	}
	res := mustApply(t, p, applied, kept, ApplyOptions{})
	for _, r := range res {
		if !reflect.DeepEqual(r.Removed, []string{"serena"}) {
			t.Errorf("%s: Removed = %v", r.Agent, r.Removed)
		}
	}
	for _, a := range All() {
		if _, ok := canon(t, p, a, "serena"); ok {
			t.Errorf("%s: serena still present", a.ID())
		}
		if _, ok := applied.Agents[a.ID()].Entries["serena"]; ok {
			t.Errorf("%s: serena still recorded", a.ID())
		}
	}
	if after, _ := canon(t, p, Claude, "goland"); string(before) != string(after) {
		t.Fatal("user entry changed")
	}
}

func TestApplyMissingFile(t *testing.T) {
	p := setupHome(t)
	if err := os.Remove(p.CodexTOML()); err != nil {
		t.Fatal(err)
	}
	applied := emptyApplied()
	res := mustApply(t, p, applied, Desired(testConfig(), "tok", testBin), ApplyOptions{})
	for _, r := range res {
		if r.Agent == "codex" {
			if !r.Missing || len(r.Written) != 0 {
				t.Errorf("codex: %+v", r)
			}
			continue
		}
		if r.Missing || len(r.Written) == 0 {
			t.Errorf("%s: %+v", r.Agent, r)
		}
	}
	if _, err := os.Stat(p.CodexTOML()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("codex config created")
	}
	if _, ok := applied.Agents["codex"]; ok {
		t.Fatal("codex recorded")
	}
}

func TestApplyParseErrorIsolated(t *testing.T) {
	p := setupHome(t)
	if err := os.WriteFile(p.ClaudeJSON(), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Apply(p, emptyApplied(), Desired(testConfig(), "tok", testBin), ApplyOptions{})
	if err == nil || !strings.Contains(err.Error(), p.ClaudeJSON()) {
		t.Fatalf("err = %v", err)
	}
	if len(res) != 3 {
		t.Fatalf("results = %d", len(res))
	}
	for _, a := range []Agent{Codex, CodeBuddy} {
		if _, ok := canon(t, p, a, "maple"); !ok {
			t.Errorf("%s not written", a.ID())
		}
	}
}

func TestApplyRetriesOnConcurrentWrite(t *testing.T) {
	p := setupHome(t)
	calls := 0
	beforeWrite = func(string) {
		calls++
		if calls == 1 {
			handEdit(t, p, Claude, Entry{Name: "other", Command: "x"})
		}
	}
	t.Cleanup(func() { beforeWrite = nil })
	mustApply(t, p, emptyApplied(), Desired(testConfig(), "tok", testBin), ApplyOptions{Agents: []Agent{Claude}})
	if calls != 2 {
		t.Fatalf("writes attempted = %d", calls)
	}
	for _, n := range []string{"other", "maple", "goland"} {
		if _, ok := canon(t, p, Claude, n); !ok {
			t.Errorf("%s missing", n)
		}
	}
}

func TestApplyGivesUpAfterRetries(t *testing.T) {
	p := setupHome(t)
	n := 0
	beforeWrite = func(string) {
		n++
		handEdit(t, p, Claude, Entry{Name: "other", Command: strings.Repeat("x", n)})
	}
	t.Cleanup(func() { beforeWrite = nil })
	_, err := Apply(p, emptyApplied(), Desired(testConfig(), "tok", testBin), ApplyOptions{Agents: []Agent{Claude}})
	if err == nil || !strings.Contains(err.Error(), p.ClaudeJSON()) || n != 3 {
		t.Fatalf("err = %v, attempts = %d", err, n)
	}
}

func TestApplySymlinkedConfig(t *testing.T) {
	p := setupHome(t)
	target := filepath.Join(p.Home, "dotfiles", "mcp.json")
	testutil.CopyFile(t, p.CodeBuddyJSON(), target)
	if err := os.Remove(p.CodeBuddyJSON()); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, p.CodeBuddyJSON()); err != nil {
		t.Fatal(err)
	}
	mustApply(t, p, emptyApplied(), Desired(testConfig(), "tok", testBin), ApplyOptions{Agents: []Agent{CodeBuddy}})
	fi, err := os.Lstat(p.CodeBuddyJSON())
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink replaced")
	}
	data, _ := os.ReadFile(target)
	if _, ok, _ := CodeBuddy.Canonical(data, "maple"); !ok {
		t.Fatal("target not updated")
	}
}

func TestRemoveForeign(t *testing.T) {
	p := setupHome(t)
	if err := RemoveForeign(p, CodeBuddy, []string{"goland", "fetch"}); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"goland", "fetch"} {
		if _, ok := canon(t, p, CodeBuddy, n); ok {
			t.Errorf("%s still present", n)
		}
	}
}
