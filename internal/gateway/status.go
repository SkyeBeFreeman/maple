package gateway

import (
	"maps"
	"slices"

	"github.com/SkyeBeFreeman/maple/internal/config"
)

// status reports every server the gateway runs; direct servers belong to
// the agents and are left out.
func (g *Gateway) status() config.Status {
	ps := g.pool.Status()
	g.mu.Lock()
	defer g.mu.Unlock()
	st := config.Status{
		Version:     g.opts.Version,
		ConfigHash:  g.cfgHash,
		ReloadError: g.reloadErr,
		Servers:     []config.ServerStatus{},
	}
	tools := map[string]int{}
	for _, e := range g.tools {
		tools[e.server]++
	}
	for _, name := range slices.Sorted(maps.Keys(g.cfg.Servers)) {
		if g.cfg.Servers[name].IsDirect() {
			continue
		}
		ss := config.ServerStatus{Name: name, State: config.StateIdle, Tools: tools[name]}
		if p, ok := ps[name]; ok {
			ss.State, ss.PID, ss.LastError = p.State, p.PID, g.red.Redact(p.LastError)
		}
		if !g.cfg.Servers[name].IsEnabled() {
			ss.State = config.StateStopped
		}
		st.Servers = append(st.Servers, ss)
	}
	return st
}
