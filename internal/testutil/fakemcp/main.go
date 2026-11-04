// Command fakemcp is a stdio MCP server used as a test double.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SkyeBeFreeman/maple/internal/testutil/fakemcp/tools"
)

func main() {
	var o tools.Options
	flag.StringVar(&o.Name, "name", "fakemcp", "server name")
	flag.StringVar(&o.Instructions, "instructions", "", "server instructions")
	flag.StringVar(&o.Description, "description", "", "server description")
	flag.IntVar(&o.Extra, "extra", 0, "number of extra tools t0..tN-1")
	extraFile := flag.String("extra-file", "", "read the number of extra tools from this file at start, replacing -extra; a missing file means 0")
	flag.BoolVar(&o.BadSchema, "bad-schema", false, "add tool bad with inputSchema {}")
	startLog := flag.String("start-log", "", "append pid to this file at start")
	stderrText := flag.String("stderr", "", "write this text to stderr at start")
	exitOnStart := flag.Int("exit-on-start", -1, "exit with this code right after start")
	flag.Parse()

	if *stderrText != "" {
		_, _ = fmt.Fprintln(os.Stderr, *stderrText)
	}
	if *startLog != "" {
		f, err := os.OpenFile(*startLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
		_ = f.Close()
	}
	if *extraFile != "" {
		n, err := readExtra(*extraFile)
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		o.Extra = n
	}
	if *exitOnStart >= 0 {
		os.Exit(*exitOnStart)
	}
	if err := tools.NewServer(o).Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// readExtra lets a test change the tool set between starts of one config.
func readExtra(path string) (int, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0, fmt.Errorf("extra-file %s: %w", path, err)
	}
	return n, nil
}
