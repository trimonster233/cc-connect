# CC-Connect：飞书接续 Codex 会话

这是基于 [CC-Connect](README.zh-CN.md) 修改的版本，用飞书管理服务器上的 AI 编程会话。主要增加完整 UUID 标识、跨目录切换、引用消息路由和共享 Codex App Server 连接。

## 能做什么

| 功能 | 使用方式 |
|---|---|
| 查看会话 UUID | 模型回复末尾显示完整 `UUID: ...`，文本和卡片均支持 |
| 跨目录切换 | 发送 `/switch 完整UUID`，自动查找本机历史及原工作目录 |
| 引用回复到指定会话 | 在飞书引用机器人消息，输入的新需求进入该消息对应的 UUID |
| 保持主会话 | 引用回复不切换主会话；普通消息继续进入主会话，显式 `/switch` 才切换 |
| 停止指定会话 | 引用目标消息发送 `/stop`，只停止该会话的机器人任务；不带引用则停止主会话 |
| 多会话执行 | 不同 UUID 可独立执行；同一个 UUID 的任务依次处理 |
| 接续正在使用的 Codex | 连接管理该会话的共享 App Server，避免另起进程导致写入冲突 |
| 同步终端或编辑器的回复 | 可选钩子转发最终正文，附问题摘要、目录、模型和 UUID，不附加完成提醒 |

UUID 标识和跨目录查找支持 Codex、Claude Code；本文的共享服务连接和可选回复同步针对 Codex。

## 使用示例

假设主会话是 A，另一条机器人消息属于会话 B：

```text
普通发送“继续”           → 会话 A
引用 B 的消息发送“继续”  → 会话 B，主会话仍为 A
再次普通发送“继续”       → 会话 A
发送 /switch B的完整UUID → 主会话切换为 B
```

主会话选择会保存，服务重启后仍然保留。

引用回复时，被引用的正文或回复链也会作为上下文传给模型，用于理解“这个”“刚才”等指代。会话目标只从当前机器人自己的 UUID 页脚中提取。引用缺少有效 UUID、消息读取失败或会话找不到时，会明确报错，避免误发到主会话。

## 在另一台服务器部署新机器人

新服务器独立使用自己的飞书应用、Codex 登录信息、项目目录和会话，无需复制旧服务器的历史或密钥。

### 1. 编译当前版本

需要 Go 1.25 或更新版本。克隆包含本次修改的仓库后，在仓库根目录执行：

```bash
# 仅运行机器人，不包含 Web 管理界面
go build -tags no_web -o cc-connect ./cmd/cc-connect
```

需要 Web 界面时安装 Node.js/npm，然后执行 `make build`。请确保新增文件也已提交到 Git；安装上游官方版本不会自动包含这里的修改。

### 2. 准备 Codex 和飞书应用

- 安装 Codex，完成登录，并准备项目目录及其依赖。
- 共享服务协议当前实测版本为 Codex App Server `0.167.2`；其他版本需要验证队列接口是否兼容。
- 创建新的飞书应用，配置消息接收、消息发送、消息读取权限及长连接事件订阅，并发布应用。基础平台配置见[原项目说明](README.zh-CN.md)。
- `admin_from` 和 `allow_from` 使用新应用下的用户 OpenID；不同应用的 OpenID 不能直接照搬。

### 3. 填写配置

保存为新服务器的 `config.toml`，将所有占位内容换为实际值：

```toml
language = "zh"
data_dir = "/home/USER/.cc-connect/codex"

[[projects]]
name = "server-codex"
session_routing = true
admin_from = "ou_YOUR_OPEN_ID"
reset_on_idle_mins = 0

[projects.agent]
type = "codex"

[projects.agent.options]
work_dir = "/home/USER/project"
backend = "app_server"
app_server_url = "unix://"
mode = "full-auto"

[[projects.platforms]]
type = "feishu"

[projects.platforms.options]
app_id = "cli_YOUR_APP_ID"
app_secret = "YOUR_APP_SECRET"
allow_from = "ou_YOUR_OPEN_ID"
```

配置中的 `admin_from` 属于项目，`allow_from` 属于平台。配置含应用密钥，应仅保存在服务器本地。

### 4. 启动

先启动本机共享 App Server。如果已经有管理目标会话的服务，直接复用；不要重复启动：

```bash
codex app-server --listen unix://
```

然后在另一个终端启动机器人：

```bash
./cc-connect --config /绝对路径/config.toml
```

验证成功后可将两个进程配置为后台服务。`unix://` 默认连接 `$CODEX_HOME/app-server-control/app-server-control.sock`，未设置 `CODEX_HOME` 时使用 `~/.codex`。两者应使用同一用户和同一个 Codex 数据目录。

共享服务必须是目标 UUID 的现有写入者。若终端或编辑器独立启动了另一个写入进程，仅启动新服务不能接管它；终端或编辑器也需要接入同一共享服务。

正常的飞书收发不依赖额外通知脚本，也不需要复制旧服务器的 `/usr/bin/codex`、`codex-for-feishu` 或接管脚本。Codex 在新服务器正常安装即可。

### 5. 可选：同步其他入口的最终回复

飞书发起的任务由 cc-connect 自动回复。终端或编辑器发起的任务若也要出现在飞书，需要额外安装[最终回复同步钩子](contrib/codex-sync/README.md)。

它转发最后一条正文，附本轮问题摘要、实际目录、模型和 UUID，通过本机 cc-connect API 使用已配置的机器人发送。飞书直接发起的任务则原生引用触发消息，并使用正常回复页脚。不添加“任务执行完成”或 `/new`、`/dir` 等提示。它会跳过机器人自己的轮次并去重，避免同一结果发送两次。

## 简单实现说明

```text
飞书消息
  → 飞书适配器读取引用链、验证机器人身份、提取 UUID
  → 核心引擎选择主会话或引用目标会话
  → Codex 适配器通过共享 App Server 提交任务
  → 根据提交标识匹配本轮事件
  → 回复飞书并附 UUID
```

- **会话路由**：核心通过可选接口查找 UUID 对应的原目录，为引用目标保存独立会话和执行状态，不修改主会话选择。
- **共享连接**：使用 WebSocket 连接 Unix socket，也支持 `ws://`、`wss://`。恢复已有会话时保留其运行配置，不启动第二个会话写入进程。
- **排队与事件匹配**：通过服务端队列和 `clientUserMessageId` 识别机器人提交，只转发对应轮次的结果。同一 UUID 忙时等待，不同 UUID 可以并行。
- **断线处理**：重连后查询队列和历史恢复结果，不自动重新提交输入，避免工具重复执行。关闭机器人只断开连接；`/stop` 只取消机器人自己的提交。
- **外部回复同步**：可选 Stop 钩子读取最终正文，经本机 `/send` API 发往配置指定的聊天，不改变主会话。

主要代码位于：

| 位置 | 职责 |
|---|---|
| `core/session_routing.go`、`core/session.go` | UUID 路由、主会话和引用会话管理 |
| `agent/codex/resolve_session.go`、`agent/claudecode/resolve_session.go` | 跨目录查找本机会话 |
| `platform/feishu/session_routing.go`、`platform/feishu/feishu.go` | 引用链解析、身份校验及消息收发 |
| `agent/codex/appserver_shared.go` | 共享连接、队列、轮次归属及断线恢复 |
| `contrib/codex-sync/` | 可选的外部最终回复同步 |

## 验证

```bash
go test ./...
go test ./core/ -run TestCUJ
go test -race ./core ./agent/codex ./agent/claudecode ./platform/feishu
python3 -m unittest discover -s contrib/codex-sync -v
```

更详细的会话规则见[会话路由说明](docs/session-routing.zh-CN.md)。
