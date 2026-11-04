package manage

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/SkyeBeFreeman/maple/internal/config"
	"github.com/SkyeBeFreeman/maple/internal/manage/agentconf"
	"github.com/SkyeBeFreeman/maple/internal/platform/atomicfile"
	"github.com/SkyeBeFreeman/maple/internal/platform/launchd"
)

//go:embed skill/SKILL.md
var skillMD []byte

// launchdOps is the seam between the use cases and launchctl.
type launchdOps struct {
	Load   func(ctx context.Context, plistPath string) error
	Unload func(ctx context.Context) error
	Loaded func(ctx context.Context) (bool, error)
}

// launchctl is non-nil in production; package tests set it to nil in init
// unless MAPLE_TEST_LAUNCHD=1. When nil, install and uninstall skip launchctl
// and doctor reports launchd as skipped.
var launchctl = &launchdOps{Load: launchd.Load, Unload: launchd.Unload, Loaded: launchd.Loaded}

// bootout returns before launchd has fully released the label, so an
// immediate bootstrap can fail with EIO (5); one delayed retry covers it.
var bootstrapRetryDelay = 500 * time.Millisecond

// InstallOptions controls Install. Port only applies when config.yaml does
// not exist yet; 0 means the default port.
type InstallOptions struct {
	Port      int
	NoLaunchd bool
}

// InstallResult lists the files Install created or changed and what it did to
// each agent.
type InstallResult struct {
	Created []string           `json:"created"`
	Agents  []agentconf.Result `json:"agents"`
	Note    string             `json:"note,omitempty"`
}

// Install sets maple up; every step only fills in what is missing, so it is
// safe to rerun.
func Install(ctx context.Context, e Env, o InstallOptions) (*InstallResult, error) {
	res := &InstallResult{}
	if err := installState(ctx, e, o, res); err != nil {
		return res, err
	}
	if !o.NoLaunchd && launchctl != nil {
		if !filepath.IsAbs(e.MapleBin) {
			return res, fmt.Errorf("maple binary path %q is not absolute; run maple install from its installed location", e.MapleBin)
		}
		if err := installLaunchd(ctx, e, res); err != nil {
			return res, err
		}
	}
	agents, err := Sync(ctx, e, false)
	res.Agents = agents
	if err != nil {
		return res, err
	}
	changed, err := atomicfile.WriteFileIfDifferent(e.Paths.SkillFile(), skillMD, 0o644, 0o755)
	if err != nil {
		return res, fmt.Errorf("write %s: %w", e.Paths.SkillFile(), err)
	}
	if changed {
		res.Created = append(res.Created, e.Paths.SkillFile())
	}
	return res, nil
}

func installState(ctx context.Context, e Env, o InstallOptions, res *InstallResult) error {
	if _, err := os.Stat(e.Paths.MapleDir()); errors.Is(err, fs.ErrNotExist) {
		res.Created = append(res.Created, e.Paths.MapleDir())
	}
	// Lock creates ~/.maple with the right mode.
	unlock, err := lockConfig(ctx, e)
	if err != nil {
		return err
	}
	defer func() { _ = unlock() }()

	_, err = os.Stat(e.Paths.Config())
	switch {
	case errors.Is(err, fs.ErrNotExist):
		port := config.EffectivePort(o.Port)
		if err := probePort(port); err != nil {
			return fmt.Errorf("port %d is in use; rerun with --port", port)
		}
		if err := config.Save(e.Paths, &config.Config{Port: port, Servers: map[string]*config.Server{}}); err != nil {
			return fmt.Errorf("save config: %w", err)
		}
		res.Created = append(res.Created, e.Paths.Config())
	case err != nil:
		return err
	case o.Port != 0:
		raw, err := os.ReadFile(e.Paths.Config())
		if err != nil {
			return err
		}
		// Rewriting the user's config here would drop their comments.
		if cur := config.PeekPort(raw); cur != o.Port {
			res.Note = fmt.Sprintf("config.yaml already exists; port %d unchanged (--port %d ignored); edit port in %s and run: maple sync", cur, o.Port, e.Paths.Config())
		}
	}
	_, created, err := config.EnsureToken(e.Paths)
	if err != nil {
		return fmt.Errorf("create %s: %w", e.Paths.Token(), err)
	}
	if created {
		res.Created = append(res.Created, e.Paths.Token())
	}
	return nil
}

// probePort fails when another process listens on the gateway's port.
func probePort(port int) error {
	ln, err := net.Listen("tcp", config.LoopbackAddr(port))
	if err != nil {
		return err
	}
	return ln.Close()
}

func installLaunchd(ctx context.Context, e Env, res *InstallResult) error {
	if err := os.MkdirAll(e.Paths.LogDir(), 0o700); err != nil {
		return err
	}
	path := launchd.PlistPath(e.Paths.Home)
	changed, err := atomicfile.WriteFileIfDifferent(path, launchd.Plist(e.MapleBin, filepath.Join(e.Paths.LogDir(), "launchd.log")), 0o644, 0o755)
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if changed {
		res.Created = append(res.Created, path)
	} else if loaded, err := launchctl.Loaded(ctx); err == nil && loaded {
		return nil
	}
	err = launchctl.Load(ctx, path)
	if err != nil && (strings.Contains(err.Error(), "Bootstrap failed: 5") || strings.Contains(err.Error(), "exit status 5")) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(bootstrapRetryDelay):
		}
		err = launchctl.Load(ctx, path)
	}
	if err != nil {
		return fmt.Errorf("load LaunchAgent: %w", err)
	}
	return nil
}

// UninstallOptions controls Uninstall. Purge also deletes ~/.maple and every
// stored secret.
type UninstallOptions struct {
	Purge     bool
	NoLaunchd bool
}

// Uninstall undoes Install: the LaunchAgent, maple's entries in the agents
// and the skill. User entries in the agents are left as they are.
func Uninstall(ctx context.Context, e Env, o UninstallOptions) error {
	if !o.NoLaunchd && launchctl != nil {
		if err := launchctl.Unload(ctx); err != nil {
			return fmt.Errorf("unload LaunchAgent: %w", err)
		}
	}
	// Removed even without launchctl, or launchd would start it at next login.
	if err := removeIfExists(launchd.PlistPath(e.Paths.Home)); err != nil {
		return err
	}
	left, err := removeEntries(ctx, e)
	if err != nil {
		return err
	}
	if err := removeIfExists(e.Paths.SkillFile()); err != nil {
		return err
	}
	// Only removes the directory if nothing else was put there.
	_ = os.Remove(filepath.Dir(e.Paths.SkillFile()))
	if o.Purge {
		if err := purge(e); err != nil {
			return err
		}
	}
	if len(left) > 0 {
		return fmt.Errorf("left entries you edited by hand: %s; remove them from the agent configs yourself", strings.Join(left, ", "))
	}
	return nil
}

func purge(e Env) error {
	if err := os.RemoveAll(e.Paths.MapleDir()); err != nil {
		return err
	}
	names, err := e.Keychain.List()
	if err != nil {
		return fmt.Errorf("list secrets: %w", err)
	}
	for _, name := range names {
		if err := e.Keychain.Delete(name); err != nil {
			return fmt.Errorf("remove secret %s: %w", name, err)
		}
	}
	return nil
}

// removeEntries deletes maple's entries from the agents and returns those it
// left because the user edited them.
func removeEntries(ctx context.Context, e Env) ([]string, error) {
	if _, err := os.Stat(e.Paths.Applied()); errors.Is(err, fs.ErrNotExist) {
		// Nothing recorded means maple never wrote an entry.
		return nil, nil
	}
	unlock, err := lockConfig(ctx, e)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unlock() }()
	applied, err := config.LoadApplied(e.Paths)
	if err != nil {
		return nil, err
	}
	results, applyErr := agentconf.Apply(e.Paths, applied, nil, agentconf.ApplyOptions{Agents: e.Agents})
	if err := config.SaveApplied(e.Paths, applied); err != nil {
		return nil, errors.Join(applyErr, err)
	}
	if applyErr != nil {
		return nil, fmt.Errorf("remove maple entries: %w", applyErr)
	}
	var left []string
	for _, r := range results {
		for _, s := range r.Skipped {
			left = append(left, r.Agent+"/"+s.Name)
		}
	}
	return left, nil
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
