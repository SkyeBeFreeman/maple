// Package config is the model shared by the CLI and the gateway: the
// config.yaml schema and its validation, secret references, and the small
// state files under ~/.maple.
package config

import "path/filepath"

// Paths derives every file location from one home directory so tests can
// point the whole program at a temporary directory.
type Paths struct {
	Home string
}

// MapleDir is ~/.maple.
func (p Paths) MapleDir() string { return filepath.Join(p.Home, ".maple") }

// Config is ~/.maple/config.yaml.
func (p Paths) Config() string { return filepath.Join(p.MapleDir(), "config.yaml") }

// Applied is ~/.maple/applied.json.
func (p Paths) Applied() string { return filepath.Join(p.MapleDir(), "applied.json") }

// Cache is ~/.maple/cache.json.
func (p Paths) Cache() string { return filepath.Join(p.MapleDir(), "cache.json") }

// Token is ~/.maple/token.
func (p Paths) Token() string { return filepath.Join(p.MapleDir(), "token") }

// LogDir is ~/.maple/logs.
func (p Paths) LogDir() string { return filepath.Join(p.MapleDir(), "logs") }

// BackupDir is ~/.maple/backups.
func (p Paths) BackupDir() string { return filepath.Join(p.MapleDir(), "backups") }

// LockFile is ~/.maple/lock.
func (p Paths) LockFile() string { return filepath.Join(p.MapleDir(), "lock") }

// ClaudeJSON is ~/.claude.json.
func (p Paths) ClaudeJSON() string { return filepath.Join(p.Home, ".claude.json") }

// CodexTOML is ~/.codex/config.toml.
func (p Paths) CodexTOML() string { return filepath.Join(p.Home, ".codex", "config.toml") }

// CodeBuddyJSON is ~/.codebuddy/mcp.json.
func (p Paths) CodeBuddyJSON() string { return filepath.Join(p.Home, ".codebuddy", "mcp.json") }

// SkillFile is ~/.agents/skills/maple/SKILL.md.
func (p Paths) SkillFile() string {
	return filepath.Join(p.Home, ".agents", "skills", "maple", "SKILL.md")
}
