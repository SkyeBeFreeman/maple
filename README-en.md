# maple

[中文](README.md) | English

maple is a local MCP gateway for coding agents. It manages your MCP server configuration in one place and forwards MCP requests from Claude Code, Codex, and CodeBuddy to those servers.

> **Status:** in design. Not usable yet.

## Goals

- **Managed in one place.** Add or change an MCP server once in maple, and every agent sees the same set after a restart.
- **One forwarding endpoint.** Each agent connects to a single gateway on `127.0.0.1`, which forwards requests to the upstream servers. All tools are exposed as `<server>__<tool>`.
- **Shared stdio servers.** By default a stdio server runs once and is shared by all agents, instead of starting one process per agent session.
- **No plaintext secrets in agent configs.** Agent configs refer to secrets by name instead of storing their values.
- **Managed from the command line.** Use the `maple` command to add, change, list, and remove servers. Agents can run it from their own shell too.

## License

[MIT](LICENSE)
