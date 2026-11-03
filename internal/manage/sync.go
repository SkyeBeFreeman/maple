package manage

import (
	"context"
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
