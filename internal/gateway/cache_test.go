package gateway

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SkyeBeFreeman/maple/internal/config"
)

func TestCacheRoundTrip(t *testing.T) {
	p := config.Paths{Home: t.TempDir()}
	c := LoadCache(p)
	if c.Version != 1 || c.Servers == nil || len(c.Servers) != 0 {
		t.Fatalf("empty cache = %+v", c)
	}
	now := time.Date(2026, 11, 4, 1, 2, 3, 0, time.UTC)
	c.Servers["demo"] = CachedServer{
		Hash:      "sha256:abc",
		Info:      &mcp.InitializeResult{ServerInfo: &mcp.Implementation{Name: "demo", Version: "1"}, Instructions: "hi"},
		Tools:     []*mcp.Tool{{Name: "x", Description: "d", InputSchema: map[string]any{"type": "object"}}},
		UpdatedAt: now,
	}
	if err := c.Save(p); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p.Cache())
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("stat %v %v", st, err)
	}
	if st, _ := os.Stat(filepath.Dir(p.Cache())); st.Mode().Perm() != 0o700 {
		t.Fatalf("dir perm %v", st.Mode())
	}
	got := LoadCache(p)
	s := got.Servers["demo"]
	if s.Hash != "sha256:abc" || s.Info.ServerInfo.Name != "demo" || s.Info.Instructions != "hi" ||
		len(s.Tools) != 1 || s.Tools[0].Name != "x" || !s.UpdatedAt.Equal(now) {
		t.Fatalf("round trip = %+v", s)
	}

	// Unchanged content must not rewrite the file.
	old := time.Unix(1000, 0)
	if err := os.Chtimes(p.Cache(), old, old); err != nil {
		t.Fatal(err)
	}
	if err := got.Save(p); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(p.Cache()); !st.ModTime().Equal(old) {
		t.Fatal("unchanged cache was rewritten")
	}
}

func TestCacheSaveSetsVersion(t *testing.T) {
	p := config.Paths{Home: t.TempDir()}
	if err := (&Cache{Servers: map[string]CachedServer{"a": {Hash: "h"}}}).Save(p); err != nil {
		t.Fatal(err)
	}
	if got := LoadCache(p); got.Servers["a"].Hash != "h" {
		t.Fatalf("cache = %+v", got)
	}
}

func TestCacheCorruptIsEmpty(t *testing.T) {
	for name, data := range map[string]string{
		"garbage": "{not json",
		"version": `{"version":2,"servers":{"a":{"hash":"h"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			p := config.Paths{Home: t.TempDir()}
			if err := os.MkdirAll(filepath.Dir(p.Cache()), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p.Cache(), []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			c := LoadCache(p)
			if c.Version != 1 || c.Servers == nil || len(c.Servers) != 0 {
				t.Fatalf("cache = %+v", c)
			}
		})
	}
}
