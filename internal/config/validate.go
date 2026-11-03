package config

import (
	"fmt"
	"regexp"
	"sort"
)

// ValidationError is a problem in config.yaml. Line is 0 when the Config was
// built in code rather than parsed.
type ValidationError struct {
	Line   int
	Server string
	Msg    string
}

func (e *ValidationError) Error() string {
	loc := "config.yaml"
	if e.Line > 0 {
		loc = fmt.Sprintf("config.yaml:%d", e.Line)
	}
	if e.Server != "" {
		return fmt.Sprintf("%s: server %q: %s", loc, e.Server, e.Msg)
	}
	return loc + ": " + e.Msg
}

// 32 leaves room for "<server>__<tool>" within the 64-character tool name
// limit.
var serverNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// reservedName is the gateway's own entry in the agents' configs; a direct
// server with this name would collide with it.
const reservedName = "maple"

// IsServerName reports whether name is allowed as a server name.
func IsServerName(name string) bool { return serverNameRe.MatchString(name) && name != reservedName }

// Validate checks the rules of spec 6.2 and returns the first violation as a
// *ValidationError.
func (c *Config) Validate() error { return validate(c, nil) }

func validate(c *Config, idx *nodeIndex) error {
	if c.Port < 1 || c.Port > 65535 {
		return &ValidationError{Line: idx.portLine(), Msg: fmt.Sprintf("port %d: must be 1-65535", c.Port)}
	}
	names := make([]string, 0, len(c.Servers))
	for n := range c.Servers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := validateServer(name, c.Servers[name], idx); err != nil {
			return err
		}
	}
	return nil
}

func validateServer(name string, s *Server, idx *nodeIndex) error {
	fail := func(line int, format string, a ...any) error {
		return &ValidationError{Line: line, Server: name, Msg: fmt.Sprintf(format, a...)}
	}
	if !serverNameRe.MatchString(name) {
		return fail(idx.serverLine(name), "invalid server name: use 1-32 characters from A-Z a-z 0-9 _ -")
	}
	if name == reservedName {
		return fail(idx.serverLine(name), "server name %q is reserved; choose another name", reservedName)
	}
	if s == nil {
		s = &Server{}
	}
	switch {
	case s.URL != "" && s.Command != "":
		return fail(idx.serverLine(name), "set either url or command, not both")
	case s.URL == "" && s.Command == "":
		return fail(idx.serverLine(name), "set url (remote) or command (stdio)")
	}

	if s.IsStdio() {
		if s.Transport != "" {
			return fail(idx.fieldLine(name, "transport"), "transport only applies to url servers")
		}
		switch s.Mode {
		case "", ModeShared, ModePerSession, ModeDirect:
		default:
			return fail(idx.fieldLine(name, "mode"), "mode %q: must be shared, per-session or direct", s.Mode)
		}
	} else {
		if s.Mode != "" {
			return fail(idx.fieldLine(name, "mode"), "mode only applies to command (stdio) servers")
		}
		switch s.Transport {
		case "", TransportHTTP, TransportSSE:
		default:
			return fail(idx.fieldLine(name, "transport"), "transport %q: must be http or sse", s.Transport)
		}
	}
	if s.Mode == ModeDirect && len(s.DisabledTools) > 0 {
		return fail(idx.fieldLine(name, "disabled_tools"), "disabled_tools is not supported with mode direct")
	}

	if _, err := Refs(s.URL); err != nil {
		return fail(idx.fieldLine(name, "url"), "url: %v", err)
	}
	for i, a := range s.Args {
		if _, err := Refs(a); err != nil {
			return fail(idx.itemLine(name, "args", i), "args[%d]: %v", i, err)
		}
	}
	for _, k := range sortedKeys(s.Headers) {
		if _, err := Refs(s.Headers[k]); err != nil {
			return fail(idx.mapEntryLine(name, "headers", k), "headers.%s: %v", k, err)
		}
	}
	for _, k := range sortedKeys(s.Env) {
		if _, err := Refs(s.Env[k]); err != nil {
			return fail(idx.mapEntryLine(name, "env", k), "env.%s: %v", k, err)
		}
	}
	return nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
