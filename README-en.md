# maple

[中文](README.md) | English

maple is a local MCP gateway for coding agents. It manages your MCP server configuration in one place and forwards MCP requests from Claude Code, Codex, and CodeBuddy to those servers.

> **Status:** the first version is usable. macOS only for now.

## Goals

- **Managed in one place.** Add or change an MCP server once in maple, and every agent sees the same set after a restart.
- **One forwarding endpoint.** Each agent connects to a single gateway on `127.0.0.1`, which forwards requests to the upstream servers. All tools are exposed as `<server>__<tool>`.
- **Shared stdio servers.** By default a stdio server runs once and is shared by all agents, instead of starting one process per agent session.
- **No plaintext secrets in agent configs.** Agent configs refer to secrets by name instead of storing their values.
- **Managed from the command line.** Use the `maple` command to add, change, list, and remove servers. Agents can run it from their own shell too.

## Installation

Requires macOS and Go 1.25 or later. maple uses cgo to access the Keychain, so building needs a C compiler (Xcode Command Line Tools).

```bash
go install github.com/SkyeBeFreeman/maple/cmd/maple@latest
maple install
```

`maple install` is safe to run again; each run only adds what is missing:

1. Creates `~/.maple/` with a default config and the token used to reach the gateway. The default port is `47321`; use `--port` if it is taken.
2. Registers and starts a LaunchAgent so launchd keeps the gateway running.
3. Writes one gateway entry named `maple` into the Claude Code, Codex, and CodeBuddy configs.

Restart your agents afterwards. To remove maple, run `maple uninstall`; add `--purge` to also delete `~/.maple/` and every stored secret.

## Common commands

```bash
# Store a secret: the value is read from the terminal (not echoed), never from arguments
maple secret set DEMO_TOKEN

# Add a remote server; refer to secrets as ${NAME}, single-quoted so the shell leaves them alone
maple mcp add demo --url https://example.com/mcp --header 'Authorization=Bearer ${DEMO_TOKEN}'

# Add a stdio server; by default all agents share one process
maple mcp add demo-local --env 'DEMO_TOKEN=${DEMO_TOKEN}' -- npx -y demo-mcp-server

maple mcp list     # list servers
maple mcp rm demo  # remove a server
maple status       # whether the gateway runs, and the state of each server
maple doctor       # check the installation and suggest fixes
maple mcp import   # move servers already in the agent configs into maple
```

- Run in a terminal, `maple mcp import` lists the servers it can import for you to pick, and asks whether to delete the original entries from the agent configs (a backup is taken first). When not interactive, name the servers to import, and add `--remove-originals` to delete the originals. Plaintext secrets in the original entries are stored in the Keychain and replaced with `${NAME}` references. Remote servers that need OAuth are skipped and stay in the agent configs.
- For stdio servers, `--mode` picks how they run: `shared` (default, one process for all agents), `per-session` (one process per agent session), or `direct` (bypasses the gateway; the agent starts it through `maple run <name>`).
- Every command accepts `--json` for output that agents can parse. See `maple --help` for full usage.

## How it works

launchd keeps the gateway `maple serve` running, listening only on `127.0.0.1`. Server configuration lives in `~/.maple/config.yaml` and secrets live in the macOS Keychain; the gateway substitutes the real values for `${NAME}` only when it connects to an upstream server. The `maple` command only edits that config. The gateway reloads when the file changes and tells connected agents that the tool list changed; if an agent does not refresh, restart it. All gateway servers share a single `maple` entry in each agent config (`direct` servers get their own entries), for example in Claude Code:

```json
{
  "mcpServers": {
    "maple": {
      "type": "http",
      "url": "http://127.0.0.1:47321/mcp",
      "headers": { "Authorization": "Bearer <token>" }
    }
  }
}
```

Codex:

```toml
[mcp_servers.maple]
url = "http://127.0.0.1:47321/mcp"
http_headers = { Authorization = "Bearer <token>" }
```

The token is written in plaintext: it only grants access to the local gateway and exists to block requests from browsers and other users on the machine. Upstream server secrets never appear in agent configs.

## License

[MIT](LICENSE)
