package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/SkyeBeFreeman/maple/internal/config"
	"github.com/SkyeBeFreeman/maple/internal/manage"
	"github.com/SkyeBeFreeman/maple/internal/platform/keychain"
)

func newRootCmd(version string) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "maple",
		Short:   "Local MCP gateway shared by coding agents",
		Version: version,
		// main prints the error once; usage would bury it.
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.PersistentFlags().Bool("json", false, "print output as JSON")
	cmd.AddCommand(
		newInstallCmd(), newUninstallCmd(), newServeCmd(version), newStatusCmd(),
		newDoctorCmd(), newSyncCmd(), newRunCmd(), newMCPCmd(), newSecretCmd(),
	)
	return cmd
}

// newEnv is the only place that looks at HOME and the executable path, so
// every other package derives paths from config.Paths.
func newEnv() (manage.Env, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return manage.Env{}, err
	}
	exe, err := os.Executable()
	if err != nil {
		return manage.Env{}, err
	}
	bin, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return manage.Env{}, err
	}
	p := config.Paths{Home: home}
	return manage.Env{Paths: p, Keychain: newKeychain(p, os.Stderr), MapleBin: bin, Shell: os.Getenv("SHELL")}, nil
}

// MAPLE_KEYCHAIN=file lets the end-to-end test run without the real Keychain.
// It is loud because a user who sets it by mistake keeps secrets in plaintext.
func newKeychain(p config.Paths, stderr io.Writer) keychain.Store {
	if os.Getenv("MAPLE_KEYCHAIN") == "file" {
		_, _ = fmt.Fprintln(stderr, "warning: MAPLE_KEYCHAIN=file stores secrets in plaintext; for tests only")
		return keychain.NewFile(filepath.Join(p.MapleDir(), "test-secrets.json"))
	}
	return keychain.New("maple")
}
