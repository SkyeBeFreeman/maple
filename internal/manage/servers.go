package manage

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/SkyeBeFreeman/maple/internal/config"
	"github.com/SkyeBeFreeman/maple/internal/manage/agentconf"
	"github.com/SkyeBeFreeman/maple/internal/platform/keychain"
)

// ChangeResult reports the side effects of a config change. Agents is empty
// when the agents' files did not need to change. Reloaded tells whether the
// running gateway picked the change up; Note explains why not.
type ChangeResult struct {
	Agents   []agentconf.Result `json:"agents"`
	Reloaded bool               `json:"reloaded"`
	Note     string             `json:"note,omitempty"`
}

// Add adds a new server.
func Add(ctx context.Context, e Env, name string, s *config.Server) (*ChangeResult, error) {
	return change(ctx, e, false, []string{name}, func(c *config.Config) error {
		if _, ok := c.Servers[name]; ok {
			return fmt.Errorf("server %q already exists; use: maple mcp set %s", name, name)
		}
		c.Servers[name] = s
		return nil
	})
}

// Set changes an existing server through edit. force overwrites agent
// entries the user modified by hand.
func Set(ctx context.Context, e Env, name string, edit func(*config.Server) error, force bool) (*ChangeResult, error) {
	return change(ctx, e, force, []string{name}, func(c *config.Config) error {
		s, err := lookup(c, name)
		if err != nil {
			return err
		}
		return edit(s)
	})
}

// Remove deletes a server.
func Remove(ctx context.Context, e Env, name string, force bool) (*ChangeResult, error) {
	return change(ctx, e, force, []string{name}, func(c *config.Config) error {
		if _, err := lookup(c, name); err != nil {
			return err
		}
		delete(c.Servers, name)
		return nil
	})
}

// SetEnabled enables or disables a server.
func SetEnabled(ctx context.Context, e Env, name string, on bool) (*ChangeResult, error) {
	return change(ctx, e, false, []string{name}, func(c *config.Config) error {
		s, err := lookup(c, name)
		if err != nil {
			return err
		}
		if on {
			// Unset is enabled; dropping the field keeps the file minimal.
			s.Enabled = nil
		} else {
			s.Enabled = &on
		}
		return nil
	})
}

func lockConfig(ctx context.Context, e Env) (func() error, error) {
	unlock, err := config.Lock(ctx, e.Paths)
	if err != nil {
		return nil, fmt.Errorf("lock %s: %w", e.Paths.MapleDir(), err)
	}
	return unlock, nil
}

func lookup(c *config.Config, name string) (*config.Server, error) {
	s, ok := c.Servers[name]
	if !ok || s == nil {
		return nil, fmt.Errorf("server %q not found; run: maple mcp list", name)
	}
	return s, nil
}

// change runs one locked read-modify-write of config.yaml, updates the
// agents when the set of direct servers changed, and then checks that the
// gateway reloaded. names are the servers mutate adds or edits.
func change(ctx context.Context, e Env, force bool, names []string, mutate func(*config.Config) error) (*ChangeResult, error) {
	res, applyErr, err := commit(ctx, e, force, names, mutate)
	if err != nil {
		return nil, err
	}
	if applyErr != nil {
		return res, fmt.Errorf("config.yaml saved, but updating agent configs failed: %w", applyErr)
	}
	return res, nil
}

// commit is change with the two failure kinds kept apart: err means
// config.yaml was not saved, applyErr means it was but the agents were not
// updated.
func commit(ctx context.Context, e Env, force bool, names []string, mutate func(*config.Config) error) (res *ChangeResult, applyErr, err error) {
	raw, res, applyErr, err := write(ctx, e, force, names, mutate)
	if err != nil {
		return nil, nil, err
	}
	// Outside the lock: the gateway takes it to reload.
	res.Reloaded, res.Note = confirmReload(ctx, e, raw)
	return res, applyErr, nil
}

func write(ctx context.Context, e Env, force bool, names []string, mutate func(*config.Config) error) (raw []byte, res *ChangeResult, applyErr, err error) {
	unlock, err := lockConfig(ctx, e)
	if err != nil {
		return nil, nil, nil, err
	}
	defer func() { _ = unlock() }()

	c, _, err := load(e.Paths)
	if err != nil {
		return nil, nil, nil, err
	}
	before := directSet(c)
	if err := mutate(c); err != nil {
		return nil, nil, nil, err
	}
	if err := c.Validate(); err != nil {
		return nil, nil, nil, err
	}
	for _, name := range names {
		// A disabled server is never started, so it may wait for its secrets.
		if s, ok := c.Servers[name]; ok && s != nil && s.IsEnabled() {
			if err := checkSecrets(e.Keychain, name, s); err != nil {
				return nil, nil, nil, err
			}
		}
	}
	changed := !slices.Equal(before, directSet(c))
	var token string
	if changed {
		// Read before saving so a missing token leaves config.yaml untouched.
		if token, err = loadToken(e.Paths); err != nil {
			return nil, nil, nil, err
		}
	}
	if err := config.Save(e.Paths, c); err != nil {
		return nil, nil, nil, fmt.Errorf("save config: %w", err)
	}
	_, raw, err = config.Load(e.Paths)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("reread config: %w", err)
	}
	res = &ChangeResult{}
	if changed {
		res.Agents, applyErr = apply(e, c, token, force)
	}
	return raw, res, applyErr, nil
}

// directSet is what the agents' direct entries depend on.
func directSet(c *config.Config) []string {
	var names []string
	for _, name := range slices.Sorted(maps.Keys(c.Servers)) {
		s := c.Servers[name]
		if s != nil && s.IsStdio() && s.EffectiveMode() == config.ModeDirect && s.IsEnabled() {
			names = append(names, name)
		}
	}
	return names
}

// checkSecrets uses List rather than Get: only names are needed, and reading
// values can make the keychain ask the user for permission.
func checkSecrets(kc keychain.Store, server string, s *config.Server) error {
	refs := s.SecretRefs()
	if len(refs) == 0 {
		return nil
	}
	have, err := kc.List()
	if err != nil {
		return fmt.Errorf("maple: %s: list secrets: %w", server, err)
	}
	for _, ref := range refs {
		if !slices.Contains(have, ref) {
			return secretErr(server, ref, keychain.ErrNotFound)
		}
	}
	return nil
}

func secretErr(server, name string, err error) error {
	if errors.Is(err, keychain.ErrNotFound) {
		return fmt.Errorf("maple: %s: missing secret %s; run: maple secret set %s", server, name, name)
	}
	return fmt.Errorf("maple: %s: read secret %s: %w", server, name, err)
}

// apply writes the desired entries and saves applied.json even when some
// agents failed, so what was written stays recorded.
func apply(e Env, c *config.Config, token string, force bool) ([]agentconf.Result, error) {
	applied, err := config.LoadApplied(e.Paths)
	if err != nil {
		return nil, err
	}
	desired := agentconf.Desired(c, token, e.MapleBin)
	results, applyErr := agentconf.Apply(e.Paths, applied, desired, agentconf.ApplyOptions{Force: force, Agents: e.Agents})
	if err := config.SaveApplied(e.Paths, applied); err != nil {
		return results, errors.Join(applyErr, fmt.Errorf("save %s: %w", e.Paths.Applied(), err))
	}
	return results, applyErr
}
