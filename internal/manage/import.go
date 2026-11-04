package manage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/SkyeBeFreeman/maple/internal/config"
	"github.com/SkyeBeFreeman/maple/internal/gateway/upstream"
	"github.com/SkyeBeFreeman/maple/internal/manage/agentconf"
)

const probeTimeout = 10 * time.Second

// ImportOptions controls Import. Without Names, the candidates are only
// listed. Rename receives the default secret names keyed by
// "<server>.<headers|env>.<key>" and returns the names to use under the same
// keys.
type ImportOptions struct {
	Names           []string
	RemoveOriginals bool
	Rename          func(defaults map[string]string) map[string]string
}

// ImportResult reports what Import found and did. SecretNames uses the same
// keys as ImportOptions.Rename. Conflicts warns about names whose content
// differs between agents; Skipped maps names that cannot be imported to why.
type ImportResult struct {
	Candidates   []agentconf.Foreign `json:"candidates"`
	Imported     []string            `json:"imported"`
	SkippedOAuth []string            `json:"skipped_oauth"`
	SecretNames  map[string]string   `json:"secret_names"`
	Backup       string              `json:"backup,omitempty"`
	Conflicts    []string            `json:"conflicts"`
	Skipped      map[string]string   `json:"skipped"`
	Change       *ChangeResult       `json:"change,omitempty"`
}

type candidate struct {
	f      agentconf.Foreign
	agents []agentconf.Agent
}

// Import moves the user's own MCP servers from the agents into config.yaml,
// storing plaintext secrets in the keychain.
func Import(ctx context.Context, e Env, o ImportOptions) (*ImportResult, error) {
	c, err := LoadConfig(e.Paths)
	if err != nil {
		return nil, err
	}
	res := &ImportResult{SecretNames: map[string]string{}, Skipped: map[string]string{}}
	cands, order, err := findCandidates(e, c, res)
	if err != nil {
		return nil, err
	}
	for _, name := range order {
		res.Candidates = append(res.Candidates, cands[name].f)
	}

	if len(o.Names) == 0 {
		return res, nil
	}
	for _, name := range o.Names {
		if _, ok := cands[name]; !ok {
			if why, ok := res.Skipped[name]; ok {
				return res, fmt.Errorf("cannot import %q: %s", name, why)
			}
			return res, fmt.Errorf("server %q not found in any agent config; run: maple mcp import", name)
		}
	}
	selected := slices.Compact(slices.Sorted(slices.Values(o.Names)))

	servers := map[string]*config.Server{}
	for _, name := range selected {
		s := toServer(cands[name].f)
		if !s.IsStdio() {
			oauth, _ := probe(ctx, s.URL, s.EffectiveTransport(), s.Headers)
			if oauth {
				res.SkippedOAuth = append(res.SkippedOAuth, name)
				continue
			}
		}
		servers[name] = s
	}
	have, err := e.Keychain.List()
	if err != nil {
		return res, fmt.Errorf("list secrets: %w", err)
	}
	precheck(servers, have, res)
	if len(servers) == 0 {
		return res, nil
	}
	created, err := moveSecrets(e, servers, have, o.Rename, res)
	if err != nil {
		return res, errors.Join(err, rollbackSecrets(e, created, res))
	}
	names := slices.Sorted(maps.Keys(servers))
	var applyErr error
	res.Change, applyErr, err = importCommit(ctx, e, false, names, func(c *config.Config) error {
		for _, name := range names {
			if _, ok := c.Servers[name]; ok {
				return fmt.Errorf("server %q already exists; use: maple mcp set %s", name, name)
			}
			c.Servers[name] = servers[name]
		}
		return nil
	})
	if err != nil {
		// config.yaml was not saved, so nothing references the new secrets;
		// leaving them would turn a retry's names into _2 variants.
		return res, errors.Join(err, rollbackSecrets(e, created, res))
	}
	res.Imported = names
	if applyErr != nil {
		applyErr = fmt.Errorf("servers %s were imported, but updating agent configs failed: %w; run: maple sync", strings.Join(names, ", "), applyErr)
	}
	if o.RemoveOriginals {
		applyErr = errors.Join(applyErr, removeOriginals(e, names, cands, res))
	}
	return res, applyErr
}

// importCommit lets tests inject an agent update failure, which import cannot
// cause on its own: imported servers are never direct.
var importCommit = commit

func rollbackSecrets(e Env, created []string, res *ImportResult) error {
	// Nothing was saved that uses these names.
	res.SecretNames = map[string]string{}
	var errs []error
	for _, name := range created {
		if err := e.Keychain.Delete(name); err != nil {
			errs = append(errs, fmt.Errorf("remove secret %s: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

func agents(e Env) []agentconf.Agent {
	if len(e.Agents) > 0 {
		return e.Agents
	}
	return agentconf.All()
}

// findCandidates lists the user's own entries, first agent wins on a name.
func findCandidates(e Env, c *config.Config, res *ImportResult) (map[string]*candidate, []string, error) {
	applied, err := config.LoadApplied(e.Paths)
	if err != nil {
		return nil, nil, err
	}
	cands := map[string]*candidate{}
	var order []string
	for _, a := range agents(e) {
		data, err := os.ReadFile(a.Path(e.Paths))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		list, err := a.List(data)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", a.Path(e.Paths), err)
		}
		var owned map[string]string
		if aa := applied.Agents[a.ID()]; aa != nil {
			owned = aa.Entries
		}
		for _, f := range list {
			if _, ok := owned[f.Name]; ok || f.Name == agentconf.GatewayName {
				continue
			}
			if !config.IsServerName(f.Name) {
				res.Skipped[f.Name] = "invalid server name: rename it in " + a.ID() + " to 1-32 characters from A-Z a-z 0-9 _ -"
				continue
			}
			if _, ok := c.Servers[f.Name]; ok {
				res.Skipped[f.Name] = "a server with this name is already in config.yaml"
				continue
			}
			if prev, ok := cands[f.Name]; ok {
				// Only identical copies count as imported, so removing the
				// originals never deletes a variant maple did not take.
				if sameForeign(prev.f, f) {
					prev.agents = append(prev.agents, a)
				} else {
					res.Conflicts = append(res.Conflicts, fmt.Sprintf("%s differs between %s and %s; using the one from %s",
						f.Name, prev.agents[0].ID(), a.ID(), prev.agents[0].ID()))
				}
				continue
			}
			cands[f.Name] = &candidate{f: f, agents: []agentconf.Agent{a}}
			order = append(order, f.Name)
		}
	}
	return cands, order, nil
}

func sameForeign(a, b agentconf.Foreign) bool {
	return a.URL == b.URL && a.Transport == b.Transport && a.Command == b.Command &&
		slices.Equal(a.Args, b.Args) && maps.Equal(a.Headers, b.Headers) && maps.Equal(a.Env, b.Env)
}

func toServer(f agentconf.Foreign) *config.Server {
	s := &config.Server{URL: f.URL, Command: f.Command, Args: f.Args, Headers: maps.Clone(f.Headers), Env: maps.Clone(f.Env)}
	if f.Command != "" {
		s.URL = ""
		s.Headers = nil
	} else if f.Transport == config.TransportSSE {
		s.Transport = config.TransportSSE
	}
	return s
}

// precheck moves servers that config.yaml would reject into Skipped, so one
// bad entry does not block the rest. References already in the agent's file
// (e.g. environment variables) must exist as maple secrets; have lists the
// secrets maple has.
func precheck(servers map[string]*config.Server, have []string, res *ImportResult) {
	for _, name := range slices.Sorted(maps.Keys(servers)) {
		s := servers[name]
		one := &config.Config{Port: config.DefaultPort, Servers: map[string]*config.Server{name: s}}
		if err := one.Validate(); err != nil {
			res.Skipped[name] = err.Error()
			delete(servers, name)
			continue
		}
		for _, ref := range s.SecretRefs() {
			if !slices.Contains(have, ref) {
				res.Skipped[name] = "references secret " + ref + " which maple does not have; run: maple secret set " + ref + ", then import it again"
				delete(servers, name)
				break
			}
		}
	}
}

func isSecret(key, value string) bool {
	if value == "" || strings.Contains(value, "${") {
		return false
	}
	if strings.EqualFold(key, "Authorization") {
		return true
	}
	k := strings.ToUpper(key)
	for _, w := range []string{"TOKEN", "KEY", "SECRET", "PASSWORD"} {
		if strings.Contains(k, w) {
			return true
		}
	}
	return false
}

var nonSecretChar = regexp.MustCompile(`[^A-Z0-9_]`)

func defaultSecretName(server, key string) string {
	n := nonSecretChar.ReplaceAllString(strings.ToUpper(server+"_"+key), "_")
	if n[0] >= '0' && n[0] <= '9' {
		n = "_" + n
	}
	return n
}

type secretSite struct {
	m     map[string]string
	key   string
	value string
	// prefix stays in place, e.g. "Bearer " of an Authorization header, so
	// the keychain holds only the credential itself.
	prefix string
}

// moveSecrets returns the keychain items it created, not reused ones, so a
// failed import can remove them again. have lists the secrets maple has.
func moveSecrets(e Env, servers map[string]*config.Server, have []string, rename func(map[string]string) map[string]string, res *ImportResult) (created []string, err error) {
	sites := map[string]secretSite{}
	defaults := map[string]string{}
	for name, s := range servers {
		for field, m := range map[string]map[string]string{"headers": s.Headers, "env": s.Env} {
			for k, v := range m {
				if !isSecret(k, v) {
					continue
				}
				site := secretSite{m: m, key: k, value: v}
				if strings.EqualFold(k, "Authorization") && strings.HasPrefix(strings.ToLower(v), "bearer ") {
					site.prefix, site.value = v[:len("bearer ")], strings.TrimSpace(v[len("bearer "):])
				}
				if site.value == "" {
					continue
				}
				id := name + "." + field + "." + k
				sites[id] = site
				defaults[id] = defaultSecretName(name, k)
			}
		}
	}
	chosen := defaults
	if rename != nil && len(defaults) > 0 {
		chosen = rename(maps.Clone(defaults))
	}
	taken := map[string]string{}
	for _, id := range slices.Sorted(maps.Keys(sites)) {
		site := sites[id]
		base := chosen[id]
		if base == "" {
			base = defaults[id]
		}
		if !config.IsSecretName(base) {
			return created, fmt.Errorf("invalid secret name %q for %s: use A-Z, 0-9 and _, not starting with a digit", base, id)
		}
		name := base
		for i := 2; ; i++ {
			if v, ok := taken[name]; ok {
				if v == site.value {
					break
				}
			} else if !slices.Contains(have, name) {
				break
			} else if v, err := e.Keychain.Get(name); err == nil && v == site.value {
				break
			}
			name = base + "_" + strconv.Itoa(i)
		}
		if _, ok := taken[name]; !ok {
			taken[name] = site.value
			if !slices.Contains(have, name) {
				if err := SecretSet(e, name, site.value); err != nil {
					return created, err
				}
				created = append(created, name)
			}
		}
		site.m[site.key] = site.prefix + "${" + name + "}"
		res.SecretNames[id] = name
	}
	return created, nil
}

// probeBody is an initialize request; any answer but an OAuth challenge
// means the server takes the configured credentials. Marshalling these
// literals cannot fail.
var probeBody, _ = json.Marshal(map[string]any{
	"jsonrpc": "2.0", "id": 1, "method": "initialize",
	"params": map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]string{"name": "maple", "version": upstream.ClientVersion},
	},
})

// probe reports whether url wants OAuth. It talks HTTP directly because the
// SDK hides the status code; network errors are not OAuth. SSE servers are
// probed with the GET that opens their stream.
func probe(ctx context.Context, url, transport string, headers map[string]string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	method, accept, body := http.MethodPost, "application/json, text/event-stream", probeBody
	if transport == config.TransportSSE {
		method, accept, body = http.MethodGet, "text/event-stream", nil
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	for k, v := range headers {
		// Unresolved references would only be rejected for the wrong reason.
		if !strings.Contains(v, "${") {
			req.Header.Set(k, v)
		}
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", accept)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err
	}
	// The body may be an endless event stream; only the status matters.
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusUnauthorized && resp.Header.Get("WWW-Authenticate") != "", nil
}

// removeOriginals backs up every agent file before deleting the imported
// entries from the agents that had them.
func removeOriginals(e Env, names []string, cands map[string]*candidate, res *ImportResult) error {
	var dir string
	byAgent := map[string][]string{}
	for _, name := range names {
		for _, a := range cands[name].agents {
			byAgent[a.ID()] = append(byAgent[a.ID()], name)
		}
	}
	for _, a := range agents(e) {
		data, err := os.ReadFile(a.Path(e.Paths))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if dir == "" {
			if err := os.MkdirAll(e.Paths.BackupDir(), 0o700); err != nil {
				return err
			}
			// Unique per run: a timestamp alone repeats within a second, and
			// a second import would overwrite the only copy of the originals.
			if dir, err = os.MkdirTemp(e.Paths.BackupDir(), time.Now().Format("20060102-150405")+"-*"); err != nil {
				return err
			}
		}
		if err := writeNew(filepath.Join(dir, a.ID()+"-"+filepath.Base(a.Path(e.Paths))), data); err != nil {
			return fmt.Errorf("back up %s: %w", a.Path(e.Paths), err)
		}
	}
	res.Backup = dir
	var errs []error
	for _, a := range agents(e) {
		if n := byAgent[a.ID()]; len(n) > 0 {
			if err := agentconf.RemoveForeign(e.Paths, a, n); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", a.ID(), err))
			}
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("servers imported, but removing the originals failed (backup in %s): %w", dir, err)
	}
	return nil
}

// writeNew refuses to replace an existing file, so a backup is never lost.
func writeNew(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
