package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/SkyeBeFreeman/maple/internal/config"
	"github.com/SkyeBeFreeman/maple/internal/gateway"
)

func newServeCmd(version string) *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run the gateway (started by launchd)",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			e, err := newEnv()
			if err != nil {
				return err
			}
			c, _, err := config.Load(e.Paths)
			if errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("%s does not exist; run: maple install", e.Paths.Config())
			}
			if err != nil {
				return err
			}
			port := c.Port
			if port == 0 {
				port = config.DefaultPort
			}
			// A busy port fails the process so launchd retries later.
			ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
			if err != nil {
				return fmt.Errorf("listen on 127.0.0.1:%d: %w; run: maple doctor", port, err)
			}
			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
			defer stop()
			opts := gateway.DefaultOptions()
			opts.Version = version
			opts.Shell = e.Shell
			return gateway.New(e.Paths, e.Keychain, opts).Serve(ctx, ln)
		},
	}
}
