package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/SkyeBeFreeman/maple/internal/config"
	"github.com/SkyeBeFreeman/maple/internal/manage"
	"github.com/SkyeBeFreeman/maple/internal/manage/agentconf"
)

func newMCPCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "mcp", Short: "Manage upstream MCP servers"}
	cmd.AddCommand(newMCPListCmd(), newMCPShowCmd(), newMCPAddCmd(), newMCPSetCmd(), newMCPRmCmd(),
		newMCPEnableCmd(true), newMCPEnableCmd(false), newMCPTestCmd(), newMCPImportCmd())
	return cmd
}

// loadConfig reads config.yaml directly: list and show need no lock.
func loadConfig() (*config.Config, error) {
	e, err := newEnv()
	if err != nil {
		return nil, err
	}
	c, _, err := config.Load(e.Paths)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%s does not exist; run: maple install", e.Paths.Config())
	}
	return c, err
}

type namedServer struct {
	Name string `json:"name"`
	*config.Server
}

func newMCPListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List servers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := loadConfig()
			if err != nil {
				return err
			}
			out := []namedServer{}
			for _, name := range slices.Sorted(maps.Keys(c.Servers)) {
				out = append(out, namedServer{Name: name, Server: c.Servers[name]})
			}
			return emit(cmd, out, func(w *strings.Builder) {
				if len(out) == 0 {
					fmt.Fprintln(w, "no servers; add one with: maple mcp add")
				}
				for _, s := range out {
					fmt.Fprintf(w, "%-20s %s\n", s.Name, summary(s.Server))
				}
			})
		},
	}
}

func summary(s *config.Server) string {
	var b strings.Builder
	if s.IsStdio() {
		fmt.Fprintf(&b, "stdio/%s %s", s.EffectiveMode(), strings.Join(append([]string{s.Command}, s.Args...), " "))
	} else {
		fmt.Fprintf(&b, "%s %s", s.EffectiveTransport(), s.URL)
	}
	if !s.IsEnabled() {
		b.WriteString(" (disabled)")
	}
	if s.Description != "" {
		b.WriteString(" - " + s.Description)
	}
	return b.String()
}

func newMCPShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <name>",
		Short: "Show one server",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := loadConfig()
			if err != nil {
				return err
			}
			s, ok := c.Servers[args[0]]
			if !ok || s == nil {
				return fmt.Errorf("server %q not found; run: maple mcp list", args[0])
			}
			return emit(cmd, namedServer{Name: args[0], Server: s}, func(w *strings.Builder) { printServer(w, args[0], s) })
		},
	}
}

func printServer(w *strings.Builder, name string, s *config.Server) {
	fmt.Fprintf(w, "name: %s\nenabled: %t\n", name, s.IsEnabled())
	if s.IsStdio() {
		fmt.Fprintf(w, "mode: %s\ncommand: %s\n", s.EffectiveMode(), s.Command)
		if len(s.Args) > 0 {
			fmt.Fprintf(w, "args: %s\n", strings.Join(s.Args, " "))
		}
	} else {
		fmt.Fprintf(w, "url: %s\ntransport: %s\n", s.URL, s.EffectiveTransport())
	}
	// Values are ${NAME} references or plain config, never keychain secrets.
	for _, k := range slices.Sorted(maps.Keys(s.Headers)) {
		fmt.Fprintf(w, "header: %s=%s\n", k, s.Headers[k])
	}
	for _, k := range slices.Sorted(maps.Keys(s.Env)) {
		fmt.Fprintf(w, "env: %s=%s\n", k, s.Env[k])
	}
	if len(s.DisabledTools) > 0 {
		fmt.Fprintf(w, "disabled tools: %s\n", strings.Join(s.DisabledTools, ", "))
	}
	if s.Description != "" {
		fmt.Fprintf(w, "description: %s\n", s.Description)
	}
}

// serverFlags are the field flags shared by add and set.
type serverFlags struct {
	url, transport, mode, description string
	headers, env                      []string
}

func (f *serverFlags) register(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVar(&f.url, "url", "", "remote server URL")
	fl.StringVar(&f.transport, "transport", "", "remote transport: http or sse")
	fl.StringArrayVar(&f.headers, "header", nil, "HTTP header K=V, repeatable; use ${NAME} for secrets; on set, K= removes K")
	fl.StringVar(&f.mode, "mode", "", "stdio mode: shared, per-session or direct")
	fl.StringArrayVar(&f.env, "env", nil, "environment variable K=V, repeatable; use ${NAME} for secrets; on set, K= removes K")
	fl.StringVar(&f.description, "description", "", "description shown to the model")
}

// parseKV turns repeated K=V flags into a map. An empty value is kept; set
// uses it to delete a key.
func parseKV(flag string, items []string) (map[string]string, error) {
	if len(items) == 0 {
		return nil, nil
	}
	m := map[string]string{}
	for _, it := range items {
		k, v, ok := strings.Cut(it, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("--%s %q: want KEY=VALUE", flag, it)
		}
		m[k] = v
	}
	return m, nil
}

// mergeKV applies set onto m; an empty value deletes the key.
func mergeKV(m, set map[string]string) map[string]string {
	if len(set) == 0 {
		return m
	}
	if m == nil {
		m = map[string]string{}
	}
	for k, v := range set {
		if v == "" {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

// splitCommand separates the server name from a stdio command given after --.
func splitCommand(cmd *cobra.Command, args []string) (name string, command []string, err error) {
	switch dash := cmd.ArgsLenAtDash(); {
	case dash < 0 && len(args) == 1:
		return args[0], nil, nil
	case dash < 0:
		return "", nil, fmt.Errorf("put the command after --: maple mcp %s %s -- <command> [args...]", cmd.Name(), args[0])
	case dash != 1:
		return "", nil, fmt.Errorf("want exactly one server name before --")
	case len(args) == 1:
		return "", nil, fmt.Errorf("missing command after --")
	}
	return args[0], args[1:], nil
}

func newMCPAddCmd() *cobra.Command {
	var f serverFlags
	cmd := &cobra.Command{
		Use:   "add <name> (--url <url> | -- <command> [args...])",
		Short: "Add a remote or stdio server",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, command, err := splitCommand(cmd, args)
			if err != nil {
				return err
			}
			if (f.url == "") == (len(command) == 0) {
				return fmt.Errorf("give either --url or a command after --")
			}
			headers, err := parseKV("header", f.headers)
			if err != nil {
				return err
			}
			env, err := parseKV("env", f.env)
			if err != nil {
				return err
			}
			s := &config.Server{URL: f.url, Transport: f.transport, Headers: headers, Env: env, Mode: f.mode, Description: f.description}
			if len(command) > 0 {
				s.Command, s.Args = command[0], command[1:]
			}
			e, err := newEnv()
			if err != nil {
				return err
			}
			res, err := manage.Add(cmd.Context(), e, name, s)
			if err != nil {
				return err
			}
			return emit(cmd, res, func(w *strings.Builder) { printChange(w, "added "+name, res) })
		},
	}
	f.register(cmd)
	return cmd
}

func newMCPSetCmd() *cobra.Command {
	var (
		f               serverFlags
		disable, enable []string
		force           bool
	)
	cmd := &cobra.Command{
		Use:   "set <name> [flags] [-- <command> [args...]]",
		Short: "Change a server",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, command, err := splitCommand(cmd, args)
			if err != nil {
				return err
			}
			headers, err := parseKV("header", f.headers)
			if err != nil {
				return err
			}
			env, err := parseKV("env", f.env)
			if err != nil {
				return err
			}
			fl := cmd.Flags()
			if fl.Changed("url") && len(command) > 0 {
				return fmt.Errorf("give either --url or a command after --, not both")
			}
			edit := func(s *config.Server) error {
				if fl.Changed("url") {
					s.URL, s.Command, s.Args, s.Env, s.Mode = f.url, "", nil, nil, ""
				}
				if len(command) > 0 {
					s.Command, s.Args, s.URL, s.Transport, s.Headers = command[0], command[1:], "", "", nil
				}
				if fl.Changed("transport") {
					s.Transport = f.transport
				}
				if fl.Changed("mode") {
					s.Mode = f.mode
				}
				if fl.Changed("description") {
					s.Description = f.description
				}
				s.Headers = mergeKV(s.Headers, headers)
				s.Env = mergeKV(s.Env, env)
				s.DisabledTools = editTools(s.DisabledTools, disable, enable)
				return nil
			}
			e, err := newEnv()
			if err != nil {
				return err
			}
			res, err := manage.Set(cmd.Context(), e, name, edit, force)
			if err != nil {
				return err
			}
			return emit(cmd, res, func(w *strings.Builder) { printChange(w, "updated "+name, res) })
		},
	}
	f.register(cmd)
	cmd.Flags().StringArrayVar(&disable, "disable-tool", nil, "hide a tool of this server, repeatable")
	cmd.Flags().StringArrayVar(&enable, "enable-tool", nil, "show a hidden tool again, repeatable")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite agent entries edited by hand")
	return cmd
}

func editTools(cur, disable, enable []string) []string {
	out := slices.Clone(cur)
	for _, t := range disable {
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	out = slices.DeleteFunc(out, func(t string) bool { return slices.Contains(enable, t) })
	if len(out) == 0 {
		return nil
	}
	slices.Sort(out)
	return out
}

func newMCPRmCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "rm <name>",
		Short: "Remove a server",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := newEnv()
			if err != nil {
				return err
			}
			res, err := manage.Remove(cmd.Context(), e, args[0], force)
			if err != nil {
				return err
			}
			return emit(cmd, res, func(w *strings.Builder) { printChange(w, "removed "+args[0], res) })
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite agent entries edited by hand")
	return cmd
}

func newMCPEnableCmd(on bool) *cobra.Command {
	use, done := "disable", "disabled "
	if on {
		use, done = "enable", "enabled "
	}
	return &cobra.Command{
		Use:   use + " <name>",
		Short: strings.ToUpper(use[:1]) + use[1:] + " a server",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := newEnv()
			if err != nil {
				return err
			}
			res, err := manage.SetEnabled(cmd.Context(), e, args[0], on)
			if err != nil {
				return err
			}
			return emit(cmd, res, func(w *strings.Builder) { printChange(w, done+args[0], res) })
		},
	}
}

func newMCPTestCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "test <name>",
		Short: "Connect to a server directly and list its tools",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := newEnv()
			if err != nil {
				return err
			}
			res, err := manage.Test(cmd.Context(), e, args[0])
			if err != nil {
				return err
			}
			return emit(cmd, res, func(w *strings.Builder) {
				fmt.Fprintf(w, "%s: %d tools\n", res.Server, len(res.Tools))
				for _, t := range res.Tools {
					fmt.Fprintf(w, "  %s\n", t)
				}
			})
		},
	}
}

func newMCPImportCmd() *cobra.Command {
	var removeOriginals bool
	cmd := &cobra.Command{
		Use:   "import [<name>...]",
		Short: "Move servers from the agent configs into maple",
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := newEnv()
			if err != nil {
				return err
			}
			o := manage.ImportOptions{Names: args, RemoveOriginals: removeOriginals}
			if len(args) == 0 && !asJSON(cmd) && term.IsTerminal(int(os.Stdin.Fd())) {
				p := &prompter{in: bufio.NewReader(cmd.InOrStdin()), out: cmd.OutOrStdout()}
				listed, err := manage.Import(cmd.Context(), e, manage.ImportOptions{})
				if err != nil {
					return err
				}
				o.Names = p.choose(listed.Candidates)
				if len(o.Names) == 0 {
					_, _ = fmt.Fprintln(p.out, "nothing selected.")
					return nil
				}
				if !removeOriginals {
					o.RemoveOriginals = p.confirm("delete the originals from the agent configs (a backup is kept)?")
				}
				o.Rename = p.rename
			}
			res, err := manage.Import(cmd.Context(), e, o)
			if res != nil {
				if perr := emit(cmd, importView(res), func(w *strings.Builder) { printImport(w, res) }); perr != nil {
					return errors.Join(err, perr)
				}
			}
			return err
		},
	}
	cmd.Flags().BoolVar(&removeOriginals, "remove-originals", false, "delete the imported entries from the agent configs")
	return cmd
}

// candidateView keeps header and env values out of the output: imported
// configs often hold plaintext secrets, and an agent reads this output.
type candidateView struct {
	Name       string   `json:"name"`
	URL        string   `json:"url,omitempty"`
	Transport  string   `json:"transport,omitempty"`
	Command    string   `json:"command,omitempty"`
	Args       []string `json:"args,omitempty"`
	HeaderKeys []string `json:"header_keys,omitempty"`
	EnvKeys    []string `json:"env_keys,omitempty"`
}

type importOutput struct {
	*manage.ImportResult
	Candidates []candidateView `json:"candidates"`
}

func importView(res *manage.ImportResult) importOutput {
	out := importOutput{ImportResult: res, Candidates: []candidateView{}}
	for _, f := range res.Candidates {
		out.Candidates = append(out.Candidates, candidateView{
			Name: f.Name, URL: f.URL, Transport: f.Transport, Command: f.Command, Args: f.Args,
			HeaderKeys: slices.Sorted(maps.Keys(f.Headers)), EnvKeys: slices.Sorted(maps.Keys(f.Env)),
		})
	}
	return out
}

func printImport(w *strings.Builder, res *manage.ImportResult) {
	if len(res.Imported) == 0 && len(res.Candidates) > 0 {
		fmt.Fprintln(w, "servers found in the agent configs:")
		for _, f := range res.Candidates {
			fmt.Fprintf(w, "  %s\n", foreignSummary(f))
		}
		fmt.Fprintln(w, "import them with: maple mcp import <name>...")
	}
	if len(res.Imported) > 0 {
		fmt.Fprintf(w, "imported %s\n", strings.Join(res.Imported, ", "))
	}
	for _, k := range slices.Sorted(maps.Keys(res.SecretNames)) {
		fmt.Fprintf(w, "stored %s as secret %s\n", k, res.SecretNames[k])
	}
	for _, n := range res.SkippedOAuth {
		fmt.Fprintf(w, "skipped %s: needs OAuth, keep it in the agent configs\n", n)
	}
	for _, k := range slices.Sorted(maps.Keys(res.Skipped)) {
		fmt.Fprintf(w, "skipped %s: %s\n", k, res.Skipped[k])
	}
	for _, c := range res.Conflicts {
		fmt.Fprintf(w, "warning: %s\n", c)
	}
	if res.Backup != "" {
		fmt.Fprintf(w, "backup of the agent configs: %s\n", res.Backup)
	}
	if res.Change != nil {
		printChange(w, "", res.Change)
	}
}

func foreignSummary(f agentconf.Foreign) string {
	if f.Command != "" {
		return f.Name + ": " + strings.Join(append([]string{f.Command}, f.Args...), " ")
	}
	return f.Name + ": " + f.URL
}

// prompter is the interactive side of import; only used on a terminal.
type prompter struct {
	in  *bufio.Reader
	out io.Writer
}

func (p *prompter) ask(q string) string {
	_, _ = fmt.Fprint(p.out, q+" ")
	line, _ := p.in.ReadString('\n')
	return strings.TrimSpace(line)
}

func (p *prompter) choose(cands []agentconf.Foreign) []string {
	if len(cands) == 0 {
		return nil
	}
	for i, f := range cands {
		_, _ = fmt.Fprintf(p.out, "%2d) %s\n", i+1, foreignSummary(f))
	}
	return pickNumbers(p.ask("import which? (numbers separated by spaces or commas, 'all', empty for none)"), cands)
}

func pickNumbers(answer string, cands []agentconf.Foreign) []string {
	if strings.EqualFold(answer, "all") {
		var all []string
		for _, f := range cands {
			all = append(all, f.Name)
		}
		return all
	}
	var names []string
	for _, tok := range strings.FieldsFunc(answer, func(r rune) bool { return r == ',' || r == ' ' }) {
		i, err := strconv.Atoi(tok)
		if err != nil || i < 1 || i > len(cands) {
			continue
		}
		if n := cands[i-1].Name; !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	return names
}

func (p *prompter) confirm(q string) bool {
	a := strings.ToLower(p.ask(q + " [y/N]"))
	return a == "y" || a == "yes"
}

func (p *prompter) rename(defaults map[string]string) map[string]string {
	out := maps.Clone(defaults)
	for _, k := range slices.Sorted(maps.Keys(defaults)) {
		if a := p.ask(fmt.Sprintf("secret name for %s [%s]:", k, defaults[k])); a != "" {
			out[k] = a
		}
	}
	return out
}
