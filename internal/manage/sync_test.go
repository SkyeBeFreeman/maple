package manage

import (
	"context"
	"testing"

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
