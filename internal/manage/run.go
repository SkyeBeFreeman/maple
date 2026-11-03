package manage

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"syscall"

	"github.com/SkyeBeFreeman/maple/internal/config"
	"github.com/SkyeBeFreeman/maple/internal/platform/proc"
)

// execFn replaces the process; tests swap it to capture the call.
var execFn = syscall.Exec

// Run replaces maple with the direct server name, so the agent talks to the
// server itself over stdio. It must not write to stdout: that is the agent's
// MCP channel. On success it does not return.
func Run(ctx context.Context, e Env, name string) error {
	c, _, err := load(e.Paths)
	if err != nil {
		return err
	}
	s, ok := c.Servers[name]
	switch {
	case !ok || s == nil:
		return fmt.Errorf("maple: %s: server not found; run: maple mcp list", name)
	case !s.IsEnabled():
		return fmt.Errorf("maple: %s: server is disabled; run: maple mcp enable %s", name, name)
	case !s.IsStdio() || s.EffectiveMode() != config.ModeDirect:
		return fmt.Errorf("maple: %s: server is not in direct mode; it is served by the maple gateway", name)
	}
	exp, _, err := s.Expand(e.Keychain.Get)
	if err != nil {
		var se *config.SecretError
		if errors.As(err, &se) {
			return secretErr(name, se.Name, se.Err)
		}
		return fmt.Errorf("maple: %s: %w", name, err)
	}
	pathEnv := proc.LoginShellPath(ctx, e.Shell)
	path, err := proc.LookPath(exp.Command, pathEnv)
	if err != nil {
		return fmt.Errorf("maple: %s: %w; install it or set an absolute command path", name, err)
	}
	env := mergeEnv(os.Environ(), map[string]string{"PATH": pathEnv})
	env = mergeEnv(env, exp.Env)
	if err := execFn(path, append([]string{path}, exp.Args...), env); err != nil {
		return fmt.Errorf("maple: %s: exec %s: %w", name, path, err)
	}
	return nil
}

// mergeEnv overrides keys of base with set. Duplicates are dropped rather
// than appended because getenv in the child returns the first match.
func mergeEnv(base []string, set map[string]string) []string {
	out := make([]string, 0, len(base)+len(set))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if _, ok := set[k]; !ok {
			out = append(out, kv)
		}
	}
	for _, k := range slices.Sorted(maps.Keys(set)) {
		out = append(out, k+"="+set[k])
	}
	return out
}
