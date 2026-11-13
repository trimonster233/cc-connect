# 按 UUID 切换会话与引用回复

在需要此能力的项目配置中启用，并重启服务：

```toml
[[projects]]
name = "local-codex"
session_routing = true
admin_from = "你的飞书用户ID"
```

Claude Code 和 Codex 均支持。默认关闭，其他代理需要实现 `core.GlobalSessionResolver` 才能跨目录查找。

每次模型回复末尾显示完整的 `UUID: ...`。它是模型后端会话 UUID，不是 cc-connect 的 `s1` 等本地编号。

直接发送 `/switch 完整UUID`，服务会在当前代理的本机会话记录中查找 UUID，恢复它的原始工作目录，并把它设为主会话。无需先 `/dir`。编号、名称和 UUID 前缀继续沿用当前目录内的匹配规则。全局切换需要项目管理员权限，多工作区项目仍受基础目录限制。

在飞书中使用“回复”引用一条带 UUID 的机器人消息，再发送下一步需求，这条消息会进入被引用的会话。该会话的新回复仍带自己的 UUID，可继续引用。主会话不会因此改变；之后不带引用的普通消息继续进入主会话。只有显式 `/switch` 会切换主会话，选择也会在服务重启后保留。

同一会话忙碌时，后续引用消息进入它自己的队列；不同会话可独立执行。

引用某条机器人消息发送 `/stop`，只停止该 UUID 在当前聊天中由机器人执行或排队的任务，并清除它的后续待处理消息，不改变主会话，也不影响其他 UUID。不带引用的 `/stop` 停止主会话。引用无效或无权限时会报错，不会退回停止主会话。其他斜杠命令仍沿用主会话逻辑。

引用路由只信任当前飞书应用发出的 UUID 页脚，不从用户输入或其他机器人的文字中提取目标。旧消息没有 UUID、引用内容读取失败、会话文件不存在时，会明确提示，避免把消息误送到主会话。机器人需要读取消息的权限。

## 接入共享 Codex App Server

若终端会话由已有 App Server 管理，可让机器人连接同一个服务：

```toml
[projects.agent.options]
backend = "app_server"
app_server_url = "unix://"
```

`unix://` 使用 `$CODEX_HOME/app-server-control/app-server-control.sock`（默认 `~/.codex`）。也支持 `unix:///绝对路径`、`ws://` 和 `wss://`。不设置地址时仍启动独立 stdio 服务。

恢复会话时保留原有目录、模型、权限及工具配置。同一个 UUID 的轮次依次执行：终端正在执行时，飞书输入进入服务端队列，回复会提示已排队。不同 UUID 可以并行。机器人只转发自己提交的轮次，断线重连不会重新提交任务；关闭机器人不会停止共享服务。`/stop` 只删除或中断机器人自己的提交，不会中断终端的任务。

共享服务必须是该 UUID 的现有写入者；独立 CLI 进程占用的会话不能通过另起服务接管。

验证：

```bash
go test ./...
go test ./core/ -run TestCUJ
go test -race ./core ./agent/codex ./agent/claudecode ./platform/feishu
```
