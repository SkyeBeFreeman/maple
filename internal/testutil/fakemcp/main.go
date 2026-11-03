// Command fakemcp is a stdio MCP server used as a test double.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SkyeBeFreeman/maple/internal/testutil/fakemcp/tools"
)

func main() {
	var o tools.Options
	flag.StringVar(&o.Name, "name", "fakemcp", "server name")
	flag.StringVar(&o.Instructions, "instructions", "", "server instructions")
	flag.StringVar(&o.Description, "description", "", "server description")
	flag.IntVar(&o.Extra, "extra", 0, "number of extra tools t0..tN-1")
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
	if *exitOnStart >= 0 {
		os.Exit(*exitOnStart)
	}
	if err := tools.NewServer(o).Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
