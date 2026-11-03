# AGENTS.md

本文件为 AI 编码工具（Claude Code、Codex、CodeBuddy 等）提供 maple 的项目上下文与开发规范。

- Claude Code 只读 `CLAUDE.md`：仓库里的 `CLAUDE.md` 只有一行 `@AGENTS.md`，用来导入本文件。
- CodeBuddy IDE 按 `CODEBUDDY.md` → `.codebuddy/CODEBUDDY.md` → `AGENTS.md` 的顺序查找，命中第一个就停。不要新增 `CODEBUDDY.md`，否则本文件不会被读到。

## 项目简介

maple 是本机自用的 MCP 网关：统一管理 MCP server 的配置，并把 Claude Code、Codex、CodeBuddy IDE 的 MCP 请求转发到这些 server。

- 形态：独立的应用服务，单二进制 `maple`
- 仓库：`github.com/SkyeBeFreeman/maple`
- 语言：Go（最低版本以 `go.mod` 为准）
- License：MIT

## 目录结构

```
cmd/maple/          main 包：只做参数解析和组装，业务逻辑放 internal/
internal/<pkg>/     全部业务代码；internal/ 外的模块无法导入，maple 不对外提供库
  testdata/         各包自己的测试夹具（go 工具链会忽略该目录）
README.md           中文说明
README-en.md        英文说明
LICENSE
```

- 不建 `pkg/`、`src/`、`util/`、`common/` 这类按「类型」而非「职责」命名的目录；包名用单个小写名词，说明它负责什么。
- 新增顶层目录前先出方案。

## 方案设计工作流

**大型需求或非平凡 bugfix，动手写代码前必须先出方案。** 满足任一条即视为大型：

- 改动跨 2 个及以上包，或新增包、调整包之间的依赖关系
- 新增上游传输协议、agent 适配器或 CLI 子命令
- 改动 store 的文件格式，或改动写入各 agent 配置文件的逻辑
- 涉及并发、子进程生命周期、鉴权与 Host/Origin 校验等安全边界
- bug 根因不清晰，需要先调研复现路径

流程：

1. 完成方案设计（skill、subagent 或手动均可）：澄清需求边界、定位根因、写成可执行的分步计划。
2. 方案文档放在 `.agents/specs/<YYYY-MM-DD-topic>.md`，材料多时改用同名子目录。`.agents/specs/` 已在 `.gitignore` 中，只留本地，不要提交。
3. 与用户确认方案后再实现，优先测试驱动（先写失败的测试，再写实现）。

小改动（typo、单文件局部修复、纯文档调整）可直接实现。

## 约定

- 对外文档只有 `README.md`（中文）和 `README-en.md`（英文）。改动用户可见的行为时，两份同步更新。
- 入库的内容（代码、测试数据、README、本文件）不写本机路径、内部服务名与域名、任何密钥，一律用通用示例。
- `~/.agents/mcp.json` 等 MCP 配置文件可能含明文密钥：只打印键名和结构，不要 `cat` 或输出值。
- 测试与本地调试一律使用临时 HOME（例如 `t.TempDir()`），不要读写真实的 `~/.maple/`、`~/.claude.json`、`~/.codex/`、`~/.codebuddy/`、`~/.agents/`。

## 构建与测试

代码落地前的预定命令，落地后按实际补充：

```bash
go build ./...          # 编译检查
go test -race ./...     # 单元测试；网关的并发和子进程管理多，默认带 -race
golangci-lint fmt       # 格式化并写回（gofmt、gci）
golangci-lint run       # 静态检查，配置见 .golangci.yml（golangci-lint v2）
go mod tidy             # 整理依赖；go mod tidy -diff 只读自查
```

## 代码风格

- 由 golangci-lint（`.golangci.yml`）强制执行。`gci` 的 import 分组：标准库 → 第三方 → 本项目（`github.com/SkyeBeFreeman/maple/...`）。
- 代码注释用英文，只解释 WHY（隐藏约束、易错点、不变量），不复述代码在做什么。
- 导出命名遵循 Go 规范，常见缩写保持全大写：`ID`、`URL`、`HTTP`、`MCP`、`JSON`、`TOML`。
- 用标准库 `context`；会阻塞或启动子进程的函数，第一个参数接收 `context.Context`。
- 错误向上返回时用 `%w` 包装并补上下文；除 `main` 的启动阶段外不 `panic`。
- 源码文件不加 License 文件头（MIT 只需根目录 `LICENSE`）。

## 提交前检查清单

- `go build ./...` 与 `go test -race ./...` 通过
- `golangci-lint fmt` 之后 `golangci-lint run` 无新增问题
- `go mod tidy -diff` 无输出
- 改动用户可见的行为时，`README.md` 与 `README-en.md` 已同步
- diff 里没有本机路径、内部域名或密钥

## Git 提交规范

- 使用 conventional commits：`<type>: <subject>`，用英文。常用 type：`feat` / `fix` / `refactor` / `test` / `docs` / `chore`
- Agent 创建 commit 之前，必须实际执行上面的构建与测试，通过后再 `git commit`
- 每次提交必须包含 `Signed-off-by` 行，使用 `git commit -s`
- 由 AI 协助产出的提交，正文末尾追加 `Co-Authored-By` 署名行，**带上实际使用的模型名称**；若该次产出使用了非默认的运行参数（如扩大上下文窗口），在模型名后用括号标注：

```
<改动说明>

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
Signed-off-by: Your Name <you@example.com>
```

格式说明：`Co-Authored-By: <工具名/模型名> (<参数>) <noreply 邮箱>`。常见写法：

```
Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>
Co-Authored-By: kimi-k3 <noreply@moonshot.cn>
Co-Authored-By: Cursor <cursoragent@cursor.com>
Co-Authored-By: CodeBuddy Code
```

模型名与参数以当次会话实际使用的为准，不要凭记忆填；不确定时只写工具名。
