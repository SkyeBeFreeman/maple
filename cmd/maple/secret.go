package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/SkyeBeFreeman/maple/internal/manage"
)

func newSecretCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "secret", Short: "Manage secrets referenced as ${NAME}"}
	cmd.AddCommand(newSecretSetCmd(), newSecretListCmd(), newSecretRmCmd())
	return cmd
}

func newSecretSetCmd() *cobra.Command {
	var fromStdin bool
	cmd := &cobra.Command{
		Use:   "set <NAME>",
		Short: "Store a secret; the value is read from the terminal or stdin, never from arguments",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			value, err := readSecret(cmd, name, fromStdin)
			if err != nil {
				return err
			}
			e, err := newEnv()
			if err != nil {
				return err
			}
			if err := manage.SecretSet(e, name, value); err != nil {
				return err
			}
			return emit(cmd, map[string]string{"set": name}, func(w *strings.Builder) {
				fmt.Fprintf(w, "stored secret %s\n", name)
			})
		},
	}
	cmd.Flags().BoolVar(&fromStdin, "stdin", false, "read the value from standard input")
	return cmd
}

func readSecret(cmd *cobra.Command, name string, fromStdin bool) (string, error) {
	if fromStdin {
		line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		if err != nil && err != io.EOF {
			return "", fmt.Errorf("read secret from stdin: %w", err)
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	fd := int(os.Stdin.Fd())
	// An agent's shell is not a terminal; the value must come from the user.
	if !term.IsTerminal(fd) {
		return "", fmt.Errorf("stdin is not a terminal; run maple secret set %s in your own terminal, or pipe the value with --stdin", name)
	}
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "value for %s: ", name)
	b, err := term.ReadPassword(fd)
	_, _ = fmt.Fprintln(cmd.ErrOrStderr())
	if err != nil {
		return "", fmt.Errorf("read secret: %w", err)
	}
	return string(b), nil
}

func newSecretListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List secret names (never values)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := newEnv()
			if err != nil {
				return err
			}
			names, err := manage.SecretList(e)
			if err != nil {
				return err
			}
			if names == nil {
				names = []string{}
			}
			return emit(cmd, names, func(w *strings.Builder) {
				for _, n := range names {
					fmt.Fprintln(w, n)
				}
			})
		},
	}
}

func newSecretRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <NAME>",
		Short: "Delete a secret",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := newEnv()
			if err != nil {
				return err
			}
			if err := manage.SecretRemove(e, args[0]); err != nil {
				return err
			}
			return emit(cmd, map[string]string{"removed": args[0]}, func(w *strings.Builder) {
				fmt.Fprintf(w, "removed secret %s\n", args[0])
			})
		},
	}
}
