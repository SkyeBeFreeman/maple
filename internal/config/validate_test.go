package config

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateRules(t *testing.T) {
	cases := []struct {
		name     string
		yaml     string
		line     int
		server   string
		msgMatch string
	}{
		{
			name: "dot in name",
			yaml: "servers:\n  a.b:\n    url: https://x\n",
			line: 2, server: "a.b", msgMatch: "name",
		},
		{
			name: "33 chars",
			yaml: "servers:\n  " + strings.Repeat("a", 33) + ":\n    url: https://x\n",
			line: 2, server: strings.Repeat("a", 33), msgMatch: "name",
		},
		{
			name: "empty name",
			yaml: "servers:\n  \"\":\n    url: https://x\n",
			line: 2, server: "", msgMatch: "name",
		},
		{
			name: "url and command",
			yaml: "servers:\n  ok:\n    url: https://x\n    command: npx\n",
			line: 2, server: "ok", msgMatch: "url",
		},
		{
			name: "neither url nor command",
			yaml: "servers:\n  ok:\n    description: hi\n",
			line: 2, server: "ok", msgMatch: "url",
		},
		{
			name: "null server",
			yaml: "servers:\n  ok:\n",
			line: 2, server: "ok", msgMatch: "url",
		},
		{
			name: "transport ws",
			yaml: "servers:\n  ok:\n    url: https://x\n    transport: ws\n",
			line: 4, server: "ok", msgMatch: "transport",
		},
		{
			name: "transport on stdio",
			yaml: "servers:\n  ok:\n    command: npx\n    transport: sse\n",
			line: 4, server: "ok", msgMatch: "transport",
		},
		{
			name: "mode on remote",
			yaml: "servers:\n  ok:\n    url: https://x\n    mode: shared\n",
			line: 4, server: "ok", msgMatch: "mode",
		},
		{
			name: "bad mode",
			yaml: "servers:\n  ok:\n    command: npx\n    mode: pooled\n",
			line: 4, server: "ok", msgMatch: "mode",
		},
		{
			name: "direct with disabled_tools",
			yaml: "servers:\n  ok:\n    command: npx\n    mode: direct\n    disabled_tools: [x]\n",
			line: 5, server: "ok", msgMatch: "disabled_tools",
		},
		{
			name: "lowercase ref in header",
			yaml: "servers:\n  ok:\n    url: https://x\n    headers:\n      Authorization: Bearer ${lower}\n",
			line: 5, server: "ok", msgMatch: "lower",
		},
		{
			name: "bad ref in url",
			yaml: "servers:\n  ok:\n    url: https://x/${a}\n",
			line: 3, server: "ok", msgMatch: "${a}",
		},
		{
			name: "bad ref in args",
			yaml: "servers:\n  ok:\n    command: npx\n    args:\n      - fine\n      - ${1X}\n",
			line: 6, server: "ok", msgMatch: "1X",
		},
		{
			name: "unterminated ref in env",
			yaml: "servers:\n  ok:\n    command: npx\n    env:\n      K: ${OPEN\n",
			line: 5, server: "ok", msgMatch: "${",
		},
		{
			name: "reserved name",
			yaml: "servers:\n  maple:\n    url: https://x\n",
			line: 2, server: "maple", msgMatch: `server name "maple" is reserved`,
		},
		{
			name: "port 0",
			yaml: "port: 0\nservers: {}\n",
			line: 1, server: "", msgMatch: "port",
		},
		{
			name: "port too large",
			yaml: "servers: {}\nport: 65536\n",
			line: 2, server: "", msgMatch: "port",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse([]byte(c.yaml))
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("Parse err = %v, want *ValidationError", err)
			}
			if ve.Line != c.line {
				t.Errorf("Line = %d, want %d (%v)", ve.Line, c.line, ve)
			}
			if ve.Server != c.server {
				t.Errorf("Server = %q, want %q", ve.Server, c.server)
			}
			if !strings.Contains(ve.Msg, c.msgMatch) {
				t.Errorf("Msg = %q, want it to mention %q", ve.Msg, c.msgMatch)
			}
		})
	}
}

func TestValidateAccepts(t *testing.T) {
	ok := []string{
		"servers:\n  A_b-9:\n    url: https://x\n    transport: sse\n",
		"servers:\n  " + strings.Repeat("a", 32) + ":\n    command: npx\n    mode: per-session\n",
		"servers:\n  d:\n    command: npx\n    mode: direct\n    env: {K: \"${K}\"}\n",
		"servers:\n  c:\n    command: sh\n    args: [-c, 'echo $HOME costs $5']\n",
		"port: 65535\n",
		"port: 1\n",
	}
	for _, in := range ok {
		if _, err := Parse([]byte(in)); err != nil {
			t.Errorf("Parse(%q): %v", in, err)
		}
	}
}

func TestValidationErrorFormat(t *testing.T) {
	e := &ValidationError{Line: 12, Server: "x", Msg: "boom"}
	if got, want := e.Error(), `config.yaml:12: server "x": boom`; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	e = &ValidationError{Line: 1, Msg: "port must be 1-65535"}
	if got, want := e.Error(), `config.yaml:1: port must be 1-65535`; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// A Config built in code (CLI path) has no YAML nodes; Validate must still
// reject it, just without a line.
func TestValidateWithoutNodes(t *testing.T) {
	c := &Config{Port: DefaultPort, Servers: map[string]*Server{"x": {URL: "u", Command: "c"}}}
	var ve *ValidationError
	if err := c.Validate(); !errors.As(err, &ve) || ve.Server != "x" {
		t.Fatalf("Validate err = %v, want ValidationError for x", err)
	}
	c = &Config{Port: DefaultPort, Servers: map[string]*Server{"x": {URL: "u"}}}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestIsServerName(t *testing.T) {
	for name, want := range map[string]bool{"ok_1-x": true, "a.b": false, "maple": false, "": false} {
		if got := IsServerName(name); got != want {
			t.Errorf("IsServerName(%q) = %v", name, got)
		}
	}
}
