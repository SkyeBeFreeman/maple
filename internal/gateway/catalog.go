package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SkyeBeFreeman/maple/internal/config"
)

// exposed is one registered gateway tool. def is the JSON of the exposed
// definition, so a rebuild can tell an unchanged tool from a changed one.
type exposed struct {
	server, tool, def string
}

// connHash keys the cache and restart decisions. Description, DisabledTools
// and Enabled do not change how the upstream is reached, so editing them
// must neither restart it nor drop its cached tools.
func connHash(s *config.Server) string {
	c := *s
	c.Description, c.DisabledTools, c.Enabled = "", nil, nil
	return c.Hash()
}

// served reports whether the gateway exposes the server; direct servers are
// run by the agents themselves.
func served(s *config.Server) bool {
	return s != nil && s.IsEnabled() && s.Mode != config.ModeDirect
}

// servedNamesLocked lists exposed servers in a stable order, which keeps tool
// name collisions resolving the same way every time. Caller holds g.mu.
func (g *Gateway) servedNamesLocked() []string {
	var names []string
	for name, s := range g.cfg.Servers {
		if served(s) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// cachedLocked returns the cache entry of a server if it matches its current
// config. Caller holds g.mu.
func (g *Gateway) cachedLocked(name string) (CachedServer, bool) {
	s := g.cfg.Servers[name]
	cs, ok := g.cache.Servers[name]
	if !ok || s == nil || cs.Hash != connHash(s) {
		return CachedServer{}, false
	}
	return cs, true
}

func disabledSet(s *config.Server) map[string]bool {
	m := make(map[string]bool, len(s.DisabledTools))
	for _, t := range s.DisabledTools {
		m[t] = true
	}
	return m
}

// rebuildLocked brings the registered tools in line with config and cache.
// Only changed tools are touched, since every change notifies all agent
// sessions. Caller holds g.mu.
func (g *Gateway) rebuildLocked() {
	taken := map[string]bool{}
	want := map[string]exposed{}
	defs := map[string]*mcp.Tool{}
	for _, name := range g.servedNamesLocked() {
		cs, ok := g.cachedLocked(name)
		if !ok {
			continue
		}
		disabled := disabledSet(g.cfg.Servers[name])
		for _, t := range cs.Tools {
			if t == nil || disabled[t.Name] {
				continue
			}
			n := ToolName(name, t.Name, taken)
			def := *t
			def.Name = n
			def.InputSchema = objectSchema(t.InputSchema)
			b, err := json.Marshal(&def)
			if err != nil {
				g.log.Warn("skipping tool with unencodable definition", "server", name, "tool", t.Name, "err", err)
				continue
			}
			want[n] = exposed{server: name, tool: t.Name, def: string(b)}
			defs[n] = &def
		}
	}
	var gone []string
	for n := range g.tools {
		if _, ok := want[n]; !ok {
			gone = append(gone, n)
		}
	}
	if len(gone) > 0 {
		g.server.RemoveTools(gone...)
	}
	for n, e := range want {
		if old, ok := g.tools[n]; ok && old == e {
			continue
		}
		// AddTool replaces a changed tool in place, so it never disappears
		// from a concurrent tools/list.
		if err := g.addTool(defs[n], e); err != nil {
			g.log.Warn("skipping tool the MCP SDK rejects", "server", e.server, "tool", e.tool, "err", err.Error())
			if _, ok := g.tools[n]; ok {
				g.server.RemoveTools(n)
			}
			delete(want, n)
		}
	}
	g.tools = want
}

// addTool turns the SDK's panics on odd upstream definitions into an error,
// so one bad tool cannot take the gateway down.
func (g *Gateway) addTool(def *mcp.Tool, e exposed) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	g.server.AddTool(def, g.toolHandler(e.server, e.tool))
	return nil
}

// objectSchema passes an object schema through and replaces anything else,
// which the SDK would refuse and agents could not call anyway.
func objectSchema(schema any) any {
	fallback := map[string]any{"type": "object"}
	if schema == nil {
		return fallback
	}
	b, err := json.Marshal(schema)
	if err != nil {
		return fallback
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil || m["type"] != "object" {
		return fallback
	}
	return m
}

// fetchMissingLocked starts listing every served server without a matching
// cache entry and returns what to wait for. Caller holds g.mu.
func (g *Gateway) fetchMissingLocked() []chan struct{} {
	var waits []chan struct{}
	for _, name := range g.servedNamesLocked() {
		if _, ok := g.cachedLocked(name); ok {
			continue
		}
		if ch := g.fetchLocked(name); ch != nil {
			waits = append(waits, ch)
		}
	}
	return waits
}

// fetchLocked lists a server in the background unless that already runs.
// Caller holds g.mu.
func (g *Gateway) fetchLocked(name string) chan struct{} {
	if ch := g.fetching[name]; ch != nil {
		return ch
	}
	s := g.cfg.Servers[name]
	if g.closed || !served(s) {
		return nil
	}
	ch := make(chan struct{})
	g.fetching[name] = ch
	g.wg.Add(1)
	go g.fetch(name, connHash(s), s.EffectiveMode(), ch)
	return ch
}

func (g *Gateway) fetch(name, hash, mode string, ch chan struct{}) {
	defer g.wg.Done()
	// Closed last, after the catalog is rebuilt, so waiters see the tools.
	defer close(ch)
	ctx, cancel := context.WithTimeout(g.bg, g.opts.ListWait)
	info, tools, err := g.pool.List(ctx, name, mode)
	cancel()

	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.fetching, name)
	again := g.refetch[name]
	delete(g.refetch, name)
	s := g.cfg.Servers[name]
	switch {
	case s == nil || connHash(s) != hash:
		// Listed under a config that has since changed.
		again = true
	case g.closed:
	case errors.Is(err, context.DeadlineExceeded):
		g.log.Info("tools still not listed; retrying on the next request", "server", name, "wait", g.opts.ListWait.String())
	case err != nil:
		g.log.Warn("cannot list tools", "server", name, "err", err.Error())
	default:
		g.cache.Servers[name] = CachedServer{Hash: hash, Info: info, Tools: tools, UpdatedAt: time.Now()}
		if err := g.cache.Save(g.paths); err != nil {
			g.log.Warn("cannot save cache", "err", err.Error())
		}
		g.rebuildLocked()
	}
	if again {
		g.fetchLocked(name)
	}
}

// toolsChanged relists a server after its tools/list_changed. Per-session
// servers are skipped: their lists are relisted on a throwaway instance,
// which cannot see what changed in one agent's instance, and one that
// announces a change on start would be restarted forever.
func (g *Gateway) toolsChanged(name string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if s := g.cfg.Servers[name]; s == nil || s.EffectiveMode() == config.ModePerSession {
		return
	}
	if g.fetching[name] != nil {
		g.refetch[name] = true
		return
	}
	g.fetchLocked(name)
}

// waitFetches waits up to d for servers still being listed; what does not
// finish in time is simply left out of this answer.
func (g *Gateway) waitFetches(ctx context.Context, d time.Duration) {
	g.mu.Lock()
	waits := g.fetchMissingLocked()
	g.mu.Unlock()
	if len(waits) == 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	for _, ch := range waits {
		select {
		case <-ch:
		case <-t.C:
			return
		case <-ctx.Done():
			return
		}
	}
}

// middleware makes initialize and tools/list wait for listing in progress,
// and fills in the instructions, which depend on the current catalog.
func (g *Gateway) middleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		switch method {
		case "initialize":
			res, err := next(ctx, method, req)
			if r, ok := res.(*mcp.InitializeResult); ok && err == nil {
				g.waitFetches(ctx, g.opts.InitWait)
				r.Instructions = g.instructions()
			}
			return res, err
		case "tools/list":
			// Later pages continue a listing that already waited.
			if p, ok := req.GetParams().(*mcp.ListToolsParams); !ok || p == nil || p.Cursor == "" {
				g.waitFetches(ctx, g.opts.ListWait)
			}
		}
		return next(ctx, method, req)
	}
}

func (g *Gateway) instructions() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var infos []ServerInfo
	// Only servers whose tools are known: one still starting, or broken,
	// would be advertised without anything to call.
	for _, name := range g.servedNamesLocked() {
		s := g.cfg.Servers[name]
		cs, ok := g.cachedLocked(name)
		if !ok {
			continue
		}
		si := ServerInfo{Name: name, About: About(s.Description, cs.Info)}
		disabled := disabledSet(s)
		for _, t := range cs.Tools {
			if t != nil && !disabled[t.Name] {
				si.Tools = append(si.Tools, ToolInfo{Name: t.Name, Description: t.Description})
			}
		}
		if len(si.Tools) > 0 {
			infos = append(infos, si)
		}
	}
	return Instructions(infos)
}
