package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/SkyeBeFreeman/maple/internal/config"
	"github.com/SkyeBeFreeman/maple/internal/manage"
	"github.com/SkyeBeFreeman/maple/internal/manage/agentconf"
)

func newInstallCmd() *cobra.Command {
	var o manage.InstallOptions
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Set up maple: config, token, LaunchAgent, agent entries and skill",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := newEnv()
			if err != nil {
				return err
			}
			res, err := manage.Install(cmd.Context(), e, o)
			if err != nil {
				return err
			}
			return emit(cmd, res, func(w *strings.Builder) {
				for _, p := range res.Created {
					fmt.Fprintf(w, "created %s\n", p)
				}
				printAgents(w, res.Agents)
				fmt.Fprintln(w, "maple is installed; restart your agents to pick it up.")
			})
		},
	}
	cmd.Flags().IntVar(&o.Port, "port", 0, "gateway port for a new config.yaml (default 47321)")
	cmd.Flags().BoolVar(&o.NoLaunchd, "no-launchd", false, "do not install or load the LaunchAgent")
	return cmd
}

func newUninstallCmd() *cobra.Command {
	var o manage.UninstallOptions
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the LaunchAgent, maple's agent entries and skill",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := newEnv()
			if err != nil {
				return err
			}
			if err := manage.Uninstall(cmd.Context(), e, o); err != nil {
				return err
			}
			return emit(cmd, map[string]any{"uninstalled": true, "purged": o.Purge}, func(w *strings.Builder) {
				fmt.Fprintln(w, "maple is uninstalled.")
			})
		},
	}
	cmd.Flags().BoolVar(&o.Purge, "purge", false, "also delete ~/.maple and every stored secret")
	cmd.Flags().BoolVar(&o.NoLaunchd, "no-launchd", false, "do not unload the LaunchAgent")
	return cmd
}

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show whether the gateway runs and the state of each server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := newEnv()
			if err != nil {
				return err
			}
			st, err := manage.Status(cmd.Context(), e)
			if err != nil {
				return err
			}
			return emit(cmd, st, func(w *strings.Builder) { printStatus(w, st) })
		},
	}
}

func printStatus(w *strings.Builder, st *config.Status) {
	fmt.Fprintf(w, "gateway %s running, config %s\n", st.Version, st.ConfigHash)
	if st.ReloadError != "" {
		fmt.Fprintf(w, "reload error: %s\n", st.ReloadError)
	}
	for _, s := range st.Servers {
		line := fmt.Sprintf("  %-20s %-10s tools=%d", s.Name, s.State, s.Tools)
		if s.PID != 0 {
			line += fmt.Sprintf(" pid=%d", s.PID)
		}
		if s.LastError != "" {
			line += " error: " + s.LastError
		}
		fmt.Fprintln(w, line)
	}
}

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check the installation and suggest fixes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := newEnv()
			if err != nil {
				return err
			}
			checks := manage.Doctor(cmd.Context(), e)
			failed := 0
			for _, c := range checks {
				if !c.OK {
					failed++
				}
			}
			if err := emit(cmd, checks, func(w *strings.Builder) {
				for _, c := range checks {
					mark := "ok  "
					if !c.OK {
						mark = "FAIL"
					}
					fmt.Fprintf(w, "%s %s: %s\n", mark, c.Name, c.Detail)
					if c.Fix != "" {
						fmt.Fprintf(w, "     fix: %s\n", c.Fix)
					}
				}
			}); err != nil {
				return err
			}
			if failed > 0 {
				return fmt.Errorf("%d check(s) failed", failed)
			}
			return nil
		},
	}
}

func newSyncCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Rewrite maple's entries in the agent configs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e, err := newEnv()
			if err != nil {
				return err
			}
			res, err := manage.Sync(cmd.Context(), e, force)
			if err != nil {
				return err
			}
			return emit(cmd, res, func(w *strings.Builder) {
				printAgents(w, res)
				fmt.Fprintln(w, "agent configs are in sync.")
			})
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite entries edited by hand")
	return cmd
}

func newRunCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run <name>",
		Short: "Start a direct-mode server (called by the agents)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := newEnv()
			if err != nil {
				return err
			}
			return manage.Run(cmd.Context(), e, args[0])
		},
	}
}

func printAgents(w *strings.Builder, rs []agentconf.Result) {
	for _, r := range rs {
		switch {
		case r.Missing:
			fmt.Fprintf(w, "%s: %s not found, skipped\n", r.Agent, r.Path)
		case len(r.Written)+len(r.Removed) > 0:
			fmt.Fprintf(w, "%s: wrote %s removed %s\n", r.Agent, list(r.Written), list(r.Removed))
		}
		for _, s := range r.Skipped {
			fmt.Fprintf(w, "%s: left %s alone: %s\n", r.Agent, s.Name, s.Reason)
		}
	}
}

func list(s []string) string {
	if len(s) == 0 {
		return "-"
	}
	return strings.Join(s, ",")
}

func printChange(w *strings.Builder, what string, res *manage.ChangeResult) {
	fmt.Fprintln(w, what)
	if res == nil {
		return
	}
	printAgents(w, res.Agents)
	if res.Reloaded {
		fmt.Fprintln(w, "gateway reloaded.")
	} else if res.Note != "" {
		fmt.Fprintf(w, "note: %s\n", res.Note)
	}
}
