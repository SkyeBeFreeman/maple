package gateway

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/SkyeBeFreeman/maple/internal/config"
)

// loadInitial reads token, cache and config at startup. A missing or broken
// config still lets the gateway serve, with no tools and the error in status.
func (g *Gateway) loadInitial() {
	g.loadToken()
	cache := LoadCache(g.paths)
	g.mu.Lock()
	g.cache = cache
	g.mu.Unlock()
	g.reload()
}

func (g *Gateway) loadToken() {
	tok, err := config.LoadToken(g.paths)
	if err != nil {
		// Keeping the old token beats locking every agent out over a
		// half-written file; an absent token rejects all requests anyway.
		g.log.Warn("cannot read token", "err", err.Error())
		return
	}
	g.mu.Lock()
	g.token = tok
	g.mu.Unlock()
}

// reload applies config.yaml if its content changed. An invalid file leaves
// the previous config in force.
func (g *Gateway) reload() {
	raw, err := os.ReadFile(g.paths.Config())
	g.mu.Lock()
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			g.reloadErr = "config.yaml not found; run: maple mcp add"
		} else {
			g.reloadErr = g.red.Redact(err.Error())
		}
		g.mu.Unlock()
		g.log.Warn("cannot read config", "err", g.reloadErr)
		return
	}
	hash := config.ContentHash(raw)
	if hash == g.cfgHash {
		// Back to the loaded content after a rejected edit.
		g.reloadErr = ""
		g.mu.Unlock()
		return
	}
	cfg, err := config.Parse(raw)
	if err != nil {
		g.reloadErr = "config.yaml: " + g.red.Redact(err.Error())
		g.mu.Unlock()
		g.log.Warn("config rejected, keeping the previous one", "err", g.reloadErr)
		return
	}
	if g.opts.Port != 0 && cfg.Port != g.opts.Port {
		// Agents are being pointed at the new port, so staying on the old
		// one would strand them. Not applied: the restart loads it anew.
		g.reloadErr = fmt.Sprintf("config.yaml: port changed from %d to %d; the gateway restarts on the new port", g.opts.Port, cfg.Port)
		if !g.portSignalled {
			g.portSignalled = true
			close(g.portChanged)
		}
		g.mu.Unlock()
		g.log.Warn("port changed, stopping so launchd restarts the gateway", "from", g.opts.Port, "to", cfg.Port)
		return
	}
	old := g.cfg
	g.cfg, g.cfgHash, g.reloadErr = cfg, hash, ""
	var reset []string
	for name, prev := range old.Servers {
		ns := cfg.Servers[name]
		if !served(ns) || connHash(ns) != connHash(prev) {
			reset = append(reset, name)
		}
	}
	for name := range g.cache.Servers {
		if cfg.Servers[name] == nil {
			delete(g.cache.Servers, name)
		}
	}
	g.rebuildLocked()
	g.mu.Unlock()
	g.log.Info("config loaded", "hash", hash, "servers", len(cfg.Servers))

	// Before listing, so that no listing runs on an instance about to stop.
	for _, name := range reset {
		g.pool.Reset(name)
	}
	g.mu.Lock()
	g.fetchMissingLocked()
	g.mu.Unlock()
}

// watch reloads after changes to config.yaml or token settle. The directory
// is watched because atomic writes replace the files.
func (g *Gateway) watch() (stop func(), err error) {
	if err := os.MkdirAll(g.paths.MapleDir(), 0o700); err != nil {
		return nil, err
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	if err := w.Add(g.paths.MapleDir()); err != nil {
		_ = w.Close()
		return nil, err
	}
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		var timer *time.Timer
		var fire <-chan time.Time
		token, cfg := false, false
		for {
			select {
			case <-done:
				if timer != nil {
					timer.Stop()
				}
				return
			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				switch filepath.Base(ev.Name) {
				case filepath.Base(g.paths.Config()):
					cfg = true
				case filepath.Base(g.paths.Token()):
					token = true
				default:
					continue
				}
				if timer == nil {
					timer = time.NewTimer(g.opts.Debounce)
				} else {
					timer.Reset(g.opts.Debounce)
				}
				fire = timer.C
			case err, ok := <-w.Errors:
				if !ok {
					return
				}
				g.log.Warn("config watch error", "err", err.Error())
			case <-fire:
				fire = nil
				if token {
					g.loadToken()
				}
				if cfg {
					g.reload()
				}
				token, cfg = false, false
			}
		}
	}()
	return func() {
		close(done)
		<-finished
		_ = w.Close()
	}, nil
}
