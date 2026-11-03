// Command maple is a local MCP gateway shared by coding agents.
package main

import (
	"fmt"
	"os"
)

// Overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := newRootCmd(version).Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
