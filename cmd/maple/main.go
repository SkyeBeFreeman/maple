// Command maple is a local MCP gateway shared by coding agents.
package main

import "os"

// Overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	// cobra has already printed the error; only the exit code is left to set.
	if err := newRootCmd(version).Execute(); err != nil {
		os.Exit(1)
	}
}
