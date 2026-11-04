# maple

中文 | [English](README-en.md)

maple 是面向编码 agent 的本机 MCP 网关：统一管理 MCP server 的配置，并把 Claude Code、Codex、CodeBuddy 的 MCP 请求转发到这些 server。

> **状态：** 第一版可用，目前只支持 macOS。

## 目标

- **统一管理。** MCP server 只需在 maple 里新增或修改一次，各 agent 重启后看到的是同一套。
- **统一转发。** 每个 agent 只连接 `127.0.0.1` 上的网关，网关把请求转发给上游 server，所有工具以 `<server>__<tool>` 的形式提供。
- **共享 stdio server。** 默认每个 stdio server 只运行一份，由所有 agent 共用，不再每个 agent 会话各起一个进程。
- **agent 配置里不放明文密钥。** agent 配置只按名称引用密钥，不保存密钥值。
- **命令行管理。** 用 `maple` 命令增删改查 server；agent 也可以在自己的 shell 里运行它。

## 安装

需要 macOS 和 Go 1.26 及以上版本。

```bash
go install github.com/SkyeBeFreeman/maple/cmd/maple@latest
maple install
```

`maple install` 可以重复运行，每次只补上缺少的部分：

1. 创建 `~/.maple/`，生成默认配置和访问网关用的 token。默认端口 `47321`，被占用时用 `--port` 指定。
2. 注册并启动 LaunchAgent，由 launchd 常驻运行网关。
3. 在 Claude Code、Codex、CodeBuddy 的配置里各写入一个名为 `maple` 的网关条目。

完成后重启 agent。卸载用 `maple uninstall`，加 `--purge` 同时删除 `~/.maple/` 和所有已保存的密钥。

## 常用命令

```bash
# 保存密钥：值从终端读取（不回显），不经过命令行参数
maple secret set DEMO_TOKEN

# 新增远程 server；密钥写成 ${NAME} 引用，用单引号避免被 shell 展开
maple mcp add demo --url https://example.com/mcp --header 'Authorization=Bearer ${DEMO_TOKEN}'

# 新增 stdio server；默认所有 agent 共用一个进程
maple mcp add demo-local --env 'DEMO_TOKEN=${DEMO_TOKEN}' -- npx -y demo-mcp-server

maple mcp list     # 列出 server
maple mcp rm demo  # 删除 server
maple status       # 网关是否在运行，以及每个 server 的状态
maple doctor       # 逐项检查安装情况并给出修复建议
maple mcp import   # 把各 agent 配置里已有的 server 收进 maple
```

- `maple mcp import` 在终端里运行时会列出可导入的 server 供勾选，并询问是否删除 agent 配置里的原条目（删除前先备份）；非交互运行时需写明 server 名，加 `--remove-originals` 才删除原条目。原条目里的明文密钥会存入钥匙串并改成 `${NAME}` 引用；需要 OAuth 的远程 server 会被跳过，继续留在 agent 配置里。
- stdio server 可用 `--mode` 选择运行方式：`shared`（默认，所有 agent 共用一个进程）、`per-session`（每个 agent 会话一个进程）、`direct`（不经过网关，由 agent 通过 `maple run <name>` 直接启动）。
- 所有命令都支持 `--json`，输出供 agent 解析的结果。完整用法见 `maple --help`。

## 工作方式

launchd 常驻运行网关 `maple serve`，它只监听 `127.0.0.1`。server 配置保存在 `~/.maple/config.yaml`，密钥保存在 macOS 钥匙串，网关连接上游 server 时才把 `${NAME}` 换成真实的值。`maple` 命令只修改这份配置，网关检测到文件变化后自动重新加载，并通知已连接的 agent 工具列表有变化；agent 没有刷新时，重启即可。经网关的 server 在每个 agent 的配置里共用一个指向网关的 `maple` 条目（`direct` server 另有各自的条目），例如 Claude Code：

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

Codex：

```toml
[mcp_servers.maple]
url = "http://127.0.0.1:47321/mcp"
http_headers = { Authorization = "Bearer <token>" }
```

这里的 token 以明文写入：它只能访问本机网关，用来挡住浏览器发来的请求和本机其他用户。上游 server 的密钥不会出现在 agent 配置里。

## License

[MIT](LICENSE)
