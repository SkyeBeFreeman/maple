// Package manage implements the CLI's use cases. Commands parse flags and
// call these functions; every error is phrased to be printed as is.
package manage

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
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

// load reads config.yaml, turning a missing file into an actionable error.
func load(p config.Paths) (*config.Config, []byte, error) {
	c, raw, err := config.Load(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, fmt.Errorf("%s does not exist; run: maple install", p.Config())
	}
	if err != nil {
		return nil, raw, err
	}
	return c, raw, nil
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
