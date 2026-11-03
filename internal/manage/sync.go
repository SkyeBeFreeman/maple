package manage

import (
	"context"
	"errors"
	"fmt"

	"github.com/SkyeBeFreeman/maple/internal/manage/agentconf"
)

// Sync rewrites maple's entries in the agents from config.yaml, the token
// and MapleBin, e.g. after a hand edit, a port change or moving the binary.
func Sync(ctx context.Context, e Env, force bool) ([]agentconf.Result, error) {
	unlock, err := lockConfig(ctx, e)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unlock() }()
	c, _, err := load(e.Paths)
	if err != nil {
		return nil, err
	}
	token, err := loadToken(e.Paths)
	if err != nil {
		return nil, err
	}
	res, err := apply(e, c, token, force)
	if err != nil {
		return res, fmt.Errorf("update agent configs: %w", err)
	}
	return res, nil
}

// SyncNote explains a gateway that does not answer on the configured port.
// After a port change it is expected: the gateway exits when it sees the new
// port and launchd starts it again there, which can take a few seconds.
func SyncNote(ctx context.Context, e Env) string {
	c, _, err := load(e.Paths)
	if err != nil {
		return ""
	}
	token, err := loadToken(e.Paths)
	if err != nil {
		return ""
	}
	if _, err := fetchStatus(ctx, e, c.Port, token); errors.Is(err, ErrGatewayDown) {
		return fmt.Sprintf("gateway is not answering on port %d yet; after a port change it restarts on the new port within seconds; run: maple status", c.Port)
	}
	return ""
}
