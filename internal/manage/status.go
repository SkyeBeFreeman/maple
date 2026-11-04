package manage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"syscall"
	"time"

	"github.com/SkyeBeFreeman/maple/internal/config"
)

// ErrGatewayDown means nothing listens on the configured port.
var ErrGatewayDown = errors.New("gateway is not running; run: maple doctor")

const (
	noteDown  = "gateway is not running; run: maple doctor"
	noteStale = "gateway has not reloaded the new config yet; run: maple status"
)

// Status asks the running gateway for its state.
func Status(ctx context.Context, e Env) (*config.Status, error) {
	c, err := LoadConfig(e.Paths)
	if err != nil {
		return nil, err
	}
	return statusOf(ctx, e, c)
}

func statusOf(ctx context.Context, e Env, c *config.Config) (*config.Status, error) {
	token, err := loadToken(e.Paths)
	if err != nil {
		return nil, err
	}
	return fetchStatus(ctx, e, c.Port, token)
}

func fetchStatus(ctx context.Context, e Env, port int, token string) (*config.Status, error) {
	url := "http://" + config.LoopbackAddr(port) + "/status"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := e.httpClient().Do(req)
	if errors.Is(err, syscall.ECONNREFUSED) {
		return nil, fmt.Errorf("port %d: %w", port, ErrGatewayDown)
	}
	if err != nil {
		return nil, fmt.Errorf("query gateway status: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return nil, fmt.Errorf("gateway on port %d rejected the token (HTTP 401); run: maple doctor", port)
	default:
		return nil, fmt.Errorf("gateway on port %d answered HTTP %d; run: maple doctor", port, resp.StatusCode)
	}
	var st config.Status
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return nil, fmt.Errorf("decode gateway status: %w", err)
	}
	return &st, nil
}

// confirmReload polls the gateway on port until it reports the hash of raw.
// Failures only produce a note: the config change itself already succeeded.
func confirmReload(ctx context.Context, e Env, raw []byte, port int, token string) (bool, string) {
	if token == "" {
		// Without a token the gateway cannot have been installed or started.
		return false, noteDown
	}
	want := config.ContentHash(raw)
	ctx, cancel := context.WithTimeout(ctx, reloadTimeout)
	defer cancel()
	tick := time.NewTicker(reloadPoll)
	defer tick.Stop()
	note := noteStale
	for {
		st, err := fetchStatus(ctx, e, port, token)
		switch {
		case errors.Is(err, ErrGatewayDown):
			return false, noteDown
		case err == nil && st.ConfigHash == want:
			return true, ""
		case err != nil:
			// Once the deadline hits, the error says nothing new.
			if ctx.Err() == nil {
				note = err.Error()
			}
		case st.ReloadError != "":
			note = st.ReloadError
		}
		select {
		case <-ctx.Done():
			return false, note
		case <-tick.C:
		}
	}
}
