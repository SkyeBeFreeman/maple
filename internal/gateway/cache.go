package gateway

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SkyeBeFreeman/maple/internal/config"
	"github.com/SkyeBeFreeman/maple/internal/platform/atomicfile"
)

const cacheVersion = 1

// Cache is the persisted tool table, keyed by server name; Hash is the
// server's config hash so a config change invalidates its entry.
type Cache struct {
	Version int                     `json:"version"`
	Servers map[string]CachedServer `json:"servers"`
}

// CachedServer is one server's initialize result and tool list.
type CachedServer struct {
	Hash      string                `json:"hash"`
	Info      *mcp.InitializeResult `json:"info"`
	Tools     []*mcp.Tool           `json:"tools"`
	UpdatedAt time.Time             `json:"updated_at"`
}

func emptyCache() *Cache {
	return &Cache{Version: cacheVersion, Servers: map[string]CachedServer{}}
}

// LoadCache reads cache.json. The cache is only an optimization, so any
// problem yields an empty cache rather than an error.
func LoadCache(p config.Paths) *Cache {
	data, err := os.ReadFile(p.Cache())
	if err != nil {
		if os.IsNotExist(err) {
			slog.Default().Info("no cache yet, starting empty", "path", p.Cache())
		} else {
			slog.Default().Warn("cache unreadable, starting empty", "path", p.Cache(), "err", err)
		}
		return emptyCache()
	}
	var c Cache
	if err := json.Unmarshal(data, &c); err != nil {
		slog.Default().Warn("cache corrupt, starting empty", "path", p.Cache(), "err", err)
		return emptyCache()
	}
	if c.Version != cacheVersion {
		slog.Default().Warn("cache version mismatch, starting empty", "path", p.Cache(), "version", c.Version)
		return emptyCache()
	}
	if c.Servers == nil {
		c.Servers = map[string]CachedServer{}
	}
	return &c
}

// Save writes cache.json, skipping the write when nothing changed so an idle
// gateway does not keep touching the file.
func (c *Cache) Save(p config.Paths) error {
	c.Version = cacheVersion
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = atomicfile.WriteFileIfDifferent(p.Cache(), data, 0o600, 0o700)
	var pe *fs.PathError
	if errors.As(err, &pe) && pe.Op == "open" {
		// An unreadable cache.json (e.g. left root-owned by a sudo run) must
		// not stop the gateway from persisting tools: replacing it via rename
		// only needs write access to the directory, so rewrite it.
		if err = os.MkdirAll(filepath.Dir(p.Cache()), 0o700); err == nil {
			err = atomicfile.WriteFile(p.Cache(), data, 0o600)
		}
	}
	return err
}
