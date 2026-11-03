package manage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/SkyeBeFreeman/maple/internal/config"
	"github.com/SkyeBeFreeman/maple/internal/gateway/upstream"
	"github.com/SkyeBeFreeman/maple/internal/platform/proc"
)

const testTimeout = 10 * time.Second

// TestResult is what `maple mcp test` reports.
type TestResult struct {
	Server       string
	Tools        []string
	Instructions string
}

// Test connects to a server directly, bypassing the gateway, the same way the
// gateway would, and lists its tools.
func Test(ctx context.Context, e Env, name string) (*TestResult, error) {
	c, _, err := load(e.Paths)
	if err != nil {
		return nil, err
	}
	s, err := lookup(c, name)
	if err != nil {
		return nil, err
	}
	exp, secrets, err := s.Expand(e.Keychain.Get)
	if err != nil {
		var se *config.SecretError
		if errors.As(err, &se) {
			return nil, secretErr(name, se.Name, se.Err)
		}
		return nil, fmt.Errorf("maple: %s: %w", name, err)
	}
	ctx, cancel := context.WithTimeout(ctx, testTimeout)
	defer cancel()
	spec := upstream.SpecFromConfig(name, exp, e.Paths.Home, proc.LoginShellPath(ctx, e.Shell), io.Discard)
	sess, err := upstream.Dial(ctx, spec, upstream.Hooks{})
	if err != nil {
		return nil, fmt.Errorf("maple: %s: connect: %s", name, redact(err.Error(), secrets))
	}
	defer func() { _ = sess.Close() }()
	tools, err := sess.ListTools(ctx)
	if err != nil {
		return nil, fmt.Errorf("maple: %s: tools/list: %s", name, redact(err.Error(), secrets))
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

// redact keeps secret values out of errors, which an agent running the CLI
// would otherwise read, e.g. a key embedded in a URL.
func redact(msg string, secrets []string) string {
	for _, v := range secrets {
		msg = strings.ReplaceAll(msg, v, "[redacted]")
	}
	return msg
}
