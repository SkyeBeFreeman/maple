package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/SkyeBeFreeman/maple/internal/config"
	"github.com/SkyeBeFreeman/maple/internal/gateway"
	"github.com/SkyeBeFreeman/maple/internal/manage"
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
			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
			defer stop()
			return serve(ctx, e, version)
		},
	}
}

func serve(ctx context.Context, e manage.Env, version string) error {
	raw, err := manage.ReadConfig(e.Paths)
	if err != nil {
		return err
	}
	// An invalid config must not stop the gateway: launchd would restart it
	// in a loop and maple status could not show the error. The gateway's own
	// load reports it in /status.
	port := config.PeekPort(raw)
	// A busy port fails the process so launchd retries later.
	addr := config.LoopbackAddr(port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w; run: maple doctor", addr, err)
	}
	opts := gateway.DefaultOptions()
	opts.Version = version
	opts.Shell = e.Shell
	opts.Port = port
	return gateway.New(e.Paths, e.Keychain, opts).Serve(ctx, ln)
}
