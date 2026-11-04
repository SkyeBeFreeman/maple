// Package manage implements the CLI's use cases. Commands parse flags and
// call these functions; every error is phrased to be printed as is.
package manage

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"time"

	"github.com/SkyeBeFreeman/maple/internal/config"
	"github.com/SkyeBeFreeman/maple/internal/manage/agentconf"
	"github.com/SkyeBeFreeman/maple/internal/platform/keychain"
)

// Env is what the use cases need from the outside world. Empty Agents means
// all agents; nil HTTP uses a client with a short timeout; Shell is the login
// shell whose PATH stdio servers run with.
type Env struct {
	Paths    config.Paths
	Keychain keychain.Store
	MapleBin string
	Agents   []agentconf.Agent
	HTTP     *http.Client
	Shell    string
}

// The gateway debounces config changes for 200ms and then restarts what
// changed; 3s covers that with room to spare without making the CLI hang.
var (
	reloadTimeout = 3 * time.Second
	reloadPoll    = 100 * time.Millisecond
)

const statusRequestTimeout = 2 * time.Second

func (e Env) httpClient() *http.Client {
	if e.HTTP != nil {
		return e.HTTP
	}
	return &http.Client{Timeout: statusRequestTimeout}
}

// ReadConfig reads config.yaml without parsing it, turning a missing file
// into an actionable error.
func ReadConfig(p config.Paths) ([]byte, error) {
	raw, err := os.ReadFile(p.Config())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%s does not exist; run: maple install", p.Config())
	}
	return raw, err
}

// LoadConfig is ReadConfig followed by config.Parse.
func LoadConfig(p config.Paths) (*config.Config, error) {
	raw, err := ReadConfig(p)
	if err != nil {
		return nil, err
	}
	return config.Parse(raw)
}

// Lookup returns the named server of c.
func Lookup(c *config.Config, name string) (*config.Server, error) {
	s, ok := c.Servers[name]
	if !ok || s == nil {
		return nil, fmt.Errorf("server %q not found; run: maple mcp list", name)
	}
	return s, nil
}

// expandServer resolves s's secret references, phrasing failures for the
// user of the named server.
func expandServer(e Env, name string, s *config.Server) (*config.Server, []string, error) {
	exp, secrets, err := s.Expand(e.Keychain.Get)
	if err != nil {
		var se *config.SecretError
		if errors.As(err, &se) {
			return nil, nil, secretErr(name, se.Name, se.Err)
		}
		return nil, nil, fmt.Errorf("maple: %s: %w", name, err)
	}
	return exp, secrets, nil
}

func loadToken(p config.Paths) (string, error) {
	tok, err := config.LoadToken(p)
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("%s does not exist; run: maple install", p.Token())
	}
	if err != nil {
		return "", fmt.Errorf("read gateway token: %w; run: maple install", err)
	}
	return tok, nil
}
