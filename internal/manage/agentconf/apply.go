package agentconf

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"slices"

	"github.com/SkyeBeFreeman/maple/internal/config"
	"github.com/SkyeBeFreeman/maple/internal/platform/atomicfile"
)

// GatewayName is the entry name of the maple gateway in every agent.
const GatewayName = config.GatewayName

const (
	maxAttempts    = 3
	reasonConflict = "conflicts with your own entry; rename it or run maple mcp import"
	reasonModified = "modified by you; rerun with --force to overwrite"
)

// beforeWrite lets tests simulate a concurrent writer between read and write.
var beforeWrite func(path string)

// Desired returns the entries maple wants in every agent: the gateway first,
// then one stdio entry per enabled direct server, sorted by name.
func Desired(cfg *config.Config, token, mapleBin string) []Entry {
	out := []Entry{{
		Name:    GatewayName,
		URL:     "http://" + config.LoopbackAddr(config.EffectivePort(cfg.Port)) + "/mcp",
		Headers: map[string]string{"Authorization": "Bearer " + token},
	}}
	names := slices.Sorted(maps.Keys(cfg.Servers))
	for _, name := range names {
		s := cfg.Servers[name]
		if s.IsDirect() && s.IsEnabled() {
			out = append(out, Entry{Name: name, Command: mapleBin, Args: []string{"run", name}})
		}
	}
	return out
}

// ApplyOptions controls Apply. Empty Agents means All().
type ApplyOptions struct {
	Force  bool
	Agents []Agent
}

// Result describes what Apply did to one agent's file.
type Result struct {
	Agent   string   `json:"agent"`
	Path    string   `json:"path"`
	Missing bool     `json:"missing"`
	Written []string `json:"written"`
	Removed []string `json:"removed"`
	Skipped []Skip   `json:"skipped"`
}

// Skip is an entry Apply left alone, with the reason shown to the user.
type Skip struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// Apply writes desired into each agent's file, touching only entries recorded
// in applied, and updates applied in place; the caller saves it. A failure in
// one agent does not stop the others.
func Apply(p config.Paths, applied *config.Applied, desired []Entry, opts ApplyOptions) ([]Result, error) {
	agents := opts.Agents
	if len(agents) == 0 {
		agents = All()
	}
	if applied.Agents == nil {
		applied.Agents = map[string]*config.AgentApplied{}
	}
	var results []Result
	var errs []error
	for _, a := range agents {
		var recorded map[string]string
		if aa := applied.Agents[a.ID()]; aa != nil {
			recorded = aa.Entries
		}
		res, rec, err := applyAgent(p, a, recorded, desired, opts.Force)
		results = append(results, res)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", a.ID(), err))
			continue
		}
		if res.Missing {
			continue
		}
		aa := applied.Agents[a.ID()]
		if aa == nil {
			if len(rec) == 0 {
				continue
			}
			aa = &config.AgentApplied{}
			applied.Agents[a.ID()] = aa
		}
		aa.Entries = rec
	}
	return results, errors.Join(errs...)
}

func applyAgent(p config.Paths, a Agent, recorded map[string]string, desired []Entry, force bool) (Result, map[string]string, error) {
	path := a.Path(p)
	var res Result
	var rec map[string]string
	err := editFile(path, func(data []byte) ([]byte, error) {
		// Recomputed from scratch on each retry so a concurrent edit is seen.
		res = Result{Agent: a.ID(), Path: path}
		rec = maps.Clone(recorded)
		if rec == nil {
			rec = map[string]string{}
		}
		return plan(a, data, desired, rec, force, &res)
	})
	if errors.Is(err, fs.ErrNotExist) {
		return Result{Agent: a.ID(), Path: path, Missing: true}, nil, nil
	}
	if err != nil {
		return res, nil, err
	}
	return res, rec, nil
}

func plan(a Agent, data []byte, desired []Entry, rec map[string]string, force bool, res *Result) ([]byte, error) {
	want := map[string]bool{}
	for _, e := range desired {
		want[e.Name] = true
		cur, ok, err := a.Canonical(data, e.Name)
		if err != nil {
			return nil, err
		}
		h, owned := rec[e.Name]
		if ok && !owned {
			res.Skipped = append(res.Skipped, Skip{e.Name, reasonConflict})
			continue
		}
		if ok && config.ContentHash(cur) != h && !force {
			res.Skipped = append(res.Skipped, Skip{e.Name, reasonModified})
			continue
		}
		next, err := a.Set(data, e)
		if err != nil {
			return nil, err
		}
		nc, _, err := a.Canonical(next, e.Name)
		if err != nil {
			return nil, err
		}
		rec[e.Name] = config.ContentHash(nc)
		if ok && bytes.Equal(cur, nc) {
			// Keep the original bytes: Set may reformat an identical entry.
			continue
		}
		data = next
		res.Written = append(res.Written, e.Name)
	}
	stale := slices.Sorted(maps.Keys(rec))
	for _, name := range stale {
		if want[name] {
			continue
		}
		cur, ok, err := a.Canonical(data, name)
		if err != nil {
			return nil, err
		}
		if !ok {
			delete(rec, name)
			continue
		}
		if config.ContentHash(cur) != rec[name] && !force {
			res.Skipped = append(res.Skipped, Skip{name, reasonModified})
			continue
		}
		if data, err = a.Remove(data, name); err != nil {
			return nil, err
		}
		delete(rec, name)
		res.Removed = append(res.Removed, name)
	}
	return data, nil
}

// RemoveForeign deletes the user's own entries from a's file, e.g. after
// import moved them into maple. Absent names are ignored.
func RemoveForeign(p config.Paths, a Agent, names []string) error {
	return editFile(a.Path(p), func(data []byte) ([]byte, error) {
		for _, name := range names {
			_, ok, err := a.Canonical(data, name)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			if data, err = a.Remove(data, name); err != nil {
				return nil, err
			}
		}
		return data, nil
	})
}

// editFile reads path, applies edit and writes the result only if the file
// did not change meanwhile, retrying because agents such as Claude Code
// rewrite their config often. A missing file returns an fs.ErrNotExist error
// and is never created: the agent is not installed.
func editFile(path string, edit func(data []byte) ([]byte, error)) error {
	for range maxAttempts {
		fi, err := os.Stat(path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out, err := edit(data)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if bytes.Equal(out, data) {
			return nil
		}
		if beforeWrite != nil {
			beforeWrite(path)
		}
		err = atomicfile.WriteFileIfUnchanged(path, out, fi.Mode().Perm(), fi)
		if errors.Is(err, atomicfile.ErrModified) {
			continue
		}
		return err
	}
	return fmt.Errorf("%s: still changing after %d attempts; retry later", path, maxAttempts)
}
