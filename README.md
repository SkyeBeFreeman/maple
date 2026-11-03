# maple

中文 | [English](README-en.md)

maple 是面向编码 agent 的本机 MCP 网关：统一管理 MCP server 的配置，并把 Claude Code、Codex、CodeBuddy 的 MCP 请求转发到这些 server。

> **状态：** 方案设计中，暂不可用。

## 目标

- **统一管理。** MCP server 只需在 maple 里新增或修改一次，各 agent 重启后看到的是同一套。
- **统一转发。** 每个 agent 只连接 `127.0.0.1` 上的网关，网关把请求转发给上游 server，所有工具以 `<server>__<tool>` 的形式提供。
- **共享 stdio server。** 默认每个 stdio server 只运行一份，由所有 agent 共用，不再每个 agent 会话各起一个进程。
- **agent 配置里不放明文密钥。** agent 配置只按名称引用密钥，不保存密钥值。
- **命令行管理。** 用 `maple` 命令增删改查 server；agent 也可以在自己的 shell 里运行它。

## License

[MIT](LICENSE)
