# CC-Connect：飞书接续 Codex 会话

这是基于 [CC-Connect](README.zh-CN.md) 修改的版本，用飞书管理服务器上的 AI 编程会话。主要增加完整 UUID 标识、跨目录切换、引用消息路由和共享 Codex App Server 连接。

**部署前先确认：共享会话接续不等于所有入口自动同步。** 如果要求终端或编辑器的最终回复也发到飞书，第 5 步的同步钩子是必装项。仅 clone、编译和填写 `config.toml`，只会自动回复飞书发起的任务。

| 需求 | 必需组件 |
|---|---|
| 飞书发起任务，结果回复飞书 | cc-connect 和机器人配置 |
| 飞书接续同一个正在使用的 Codex UUID | 管理该 UUID 的同一个共享 App Server |
| 终端或编辑器发起任务，最终回复也同步到飞书 | 额外安装 `contrib/codex-sync` Stop 钩子并指定目标聊天 |
| 在飞书审批飞书任务的权限请求 | 后端产生审批事件，并允许交互审批；示例 `full-auto` 不会请求审批 |
| 在飞书审批终端或编辑器任务的权限请求 | 当前未实现；最终回复同步钩子不转发审批 |

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
| 同步终端或编辑器的回复 | 钩子提交最终正文及元数据，由核心生成统一回复，不附加完成提醒 |

UUID 标识和跨目录查找支持 Codex、Claude Code；本文的共享服务连接和外部回复同步针对 Codex。

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

需要 Go 1.25 或更新版本；统一部署命令还需要 Python 3.11+ 和 Linux systemd 用户服务。克隆包含本次修改的仓库后，在仓库根目录执行：

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

到这里仅完成“飞书发起 → 飞书回复”的部署。若还需要当前终端或编辑器的回复同步，必须继续第 5 步。无需复制旧服务器的 `/usr/bin/codex`、`codex-for-feishu` 或接管脚本；Codex 在新服务器正常安装即可。

### 5. 统一部署程序与回复钩子

飞书发起的任务由 cc-connect 自动回复。终端或编辑器发起的任务若也要出现在飞书，需要额外安装[最终回复同步钩子](contrib/codex-sync/README.md)。

飞书发起的任务和钩子提交的终端、编辑器任务，都由同一个核心函数渲染为“问题摘要＋正文＋目录、模型、UUID”。钩子只提交结构化数据，不再单独拼格式。统一保留原状态页脚：第一行显示模型、`effort`、`out/in/cw/cr` 和 `ctx`，第二行显示目录（用户目录缩写为 `~`），第三行显示 UUID。用量采用后端报告的最新一次推理统计，缺失时省略。

在仓库根目录，首次启用同步时执行：

```bash
python3 contrib/deploy.py --config /绝对路径/config.toml --project server-codex \
  --sync-session 'feishu:oc_聊天ID:ou_用户ID' --restart
```

目标聊天键从新机器人的 `message received` 日志中获取。尚未取得时，先用同一命令加 `--no-sync --restart` 启动桥接服务，取得后再启用同步。等当前桥接任务完成再部署；命令只重启桥接服务，不停止共享 App Server。

后续更新统一执行：

```bash
git pull --ff-only && python3 contrib/deploy.py --config /绝对路径/config.toml --project server-codex --restart
```

同步目标会保存，无需重复填写。它会一起构建程序、更新钩子入口并重启桥接服务；仅 `git pull` 不会替换已运行的旧二进制。钩子通过启动器直接执行仓库源码，避免仓库外副本版本漂移。详见[统一部署与排错](contrib/codex-sync/README.md)。

### 6. 验收两条收发链路

1. 在飞书发起一个简单问题：确认收到原生引用该问题的回复，问题摘要、正文、目录、模型和 UUID 正确。
2. 在终端或编辑器发起一个新轮次：确认最终正文也出现在目标飞书聊天中，且只发送一次。这一步失败时，不能认定回复同步已经部署完成。
3. 引用机器人消息继续提问，再引用目标消息发送 `/stop`，确认路由正确、主会话未改变。
4. 权限审批另行验证：飞书发起的任务需要后端实际产生审批事件；终端或编辑器任务的审批不会因为第 2 步成功而自动同步。

排查时先区分：飞书任务没有收消息日志，应检查应用事件订阅和用户权限；终端任务只有当前界面有回复、飞书没有消息，应检查 Stop 钩子、同步配置和本地 API。不要把两条链路混为一谈。

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
- **外部回复同步**：Stop 钩子提交最终正文和元数据，经本机 `/send` API 进入同一个核心渲染函数，发往指定聊天，不改变主会话。

主要代码位于：

| 位置 | 职责 |
|---|---|
| `core/session_routing.go`、`core/session.go` | UUID 路由、主会话和引用会话管理 |
| `agent/codex/resolve_session.go`、`agent/claudecode/resolve_session.go` | 跨目录查找本机会话 |
| `platform/feishu/session_routing.go`、`platform/feishu/feishu.go` | 引用链解析、身份校验及消息收发 |
| `agent/codex/appserver_shared.go` | 共享连接、队列、轮次归属及断线恢复 |
| `contrib/codex-sync/`、`contrib/deploy.py` | 外部最终回复数据采集、统一安装与更新 |

## 验证

```bash
go test ./...
go test ./core/ -run TestCUJ
go test -race ./core ./agent/codex ./agent/claudecode ./platform/feishu
python3 -m unittest discover -s contrib/codex-sync -v
```

更详细的会话规则见[会话路由说明](docs/session-routing.zh-CN.md)。

## 飞书审批与权限模式

统一部署会注册 `PermissionRequest` 钩子。Codex `/hooks` 加载并信任后，外部权限请求会发送到配置的同步聊天。管理员可点击允许／拒绝，或使用卡片中的 `/approval 请求编号 allow|deny`。审批四分钟超时拒绝，仅对本次请求有效。部署不代替 Codex 的钩子信任，也不改变宿主机权限。

`/mode` 查看代理模式；带参数的权限切换仅允许 `admin_from` 管理员。已有共享会话会在同一 UUID 中提交配置确认轮次，更新审批与沙箱并回读验证，忙碌或验证失败时明确报错。详见 `contrib/codex-sync/README.md`。

## Docker Codex 入口（首版）

登记文件位于 `~/.config/cc-connect/environments/<入口名>/environment.toml`。
当前 `emodia` 配置复用原 `~/docker/start.sh` 的 UID/GID 和代理方式，通过非交互 `connect.sh` 接入容器已有 App Server，不另建写入进程。自定义 `start_script` 用于管理员明确启动；不会自动运行交互式 bash。

飞书发送 `/docker` 查看入口，`/docker emodia` 选择容器，`/list` 查看其中会话，`/switch UUID` 接续；`/docker local` 返回本机。会话存储按环境隔离。启动入口使用 `/docker start emodia`。跨入口汇总与自动 UUID 搜索尚未加入；引用回复按可信机器人页脚的环境和 UUID 路由，不改变当前环境；旧消息则在白名单中查找，歧义时报错。

配置格式、自定义脚本约定与限制见 [运行环境说明](contrib/environments/README.md)。

登记的容器由 `cc-connect-environments-项目名` 用户服务每十秒检测并自动安装回复／审批钩子和专用 relay；可用 `auto_configure = false` 关闭自动配置。安装会保留其他钩子并备份。容器中仍须加载并信任新增钩子，不会自动登录 Codex 或绕过钩子信任。
