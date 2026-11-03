package manage

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/SkyeBeFreeman/maple/internal/config"
	"github.com/SkyeBeFreeman/maple/internal/manage/agentconf"
)

func TestSyncWritesGatewayAndDirect(t *testing.T) {
	e := setup(t, "servers:\n  d:\n    command: npx\n    mode: direct\n  s:\n    command: npx\n")
	e.Agents = []agentconf.Agent{agentconf.Claude}
	res, err := Sync(context.Background(), e, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Agent != "claude" {
		t.Fatalf("res = %+v", res)
	}
	if _, ok := entryArgs(t, e, agentconf.Claude, "d"); !ok {
		t.Error("claude missing d")
	}
	if _, ok := entryArgs(t, e, agentconf.Claude, "s"); ok {
		t.Error("shared server written to claude")
	}
	c, ok := entryArgs(t, e, agentconf.Claude, agentconf.GatewayName)
	if !ok || !contains(c, "Bearer "+testToken) {
		t.Errorf("gateway entry = %s", c)
	}
	if _, ok := entryArgs(t, e, agentconf.Codex, "d"); ok {
		t.Error("codex written although not selected")
	}
}

func TestSyncNote(t *testing.T) {
	t.Run("gateway down", func(t *testing.T) {
		e := setup(t, "")
		port := loadConfig(t, e).Port
		note := SyncNote(context.Background(), e)
		if !strings.Contains(note, "port "+strconv.Itoa(port)) || !strings.Contains(note, "restarts") {
			t.Errorf("note = %q", note)
		}
	})
	t.Run("gateway up", func(t *testing.T) {
		e := setup(t, "")
		fakeGateway(t, e, func() config.Status { return config.Status{} })
		if note := SyncNote(context.Background(), e); note != "" {
			t.Errorf("note = %q, want none", note)
		}
	})
}
