package manage

import (
	"context"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/SkyeBeFreeman/maple/internal/gateway"
	"github.com/SkyeBeFreeman/maple/internal/gateway/upstream"
	"github.com/SkyeBeFreeman/maple/internal/platform/proc"
)

const testTimeout = 10 * time.Second

// TestResult is what `maple mcp test` reports.
type TestResult struct {
	Server       string   `json:"server"`
	Tools        []string `json:"tools"`
	Instructions string   `json:"instructions,omitempty"`
}

// Test connects to a server directly, bypassing the gateway, the same way the
// gateway would, and lists its tools.
func Test(ctx context.Context, e Env, name string) (*TestResult, error) {
	c, err := LoadConfig(e.Paths)
	if err != nil {
		return nil, err
	}
	s, err := Lookup(c, name)
	if err != nil {
		return nil, err
	}
	exp, secrets, err := expandServer(e, name, s)
	if err != nil {
		return nil, err
	}
	// Only stdio servers need it, and it starts the login shell, which must
	// not eat into the connect timeout.
	var pathEnv string
	if s.IsStdio() {
		pathEnv = proc.LoginShellPath(ctx, e.Shell)
	}
	// Errors reach an agent running the CLI, so a key embedded in a URL
	// must not leak through them.
	red := gateway.NewRedactor(secrets)
	ctx, cancel := context.WithTimeout(ctx, testTimeout)
	defer cancel()
	spec := upstream.SpecFromConfig(name, exp, e.Paths.Home, pathEnv, io.Discard)
	sess, err := upstream.Dial(ctx, spec, upstream.Hooks{})
	if err != nil {
		return nil, fmt.Errorf("maple: %s: connect: %s", name, red.Redact(err.Error()))
	}
	defer func() { _ = sess.Close() }()
	tools, err := sess.ListTools(ctx)
	if err != nil {
		return nil, fmt.Errorf("maple: %s: tools/list: %s", name, red.Redact(err.Error()))
	}
	res := &TestResult{Server: name}
	if info := sess.Info(); info != nil {
		res.Instructions = info.Instructions
	}
	for _, t := range tools {
		res.Tools = append(res.Tools, t.Name)
	}
	sort.Strings(res.Tools)
	return res, nil
}
