package manage

import (
	"context"
	"testing"

	"github.com/SkyeBeFreeman/maple/internal/config"
	"github.com/SkyeBeFreeman/maple/internal/manage/agentconf"
)

func TestDoctor(t *testing.T) {
	e := setup(t, "servers:\n  sec:\n    url: http://127.0.0.1:1/mcp\n    headers:\n      Authorization: Bearer ${MISSING_TOKEN}\n  cmd:\n    command: maple-no-such-command-xyz\n")
	ctx := context.Background()
	if _, err := Sync(ctx, e, false); err != nil {
		t.Fatal(err)
	}
	// Claude's gateway entry carries a stale token, recorded as maple's own
	// write so only the token check notices.
	path := e.Paths.ClaudeJSON()
	data, err := agentconf.Claude.Set([]byte(readFile(t, path)), agentconf.Entry{
		Name: agentconf.GatewayName, URL: "http://127.0.0.1:1/mcp", Headers: map[string]string{"Authorization": "Bearer old"},
	})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(data))
	canon, _, _ := agentconf.Claude.Canonical(data, agentconf.GatewayName)
	applied, _ := config.LoadApplied(e.Paths)
	applied.Agents["claude"].Entries[agentconf.GatewayName] = config.ContentHash(canon)
	if err := config.SaveApplied(e.Paths, applied); err != nil {
		t.Fatal(err)
	}
	fakeGateway(t, e, func() config.Status { return config.Status{Version: "test"} })

	checks := Doctor(ctx, e)
	want := []string{"launchd", "gateway", "port", "agent:claude", "agent:codex", "agent:codebuddy", "direct-entries", "applied", "secrets", "commands", "config"}
	if len(checks) != len(want) {
		t.Fatalf("checks = %+v", checks)
	}
	bad := map[string]bool{"agent:claude": true, "secrets": true, "commands": true}
	for i, c := range checks {
		if c.Name != want[i] {
			t.Errorf("check %d = %s, want %s", i, c.Name, want[i])
		}
		if c.OK == bad[c.Name] {
			t.Errorf("%s: OK = %v (%s)", c.Name, c.OK, c.Detail)
		}
		if !c.OK && c.Fix == "" {
			t.Errorf("%s: no Fix", c.Name)
		}
	}
	if checks[0].Detail != "skipped" {
		t.Errorf("launchd detail = %q", checks[0].Detail)
	}
}
