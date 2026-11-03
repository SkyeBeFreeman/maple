package config

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestAppliedMissingIsEmpty(t *testing.T) {
	a, err := LoadApplied(Paths{Home: t.TempDir()})
	if err != nil {
		t.Fatalf("LoadApplied: %v", err)
	}
	if a.Version != 1 || a.Agents == nil || len(a.Agents) != 0 {
		t.Errorf("LoadApplied missing = %+v, want version 1 and empty agents", a)
	}
}

func TestAppliedRoundTrip(t *testing.T) {
	p := Paths{Home: t.TempDir()}
	in := &Applied{Version: 1, Agents: map[string]*AgentApplied{
		"claude": {Entries: map[string]string{"maple": "sha256:aa", "serena": "sha256:bb"}},
		"codex":  {Entries: map[string]string{"maple": "sha256:cc"}},
	}}
	if err := SaveApplied(p, in); err != nil {
		t.Fatalf("SaveApplied: %v", err)
	}
	if got := mode(t, p.Applied()); got != 0o600 {
		t.Errorf("applied.json mode = %o, want 600", got)
	}
	out, err := LoadApplied(p)
	if err != nil {
		t.Fatalf("LoadApplied: %v", err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Errorf("round trip = %+v, want %+v", out, in)
	}
	raw, _ := os.ReadFile(p.Applied())
	for _, key := range []string{`"version"`, `"agents"`, `"entries"`} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("applied.json %s missing key %s", raw, key)
		}
	}
}

func TestAppliedCorrupt(t *testing.T) {
	p := Paths{Home: t.TempDir()}
	_ = os.MkdirAll(p.MapleDir(), 0o700)
	_ = os.WriteFile(p.Applied(), []byte("{"), 0o600)
	if _, err := LoadApplied(p); err == nil {
		t.Error("LoadApplied accepted corrupt JSON")
	}
}
