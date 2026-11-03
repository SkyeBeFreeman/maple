package manage

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/SkyeBeFreeman/maple/internal/config"
	"github.com/SkyeBeFreeman/maple/internal/manage/agentconf"
	"github.com/SkyeBeFreeman/maple/internal/platform/keychain"
)

func entryArgs(t *testing.T, e Env, a agentconf.Agent, name string) (string, bool) {
	t.Helper()
	c, ok, err := a.Canonical([]byte(readFile(t, a.Path(e.Paths))), name)
	if err != nil {
		t.Fatal(err)
	}
	return string(c), ok
}

func TestAddRemoteDoesNotTouchAgents(t *testing.T) {
	e := setup(t, "")
	before := snapshot(t, e)
	res, err := Add(context.Background(), e, "remote", &config.Server{URL: "https://example.com/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Agents) != 0 {
		t.Errorf("Agents = %+v, want none", res.Agents)
	}
	if _, ok := loadConfig(t, e).Servers["remote"]; !ok {
		t.Error("remote not in config")
	}
	for f, want := range before {
		if got := readFile(t, f); got != want {
			t.Errorf("%s changed", f)
		}
	}
	if _, err := os.Stat(e.Paths.Applied()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("applied.json written: %v", err)
	}
}

func TestAddDirectWritesAgents(t *testing.T) {
	e := setup(t, "")
	ctx := context.Background()
	if _, err := Add(ctx, e, "dir", &config.Server{Command: "npx", Mode: config.ModeDirect}); err != nil {
		t.Fatal(err)
	}
	for _, a := range agentconf.All() {
		c, ok := entryArgs(t, e, a, "dir")
		if !ok {
			t.Fatalf("%s: no dir entry", a.ID())
		}
		if !strings.Contains(c, testBin) || !strings.Contains(c, `"run"`) || !strings.Contains(c, `"dir"`) {
			t.Errorf("%s: entry = %s", a.ID(), c)
		}
		if _, ok := entryArgs(t, e, a, agentconf.GatewayName); !ok {
			t.Errorf("%s: no gateway entry", a.ID())
		}
	}
	applied, err := config.LoadApplied(e.Paths)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := applied.Agents["claude"].Entries["dir"]; !ok {
		t.Errorf("applied = %+v", applied.Agents["claude"])
	}

	// Disabling removes the entry; enabling brings it back.
	if _, err := SetEnabled(ctx, e, "dir", false); err != nil {
		t.Fatal(err)
	}
	if _, ok := entryArgs(t, e, agentconf.Claude, "dir"); ok {
		t.Error("disabled server still in claude")
	}
	if _, err := SetEnabled(ctx, e, "dir", true); err != nil {
		t.Fatal(err)
	}
	if _, ok := entryArgs(t, e, agentconf.Claude, "dir"); !ok {
		t.Error("enabled server missing from claude")
	}

	if _, err := Remove(ctx, e, "dir", false); err != nil {
		t.Fatal(err)
	}
	for _, a := range agentconf.All() {
		if _, ok := entryArgs(t, e, a, "dir"); ok {
			t.Errorf("%s: dir still present", a.ID())
		}
	}
	if _, ok := loadConfig(t, e).Servers["dir"]; ok {
		t.Error("dir still in config")
	}
}

func TestSetModeChangeWritesAgents(t *testing.T) {
	e := setup(t, "servers:\n  s:\n    command: npx\n")
	_, err := Set(context.Background(), e, "s", func(s *config.Server) error {
		s.Mode = config.ModeDirect
		return nil
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := entryArgs(t, e, agentconf.Codex, "s"); !ok {
		t.Error("codex missing s after switching to direct")
	}
}

func TestSetArgsDoesNotTouchAgents(t *testing.T) {
	e := setup(t, "servers:\n  s:\n    command: npx\n    mode: direct\n")
	before := snapshot(t, e)
	_, err := Set(context.Background(), e, "s", func(s *config.Server) error {
		s.Args = []string{"-y", "x"}
		return nil
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	for f, want := range before {
		if readFile(t, f) != want {
			t.Errorf("%s changed", f)
		}
	}
	if got := loadConfig(t, e).Servers["s"].Args; len(got) != 2 {
		t.Errorf("Args = %v", got)
	}
}

func TestSetEditErrorWritesNothing(t *testing.T) {
	e := setup(t, "servers:\n  s:\n    command: npx\n")
	before := readFile(t, e.Paths.Config())
	boom := errors.New("boom")
	_, err := Set(context.Background(), e, "s", func(*config.Server) error { return boom }, false)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if readFile(t, e.Paths.Config()) != before {
		t.Error("config changed")
	}
}

func TestUnknownAndDuplicateServer(t *testing.T) {
	e := setup(t, "servers:\n  s:\n    command: npx\n")
	ctx := context.Background()
	if _, err := Add(ctx, e, "s", &config.Server{Command: "x"}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("Add dup err = %v", err)
	}
	if _, err := Remove(ctx, e, "nope", false); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("Remove err = %v", err)
	}
	if _, err := SetEnabled(ctx, e, "nope", true); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("SetEnabled err = %v", err)
	}
	if _, err := Add(ctx, e, "maple", &config.Server{Command: "x"}); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Errorf("Add maple err = %v", err)
	}
}

func TestAddRequiresSecrets(t *testing.T) {
	e := setup(t, "")
	before := readFile(t, e.Paths.Config())
	_, err := Add(context.Background(), e, "r", &config.Server{
		URL:     "https://example.com/mcp",
		Headers: map[string]string{"Authorization": "Bearer ${X}"},
	})
	if err == nil || !strings.Contains(err.Error(), "missing secret X; run: maple secret set X") {
		t.Fatalf("err = %v", err)
	}
	if readFile(t, e.Paths.Config()) != before {
		t.Error("config changed")
	}

	if err := e.Keychain.Set("X", "v"); err != nil {
		t.Fatal(err)
	}
	if _, err := Add(context.Background(), e, "r", &config.Server{
		URL:     "https://example.com/mcp",
		Headers: map[string]string{"Authorization": "Bearer ${X}"},
	}); err != nil {
		t.Fatal(err)
	}
}

// noReadStore fails on Get: checking that secrets exist must not read their
// values, which can make the real keychain prompt the user.
type noReadStore struct{ keychain.Store }

func (noReadStore) Get(string) (string, error) { return "", errors.New("value read") }

func TestAddChecksSecretsWithoutReadingThem(t *testing.T) {
	e := setup(t, "")
	mem := keychain.NewMemory()
	if err := mem.Set("X", "v"); err != nil {
		t.Fatal(err)
	}
	e.Keychain = noReadStore{mem}
	if _, err := Add(context.Background(), e, "r", &config.Server{URL: "https://x/${X}"}); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidConfigRefusesWrite(t *testing.T) {
	e := setup(t, "servers:\n  bad:\n    url: https://x\n    mode: shared\n")
	before := readFile(t, e.Paths.Config())
	_, err := Add(context.Background(), e, "r", &config.Server{URL: "https://example.com/mcp"})
	var ve *config.ValidationError
	if !errors.As(err, &ve) || ve.Line != 5 || !strings.Contains(err.Error(), "config.yaml:5") {
		t.Fatalf("err = %v, want validation error at line 5", err)
	}
	if readFile(t, e.Paths.Config()) != before {
		t.Error("config changed")
	}
}

func TestMissingConfigOrToken(t *testing.T) {
	e := setup(t, "")
	if err := os.Remove(e.Paths.Token()); err != nil {
		t.Fatal(err)
	}
	_, err := Add(context.Background(), e, "d", &config.Server{Command: "npx", Mode: config.ModeDirect})
	if err == nil || !strings.Contains(err.Error(), "maple install") {
		t.Errorf("no token err = %v", err)
	}
	if err := os.Remove(e.Paths.Config()); err != nil {
		t.Fatal(err)
	}
	_, err = Add(context.Background(), e, "r", &config.Server{URL: "https://x"})
	if err == nil || !strings.Contains(err.Error(), "maple install") || strings.Contains(err.Error(), "invalid") {
		t.Errorf("no config err = %v", err)
	}
}

func TestReloadConfirmation(t *testing.T) {
	t.Run("gateway down", func(t *testing.T) {
		e := setup(t, "")
		res, err := Add(context.Background(), e, "r", &config.Server{URL: "https://x"})
		if err != nil {
			t.Fatal(err)
		}
		if res.Reloaded || res.Note != "gateway is not running; run: maple doctor" {
			t.Errorf("res = %+v", res)
		}
	})
	t.Run("reloaded", func(t *testing.T) {
		e := setup(t, "")
		fakeGateway(t, e, func() config.Status {
			return config.Status{ConfigHash: currentHash(t, e)}
		})
		res, err := Add(context.Background(), e, "r", &config.Server{URL: "https://x"})
		if err != nil {
			t.Fatal(err)
		}
		if !res.Reloaded || res.Note != "" {
			t.Errorf("res = %+v", res)
		}
	})
	t.Run("stale", func(t *testing.T) {
		e := setup(t, "")
		hits := fakeGateway(t, e, func() config.Status { return config.Status{ConfigHash: "sha256:old"} })
		res, err := Add(context.Background(), e, "r", &config.Server{URL: "https://x"})
		if err != nil {
			t.Fatal(err)
		}
		if res.Reloaded || res.Note != "gateway has not reloaded the new config yet; run: maple status" {
			t.Errorf("res = %+v", res)
		}
		if hits.Load() < 2 {
			t.Errorf("polled %d times, want several", hits.Load())
		}
	})
	t.Run("unauthorized", func(t *testing.T) {
		e := setup(t, "")
		fakeGateway(t, e, func() config.Status { return config.Status{} })
		writeFile(t, e.Paths.Token(), "other\n")
		res, err := Add(context.Background(), e, "r", &config.Server{URL: "https://x"})
		if err != nil {
			t.Fatal(err)
		}
		if res.Reloaded || !strings.Contains(res.Note, "401") {
			t.Errorf("res = %+v", res)
		}
	})
	t.Run("reload error", func(t *testing.T) {
		e := setup(t, "")
		fakeGateway(t, e, func() config.Status {
			return config.Status{ConfigHash: "sha256:old", ReloadError: "config.yaml:3: boom"}
		})
		res, err := Add(context.Background(), e, "r", &config.Server{URL: "https://x"})
		if err != nil {
			t.Fatal(err)
		}
		if res.Reloaded || res.Note != "config.yaml:3: boom" {
			t.Errorf("res = %+v", res)
		}
	})
}
