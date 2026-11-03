package config

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/SkyeBeFreeman/maple/internal/platform/atomicfile"
)

var lockTimeout = 10 * time.Second

// Load reads and parses config.yaml. The raw bytes are returned even when
// parsing fails, so the gateway can report the hash of what it saw. A missing
// file yields an error matching fs.ErrNotExist.
func Load(p Paths) (*Config, []byte, error) {
	data, err := os.ReadFile(p.Config())
	if err != nil {
		return nil, nil, err
	}
	c, err := Parse(data)
	if err != nil {
		return nil, data, err
	}
	return c, data, nil
}

// Save validates c and atomically writes config.yaml. Hand-written comments
// are not preserved.
func Save(p Paths, c *Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(c); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := ensureDir(p); err != nil {
		return err
	}
	return atomicfile.WriteFile(p.Config(), buf.Bytes(), 0o600)
}

// Lock serializes writers of ~/.maple across the CLI and the gateway. It waits
// at most 10s (or until ctx ends) and then fails with *atomicfile.LockedError.
func Lock(ctx context.Context, p Paths) (unlock func() error, err error) {
	if err := ensureDir(p); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, lockTimeout)
	defer cancel()
	return atomicfile.Lock(ctx, p.LockFile())
}

func ensureDir(p Paths) error {
	return os.MkdirAll(p.MapleDir(), 0o700)
}
