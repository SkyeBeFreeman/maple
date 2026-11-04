package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SkyeBeFreeman/maple/internal/config"
)

// exposed is one registered gateway tool. def is the JSON of the exposed
// definition, so a rebuild can tell an unchanged tool from a changed one.
type exposed struct {
	server, tool, def string
}

// served reports whether the gateway exposes the server; direct servers are
// run by the agents themselves.
func served(s *config.Server) bool {
	return s != nil && s.IsEnabled() && !s.IsDirect()
}

// setConfigLocked installs cfg together with what is derived from it, so
// the request path does not rehash every server. Caller holds g.mu.
func (g *Gateway) setConfigLocked(cfg *config.Config) {
	g.cfg = cfg
	g.connHashes = make(map[string]string, len(cfg.Servers))
	g.served = nil
	// Sorted, which keeps tool name collisions resolving the same way every
	// time.
	for _, name := range slices.Sorted(maps.Keys(cfg.Servers)) {
		s := cfg.Servers[name]
		if s != nil {
			g.connHashes[name] = s.ConnHash()
		}
		if served(s) {
			g.served = append(g.served, name)
		}
	}
}

// cachedLocked returns the cache entry of a server if it matches its current
// config. Caller holds g.mu.
func (g *Gateway) cachedLocked(name string) (CachedServer, bool) {
	h, known := g.connHashes[name]
	cs, ok := g.cache.Servers[name]
	if !ok || !known || cs.Hash != h {
		return CachedServer{}, false
	}
	return cs, true
}

// visibleToolsLocked returns a served server's cache entry and its tools
// minus the disabled ones; ok is false while its tools are unknown. Caller
// holds g.mu.
func (g *Gateway) visibleToolsLocked(name string) (cs CachedServer, tools []*mcp.Tool, ok bool) {
	cs, ok = g.cachedLocked(name)
	if !ok {
		return cs, nil, false
	}
	disabled := disabledSet(g.cfg.Servers[name])
	for _, t := range cs.Tools {
		if t != nil && !disabled[t.Name] {
			tools = append(tools, t)
		}
	}
	return cs, tools, true
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
	for _, name := range g.served {
		_, tools, _ := g.visibleToolsLocked(name)
		for _, t := range tools {
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
	// Decoded upstream and cached schemas already have this shape, so the
	// round trip below is only needed for anything else.
	if m, ok := schema.(map[string]any); ok && m["type"] == "object" {
		return m
	}
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
	for _, name := range g.served {
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
	go g.fetch(name, g.connHashes[name], s.EffectiveMode(), ch)
	return ch
}

func (g *Gateway) fetch(name, hash, mode string, ch chan struct{}) {
	defer g.wg.Done()
	// Closed last, after the catalog is rebuilt, so waiters see the tools.
	defer close(ch)
	ctx, cancel := context.WithTimeout(g.bg, g.opts.ListWait)
	info, tools, err := g.pool.List(ctx, name, mode)
	cancel()
	if snap, seq := g.fetched(name, hash, info, tools, err); snap != nil {
		g.saveCache(snap, seq)
	}
}

// fetched records a finished listing and returns a cache snapshot to save
// when the cache changed.
func (g *Gateway) fetched(name, hash string, info *mcp.InitializeResult, tools []*mcp.Tool, err error) (*Cache, uint64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.fetching, name)
	again := g.refetch[name]
	delete(g.refetch, name)
	var snap *Cache
	var seq uint64
	switch h, ok := g.connHashes[name]; {
	case !ok || h != hash:
		// Listed under a config that has since changed.
		again = true
	case g.closed:
	case errors.Is(err, context.DeadlineExceeded):
		g.log.Info("tools still not listed; retrying on the next request", "server", name, "wait", g.opts.ListWait.String())
	case err != nil:
		g.log.Warn("cannot list tools", "server", name, "err", err.Error())
	default:
		g.cache.Servers[name] = CachedServer{Hash: hash, Info: info, Tools: tools, UpdatedAt: time.Now()}
		g.cacheSeq++
		snap, seq = &Cache{Version: cacheVersion, Servers: maps.Clone(g.cache.Servers)}, g.cacheSeq
		g.rebuildLocked()
	}
	if again {
		g.fetchLocked(name)
	}
	return snap, seq
}

// saveCache writes snap outside g.mu, which every request takes, so a slow
// disk does not stall them. seq orders the snapshots: one taken earlier but
// arriving later must not replace a newer one on disk.
func (g *Gateway) saveCache(snap *Cache, seq uint64) {
	g.saveMu.Lock()
	defer g.saveMu.Unlock()
	if seq <= g.savedSeq {
		return
	}
	g.savedSeq = seq
	if err := snap.Save(g.paths); err != nil {
		g.log.Warn("cannot save cache", "err", err.Error())
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

// connected relists a server once a new connection to it is up, which is
// how the gateway learns of tools an upstream adds without announcing them,
// as remote ones cannot with their standalone SSE stream off. The decision
// is made before dial returns and only the listing runs in the background:
// decided later, a connection made by a listing could find that listing
// already done and relist once more.
func (g *Gateway) connected(name string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.relistDueLocked(name) {
		g.fetchLocked(name)
	}
}

// relistDueLocked reports whether a new connection to name calls for a
// relist. Caller holds g.mu.
func (g *Gateway) relistDueLocked(name string) bool {
	// A per-session server connects once per agent session, and its lists
	// come from a throwaway instance that would connect again.
	if s := g.cfg.Servers[name]; s == nil || s.EffectiveMode() == config.ModePerSession {
		return false
	}
	// A listing under way most likely made this connection. If it instead
	// fails on an instance that died meanwhile, the new instance's tools
	// wait for the next reconnect: dial cannot tell who asked for it.
	if g.fetching[name] != nil {
		return false
	}
	// Without an entry the first listing is under way or about to be.
	cs, ok := g.cachedLocked(name)
	// RelistAfter keeps reconnect loops (crashes, expired remote sessions)
	// from relisting every time.
	return ok && time.Since(cs.UpdatedAt) >= g.opts.RelistAfter
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
	for _, name := range g.served {
		cs, tools, ok := g.visibleToolsLocked(name)
		if !ok {
			continue
		}
		si := ServerInfo{Name: name, About: About(g.cfg.Servers[name].Description, cs.Info)}
		for _, t := range tools {
			si.Tools = append(si.Tools, ToolInfo{Name: t.Name, Description: t.Description})
		}
		if len(si.Tools) > 0 {
			infos = append(infos, si)
		}
	}
	return Instructions(infos)
}
