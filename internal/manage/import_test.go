package manage

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/SkyeBeFreeman/maple/internal/config"
	"github.com/SkyeBeFreeman/maple/internal/manage/agentconf"
	"github.com/SkyeBeFreeman/maple/internal/testutil"
)

func importEnv(t *testing.T) Env {
	t.Helper()
	e := setup(t, "")
	remote := testutil.HTTPServer(t, "http", [2]string{"Authorization", "Bearer abc"}, testutil.FakeOptions{})
	oauth := testutil.OAuthServer(t)
	bin := testutil.BuildFakeMCP(t)
	cb := map[string]any{"mcpServers": map[string]any{
		"remote": map[string]any{"url": remote.URL, "transportType": "streamable-http", "headers": map[string]string{"Authorization": "Bearer abc"}},
		"local":  map[string]any{"command": bin, "env": map[string]string{"API_KEY": "xyz", "PLAIN": "1"}},
		"oauth":  map[string]any{"type": "http", "url": oauth.URL},
		"bad.nm": map[string]any{"command": "x"},
	}}
	data, _ := json.MarshalIndent(cb, "", "  ")
	writeFile(t, e.Paths.CodeBuddyJSON(), string(data))
	return e
}

func TestImport(t *testing.T) {
	for _, remove := range []bool{false, true} {
		e := importEnv(t)
		original := readFile(t, e.Paths.CodeBuddyJSON())
		res, err := Import(context.Background(), e, ImportOptions{Names: []string{"remote", "local", "oauth"}, RemoveOriginals: remove})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(res.Imported, []string{"local", "remote"}) || !slices.Equal(res.SkippedOAuth, []string{"oauth"}) {
			t.Fatalf("res = %+v", res)
		}
		if _, ok := res.Skipped["bad.nm"]; !ok {
			t.Errorf("Skipped = %v", res.Skipped)
		}
		c := loadConfig(t, e)
		auth := res.SecretNames["remote.headers.Authorization"]
		key := res.SecretNames["local.env.API_KEY"]
		if auth != "REMOTE_AUTHORIZATION" || key != "LOCAL_API_KEY" {
			t.Fatalf("SecretNames = %v", res.SecretNames)
		}
		if got := c.Servers["remote"].Headers["Authorization"]; got != "Bearer ${"+auth+"}" {
			t.Errorf("header = %q", got)
		}
		if got := c.Servers["local"].Env; got["API_KEY"] != "${"+key+"}" || got["PLAIN"] != "1" {
			t.Errorf("env = %v", got)
		}
		if v, _ := e.Keychain.Get(auth); v != "abc" {
			t.Errorf("%s = %q", auth, v)
		}
		if v, _ := e.Keychain.Get(key); v != "xyz" {
			t.Errorf("%s = %q", key, v)
		}
		if _, ok := c.Servers["oauth"]; ok {
			t.Error("oauth imported")
		}
		list, _ := agentconf.CodeBuddy.List([]byte(readFile(t, e.Paths.CodeBuddyJSON())))
		var names []string
		for _, f := range list {
			names = append(names, f.Name)
		}
		if remove {
			if slices.Contains(names, "remote") || slices.Contains(names, "local") || !slices.Contains(names, "oauth") {
				t.Errorf("after remove: %v", names)
			}
			if got := readFile(t, filepath.Join(res.Backup, "codebuddy-mcp.json")); got != original {
				t.Error("backup differs from original")
			}
		} else {
			if !slices.Contains(names, "remote") || res.Backup != "" {
				t.Errorf("kept: %v backup %q", names, res.Backup)
			}
		}
	}
}

func TestImportSecretNameCollision(t *testing.T) {
	e := importEnv(t)
	_ = e.Keychain.Set("LOCAL_API_KEY", "other")
	res, err := Import(context.Background(), e, ImportOptions{Names: []string{"local"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.SecretNames["local.env.API_KEY"]; got != "LOCAL_API_KEY_2" {
		t.Errorf("name = %q", got)
	}
}

func TestImportNonInteractiveListsOnly(t *testing.T) {
	e := importEnv(t)
	before := readFile(t, e.Paths.Config())
	res, err := Import(context.Background(), e, ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range res.Candidates {
		names = append(names, f.Name)
	}
	// goland is in both claude and codebuddy samples with different content.
	for _, want := range []string{"goland", "node_repl", "remote", "local", "oauth"} {
		if !slices.Contains(names, want) {
			t.Errorf("candidates %v lack %s", names, want)
		}
	}
	if len(res.Imported) != 0 || readFile(t, e.Paths.Config()) != before {
		t.Error("imported without selection")
	}
	if _, err := os.Stat(e.Paths.Applied()); err == nil {
		t.Error("applied written")
	}
}

func TestImportConflictKeepsOtherVariant(t *testing.T) {
	e := setup(t, "")
	writeFile(t, e.Paths.ClaudeJSON(), `{"mcpServers": {"dup": {"command": "a"}}}`)
	writeFile(t, e.Paths.CodeBuddyJSON(), `{"mcpServers": {"dup": {"command": "b"}}}`)
	res, err := Import(context.Background(), e, ImportOptions{Names: []string{"dup"}, RemoveOriginals: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Conflicts) != 1 || loadConfig(t, e).Servers["dup"].Command != "a" {
		t.Fatalf("res = %+v", res)
	}
	if _, ok := entryArgs(t, e, agentconf.Claude, "dup"); ok {
		t.Error("imported claude entry kept")
	}
	if _, ok := entryArgs(t, e, agentconf.CodeBuddy, "dup"); !ok {
		t.Error("different codebuddy entry removed")
	}
}

func TestProbeOAuth(t *testing.T) {
	ctx := context.Background()
	oauth := testutil.OAuthServer(t)
	plain := testutil.HTTPServer(t, "sse", [2]string{"Authorization", "Bearer x"}, testutil.FakeOptions{})
	for _, tc := range []struct {
		url, transport string
		headers        map[string]string
		want           bool
	}{
		{oauth.URL, "http", nil, true},
		{oauth.URL, "sse", nil, true},
		{plain.URL, "sse", map[string]string{"Authorization": "Bearer x"}, false},
		// A 401 without a challenge is a bad key, not OAuth.
		{plain.URL, "sse", nil, false},
	} {
		got, err := probe(ctx, tc.url, tc.transport, tc.headers)
		if err != nil || got != tc.want {
			t.Errorf("probe(%s, %s) = %v, %v", tc.url, tc.transport, got, err)
		}
	}
	if got, err := probe(ctx, "http://127.0.0.1:1/mcp", "http", nil); got || err == nil {
		t.Errorf("unreachable = %v, %v", got, err)
	}
}

func TestImportRollsBackSecretsWhenWriteFails(t *testing.T) {
	e := importEnv(t)
	before := readFile(t, e.Paths.Config())
	// Holding the lock makes the config write fail after the secrets were
	// stored, as a lock timeout would.
	unlock, err := config.Lock(context.Background(), e.Paths)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unlock() }()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := Import(ctx, e, ImportOptions{Names: []string{"local"}}); err == nil {
		t.Fatal("no error")
	}
	if names, _ := e.Keychain.List(); len(names) != 0 {
		t.Errorf("keychain = %v", names)
	}
	if readFile(t, e.Paths.Config()) != before {
		t.Error("config changed")
	}
}

func TestImportReportsAgentFailureAfterSave(t *testing.T) {
	e := importEnv(t)
	boom := errors.New("boom")
	prev := importCommit
	importCommit = func(ctx context.Context, e Env, force bool, names []string, mutate func(*config.Config) error) (*ChangeResult, error, error) {
		res, _, err := prev(ctx, e, force, names, mutate)
		return res, boom, err
	}
	t.Cleanup(func() { importCommit = prev })
	res, err := Import(context.Background(), e, ImportOptions{Names: []string{"local"}, RemoveOriginals: true})
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "imported, but updating agent configs failed") {
		t.Fatalf("err = %v", err)
	}
	if !slices.Equal(res.Imported, []string{"local"}) || res.Backup == "" {
		t.Errorf("res = %+v", res)
	}
	if v, _ := e.Keychain.Get("LOCAL_API_KEY"); v != "xyz" {
		t.Errorf("secret rolled back: %q", v)
	}
	if _, ok := entryArgs(t, e, agentconf.CodeBuddy, "local"); ok {
		t.Error("original kept")
	}
}
