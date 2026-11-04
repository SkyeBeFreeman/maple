package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SkyeBeFreeman/maple/internal/config"
	"github.com/SkyeBeFreeman/maple/internal/gateway/pool"
	"github.com/SkyeBeFreeman/maple/internal/gateway/upstream"
	"github.com/SkyeBeFreeman/maple/internal/platform/keychain"
)

func unknownTool(name string) error {
	return &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: fmt.Sprintf("unknown tool %q", name)}
}

// toolHandler forwards calls of one exposed tool. The route is fixed per
// registration: a rebuild that changes it registers a new handler.
func (g *Gateway) toolHandler(server, tool string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		g.mu.Lock()
		s := g.cfg.Servers[server]
		g.mu.Unlock()
		if !served(s) {
			// Removed by a reload the agent has not caught up with yet.
			return nil, unknownTool(req.Params.Name)
		}
		params := &mcp.CallToolParams{Name: tool, Meta: maps.Clone(req.Params.Meta)}
		if len(req.Params.Arguments) > 0 {
			params.Arguments = req.Params.Arguments
		}
		if tok := req.Params.GetProgressToken(); tok != nil {
			ours, done := g.trackProgress(ctx, req.Session, tok)
			defer done()
			params.SetProgressToken(ours)
		}
		sid := ""
		if req.Session != nil {
			sid = req.Session.ID()
		}
		res, err := g.pool.Call(ctx, server, s.EffectiveMode(), sid, params)
		if err == nil {
			return res, nil
		}
		var je *jsonrpc.Error
		if errors.As(err, &je) {
			return nil, je
		}
		if ctx.Err() != nil {
			// The agent cancelled; nobody reads the answer.
			return nil, ctx.Err()
		}
		msg := err.Error()
		var pe *pool.Error
		if !errors.As(err, &pe) {
			msg = "maple: " + server + ": " + msg
		}
		msg = g.red.Redact(msg)
		// Arguments are never logged: they may carry anything.
		g.log.Warn("tool call failed", "server", server, "tool", tool, "err", msg)
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: msg}}}, nil
	}
}

// dial is the pool's Dialer: it expands the server's secrets from the
// Keychain and connects, turning failures into advice for the model.
func (g *Gateway) dial(ctx context.Context, name string) (upstream.Session, error) {
	g.mu.Lock()
	s := g.cfg.Servers[name]
	g.mu.Unlock()
	if s == nil {
		return nil, &pool.Error{Server: name, Problem: "not in config.yaml", Fix: "run: maple mcp list"}
	}
	exp, secrets, err := s.Expand(g.kc.Get)
	if err != nil {
		return nil, secretError(name, err)
	}
	g.red.Add(secrets...)
	var stderr io.Writer
	if s.IsStdio() {
		stderr = g.serverLog(name)
	}
	spec := upstream.SpecFromConfig(name, exp, g.paths.Home, g.pathEnv, stderr)
	sess, err := upstream.Dial(ctx, spec, g.hooks(name))
	if err != nil {
		return nil, g.dialError(name, s, err)
	}
	return sess, nil
}

func secretError(server string, err error) error {
	var se *config.SecretError
	if !errors.As(err, &se) {
		return &pool.Error{Server: server, Problem: err.Error()}
	}
	switch {
	case errors.Is(err, keychain.ErrNotFound):
		return &pool.Error{Server: server, Problem: "missing secret " + se.Name, Fix: "run: maple secret set " + se.Name}
	case errors.Is(err, keychain.ErrDenied):
		return &pool.Error{Server: server, Problem: "cannot read secret " + se.Name + " from the Keychain", Fix: "run: maple doctor"}
	}
	return &pool.Error{Server: server, Problem: "cannot read secret " + se.Name + ": " + se.Err.Error(), Fix: "run: maple doctor"}
}

// dialError explains a failed start. Messages name the configured URL, not
// the expanded one, which may embed a secret.
func (g *Gateway) dialError(name string, s *config.Server, err error) error {
	msg := g.red.Redact(err.Error())
	if s.IsStdio() {
		return &pool.Error{Server: name, Problem: msg}
	}
	// The SDK reports HTTP failures only as status text, never typed.
	if strings.Contains(msg, http.StatusText(http.StatusUnauthorized)) || strings.Contains(msg, http.StatusText(http.StatusForbidden)) {
		fix := "check the server's credentials"
		if refs := s.SecretRefs(); len(refs) > 0 {
			fix = "check the secrets it uses: ${" + strings.Join(refs, "}, ${") + "}; run: maple secret set " + refs[0]
		}
		return &pool.Error{Server: name, Problem: "authentication failed", Fix: fix}
	}
	// The SDK nests the network error under several layers of its own
	// wording; the innermost url.Error is what tells the user something.
	reason := msg
	var ue *url.Error
	if errors.As(err, &ue) {
		reason = g.red.Redact(ue.Err.Error())
	}
	return &pool.Error{Server: name, Problem: "cannot reach " + s.URL + ": " + reason}
}

// hooks wire upstream notifications back into the gateway. They must not
// block, since closing a session waits for them.
func (g *Gateway) hooks(name string) upstream.Hooks {
	return upstream.Hooks{
		OnToolsChanged: func() { go g.toolsChanged(name) },
		OnProgress:     g.forwardProgress,
		OnLog: func(p *mcp.LoggingMessageParams) { //nolint:staticcheck // see upstream.Hooks.OnLog
			g.log.Info("upstream log", "server", name, "level", string(p.Level), "logger", p.Logger, "data", fmt.Sprint(p.Data))
		},
	}
}
