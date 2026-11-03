---
name: maple
description: Manage MCP servers shared by Claude Code, Codex and CodeBuddy through the maple gateway. Use when adding, changing, removing or debugging MCP servers.
---

# maple

maple is a local MCP gateway. Claude Code, Codex and CodeBuddy all reach the
user's MCP servers through one `maple` entry, so a change made once applies to
every agent.

## Rules

- To add, change, remove or debug MCP servers, use the `maple` CLI instead of
  editing agent config files. Prefer `--json` output when you parse results.
- Secrets are set by the user only. Ask the user to run `maple secret set NAME`
  in their own terminal (your shell is not an interactive terminal), then
  reference the secret as `${NAME}` in headers or env. Never put a secret value
  in a command argument or in any file.
- After a change, check `maple status --json`. If the tool list did not
  refresh, ask the user to restart the agent.

## Common commands

- `maple mcp list --json` / `maple mcp show NAME --json`: list or inspect servers
- `maple mcp add NAME --url URL [--header 'Authorization=Bearer ${NAME_TOKEN}']`
- `maple mcp add NAME [--env KEY='${SECRET}'] -- COMMAND [ARGS...]`
- `maple mcp set NAME ...`, `maple mcp enable NAME`, `maple mcp disable NAME`
- `maple mcp rm NAME`: remove a server
- `maple mcp test NAME`: connect directly and list its tools
- `maple status --json`: gateway state and per-server status
- `maple doctor`: diagnose problems and print fixes
