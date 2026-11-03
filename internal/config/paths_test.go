package config

import "testing"

func TestPaths(t *testing.T) {
	p := Paths{Home: "/h"}
	cases := []struct {
		name, got, want string
	}{
		{"MapleDir", p.MapleDir(), "/h/.maple"},
		{"Config", p.Config(), "/h/.maple/config.yaml"},
		{"Applied", p.Applied(), "/h/.maple/applied.json"},
		{"Cache", p.Cache(), "/h/.maple/cache.json"},
		{"Token", p.Token(), "/h/.maple/token"},
		{"LogDir", p.LogDir(), "/h/.maple/logs"},
		{"BackupDir", p.BackupDir(), "/h/.maple/backups"},
		{"LockFile", p.LockFile(), "/h/.maple/lock"},
		{"ClaudeJSON", p.ClaudeJSON(), "/h/.claude.json"},
		{"CodexTOML", p.CodexTOML(), "/h/.codex/config.toml"},
		{"CodeBuddyJSON", p.CodeBuddyJSON(), "/h/.codebuddy/mcp.json"},
		{"SkillFile", p.SkillFile(), "/h/.agents/skills/maple/SKILL.md"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s() = %q, want %q", c.name, c.got, c.want)
		}
	}
}
