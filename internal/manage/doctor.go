package manage

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/SkyeBeFreeman/maple/internal/config"
	"github.com/SkyeBeFreeman/maple/internal/manage/agentconf"
	"github.com/SkyeBeFreeman/maple/internal/platform/keychain"
	"github.com/SkyeBeFreeman/maple/internal/platform/proc"
)

// Check is one line of `maple doctor`. Fix is set when OK is false.
type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

const (
	fixInstall     = "run: maple install"
	fixSync        = "run: maple sync"
	fixKeychain    = "allow maple to access the keychain, then rerun"
	detailNoConfig = "skipped: config.yaml could not be loaded"
)

// doctorState is what several checks share, loaded once.
type doctorState struct {
	cfg        *config.Config
	cfgErr     error
	token      string
	tokenErr   error
	applied    *config.Applied
	appliedErr error
	gatewayUp  bool
	agents     []agentFile
}

// agentFile is one agent's config file, read and listed once for every
// check that looks at it.
type agentFile struct {
	a       agentconf.Agent
	path    string
	data    []byte
	readErr error
	list    []agentconf.Foreign
	listErr error
}

func readAgentFile(e Env, a agentconf.Agent) agentFile {
	f := agentFile{a: a, path: a.Path(e.Paths)}
	if f.data, f.readErr = os.ReadFile(f.path); f.readErr == nil {
		f.list, f.listErr = a.List(f.data)
	}
	return f
}

// Doctor runs every check of spec 10.3 in a fixed order. It never reads
// secret values.
func Doctor(ctx context.Context, e Env) []Check {
	var st doctorState
	st.cfg, st.cfgErr = LoadConfig(e.Paths)
	st.token, st.tokenErr = loadToken(e.Paths)
	st.applied, st.appliedErr = config.LoadApplied(e.Paths)
	for _, a := range agents(e) {
		st.agents = append(st.agents, readAgentFile(e, a))
	}

	checks := []Check{doctorLaunchd(ctx), doctorGateway(ctx, e, &st)}
	checks = append(checks, doctorPort(&st))
	for _, f := range st.agents {
		checks = append(checks, doctorAgent(e, &st, f))
	}
	checks = append(checks,
		doctorDirect(&st),
		doctorApplied(e, &st),
		doctorSecrets(e, &st),
		doctorCommands(ctx, e, &st),
		doctorConfig(e, &st),
	)
	return checks
}

func ok(name, detail string) Check { return Check{Name: name, OK: true, Detail: detail} }

func fail(name, detail, fix string) Check {
	return Check{Name: name, Detail: detail, Fix: fix}
}

func doctorLaunchd(ctx context.Context) Check {
	if launchctl == nil {
		return ok("launchd", "skipped")
	}
	loaded, err := launchctl.Loaded(ctx)
	switch {
	case err != nil:
		return fail("launchd", err.Error(), fixInstall)
	case !loaded:
		return fail("launchd", "LaunchAgent is not loaded", fixInstall)
	}
	return ok("launchd", "LaunchAgent is loaded")
}

func doctorGateway(ctx context.Context, e Env, st *doctorState) Check {
	const name = "gateway"
	if st.cfgErr != nil || st.tokenErr != nil {
		return fail(name, "skipped: config.yaml or token could not be loaded", fixInstall)
	}
	s, err := fetchStatus(ctx, e, st.cfg.Port, st.token)
	if err != nil {
		return fail(name, err.Error(), "run: maple install, then check "+e.Paths.LogDir())
	}
	st.gatewayUp = true
	if s.ReloadError != "" {
		return fail(name, "last reload failed: "+s.ReloadError, "fix config.yaml; the gateway reloads it automatically")
	}
	return ok(name, "reachable, version "+s.Version)
}

func doctorPort(st *doctorState) Check {
	const name = "port"
	if st.cfgErr != nil {
		return fail(name, detailNoConfig, "fix config.yaml")
	}
	port := strconv.Itoa(st.cfg.Port)
	if st.gatewayUp {
		return ok(name, "port "+port+" is held by the maple gateway")
	}
	if probePort(st.cfg.Port) != nil {
		return fail(name, "port "+port+" is used by another process",
			"find it with: lsof -nP -iTCP:"+port+" -sTCP:LISTEN; stop it or change port in config.yaml and run: maple sync")
	}
	return ok(name, "port "+port+" is free (the gateway is not listening)")
}

func doctorAgent(e Env, st *doctorState, f agentFile) Check {
	a, id, path := f.a, f.a.ID(), f.path
	name := "agent:" + id
	if errors.Is(f.readErr, os.ErrNotExist) {
		return ok(name, "not installed")
	}
	if f.readErr != nil {
		return fail(name, f.readErr.Error(), "check the permissions of "+path)
	}
	if f.listErr != nil {
		return fail(name, path+": "+f.listErr.Error(), "fix the syntax of "+path)
	}
	list := f.list
	i := slices.IndexFunc(list, func(f agentconf.Foreign) bool { return f.Name == agentconf.GatewayName })
	switch {
	case i < 0:
		return fail(name, "no maple entry in "+path, fixSync)
	case st.cfgErr != nil || st.tokenErr != nil:
		return fail(name, "skipped: config.yaml or token could not be loaded", fixInstall)
	}
	want := agentconf.Desired(st.cfg, st.token, e.MapleBin)[0]
	got := list[i]
	if got.URL == want.URL && headerValue(got.Headers, "Authorization") == want.Headers["Authorization"] {
		return ok(name, "maple entry is up to date")
	}
	detail := "maple entry does not match the current token or port"
	var h string
	if st.appliedErr == nil && st.applied.Agents[id] != nil {
		h = st.applied.Agents[id].Entries[agentconf.GatewayName]
	}
	cur, _, _ := a.Canonical(f.data, agentconf.GatewayName)
	switch {
	case h == "":
		return fail(name, detail+"; it is your own entry, not maple's", "rename or remove your maple entry in "+path+", then run: maple sync")
	case config.ContentHash(cur) != h:
		return fail(name, detail, "run: maple sync --force (overwrites your edits)")
	}
	return fail(name, detail, fixSync)
}

// headerValue looks key up case-insensitively, as HTTP does.
func headerValue(h map[string]string, key string) string {
	for k, v := range h {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return ""
}

// recorded returns, per agent, the entry names maple wrote.
func recorded(st *doctorState) map[string][]string {
	out := map[string][]string{}
	if st.appliedErr != nil {
		return out
	}
	for id, aa := range st.applied.Agents {
		if aa != nil {
			out[id] = slices.Sorted(maps.Keys(aa.Entries))
		}
	}
	return out
}

func doctorDirect(st *doctorState) Check {
	const name = "direct-entries"
	var bad []string
	n := 0
	rec := recorded(st)
	for _, af := range st.agents {
		if af.readErr != nil || af.listErr != nil {
			continue
		}
		names := rec[af.a.ID()]
		for _, f := range af.list {
			if f.Name == agentconf.GatewayName || !slices.Contains(names, f.Name) || f.Command == "" {
				continue
			}
			n++
			if fi, err := os.Stat(f.Command); err != nil || fi.IsDir() {
				bad = append(bad, af.a.ID()+"/"+f.Name+" -> "+f.Command)
			}
		}
	}
	if len(bad) > 0 {
		return fail(name, "maple binary not found: "+strings.Join(bad, ", "), fixSync)
	}
	if n == 0 {
		return ok(name, "none")
	}
	return ok(name, fmt.Sprintf("%d entries point to an existing maple binary", n))
}

func doctorApplied(e Env, st *doctorState) Check {
	const name = "applied"
	if st.appliedErr != nil {
		return fail(name, st.appliedErr.Error(), "delete "+e.Paths.Applied()+" and run: maple sync")
	}
	var bad []string
	for _, f := range st.agents {
		a := f.a
		aa := st.applied.Agents[a.ID()]
		if aa == nil || len(aa.Entries) == 0 {
			continue
		}
		if errors.Is(f.readErr, os.ErrNotExist) {
			// The agent was uninstalled; sync cannot change that.
			continue
		}
		if f.readErr != nil {
			bad = append(bad, a.ID()+": "+f.readErr.Error())
			continue
		}
		for _, entry := range slices.Sorted(maps.Keys(aa.Entries)) {
			cur, found, err := a.Canonical(f.data, entry)
			switch {
			case err != nil:
				bad = append(bad, a.ID()+": "+err.Error())
			case !found:
				bad = append(bad, a.ID()+"/"+entry+" is missing")
			case config.ContentHash(cur) != aa.Entries[entry]:
				bad = append(bad, a.ID()+"/"+entry+" was edited by hand")
			}
		}
	}
	if len(bad) > 0 {
		return fail(name, strings.Join(bad, "; "), "run: maple sync (add --force to overwrite hand edits)")
	}
	return ok(name, "agent configs match applied.json")
}

func doctorSecrets(e Env, st *doctorState) Check {
	const name = "secrets"
	if st.cfgErr != nil {
		return fail(name, detailNoConfig, "fix config.yaml")
	}
	want := map[string][]string{}
	for _, server := range slices.Sorted(maps.Keys(st.cfg.Servers)) {
		s := st.cfg.Servers[server]
		if s == nil || !s.IsEnabled() {
			continue
		}
		for _, ref := range s.SecretRefs() {
			want[ref] = append(want[ref], server)
		}
	}
	if len(want) == 0 {
		return ok(name, "no secrets referenced")
	}
	have, err := e.Keychain.List()
	if err != nil {
		if errors.Is(err, keychain.ErrDenied) {
			return fail(name, err.Error(), fixKeychain)
		}
		return fail(name, err.Error(), "check that the login keychain is unlocked, then rerun")
	}
	var missing, cmds []string
	for _, ref := range slices.Sorted(maps.Keys(want)) {
		if !slices.Contains(have, ref) {
			missing = append(missing, ref+" (used by "+strings.Join(want[ref], ", ")+")")
			cmds = append(cmds, "maple secret set "+ref)
		}
	}
	if len(missing) > 0 {
		return fail(name, "missing: "+strings.Join(missing, ", "), "run: "+strings.Join(cmds, "; "))
	}
	return ok(name, fmt.Sprintf("all %d referenced secrets exist", len(want)))
}

func doctorCommands(ctx context.Context, e Env, st *doctorState) Check {
	const name = "commands"
	if st.cfgErr != nil {
		return fail(name, detailNoConfig, "fix config.yaml")
	}
	var pathEnv string
	var bad []string
	n := 0
	for _, server := range slices.Sorted(maps.Keys(st.cfg.Servers)) {
		s := st.cfg.Servers[server]
		if s == nil || !s.IsStdio() || !s.IsEnabled() {
			continue
		}
		// Only computed when needed: it starts the user's login shell.
		if pathEnv == "" {
			pathEnv = proc.LoginShellPath(ctx, e.Shell)
		}
		n++
		// A command made of a secret reference cannot be checked without
		// reading the secret.
		if strings.Contains(s.Command, "${") {
			continue
		}
		if _, err := proc.LookPath(s.Command, pathEnv); err != nil {
			bad = append(bad, server+": "+s.Command)
		}
	}
	if len(bad) > 0 {
		return fail(name, "not found in the login shell PATH: "+strings.Join(bad, ", "),
			"install the missing command, or give the server its absolute path with: maple mcp set")
	}
	return ok(name, fmt.Sprintf("%d stdio commands found", n))
}

func doctorConfig(e Env, st *doctorState) Check {
	if st.cfgErr != nil {
		return fail("config", st.cfgErr.Error(), "fix "+e.Paths.Config()+" at the reported line")
	}
	return ok("config", e.Paths.Config()+" is valid")
}
