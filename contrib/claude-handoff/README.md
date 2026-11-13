# 本机 Claude Code → 飞书接续

这是本机部署辅助工具，不修改 cc-connect 的 Go 内核。使用 Python 3 标准库，进程管理依赖 Linux `/proc`。

```bash
cc-takeover                         # 全局列出最近 12 个终端会话
cc-takeover UUID --instructions     # 只显示飞书切换命令
cc-takeover UUID --dry-run          # 查看本地接管计划
cc-takeover UUID                    # 备份状态，短暂重启服务并接管
```

飞书中发送 `/cc-sessions` 可查看全局会话。对目标会话，**分三条消息**发送输出的 `/new`、`/dir /绝对路径` 和 `/switch UUID`，等各自确认后，直接说下一步要做什么。先 `/new` 是为了避免 `/dir` 清空之前桥接会话的历史。已有 `/history` 可以读取恢复会话的历史。此方式不需要终端执行接管工具。

终端接管模式仅支持单工作区 Claude 项目，默认名称为 `local-claude`。可用 `--project`、`--config`、`--data-dir`、`--binary` 指定部署；多个聊天目标必须使用 `--session-key`。配置中的项目类型和工作目录需要使用普通双引号 TOML 字符串。工具使用项目状态覆盖保存工作目录，不修改原始配置，复制并合并会话存储时保留历史、重映射冲突的本地会话编号。原文件备份在 `~/.cc-connect/handoff-backups/`。若机器人仍有 Claude 子进程，先等待任务结束，再从飞书 `/stop` 后重试。

此工具恢复同一个 Claude 会话的上下文，不操纵原终端的交互进程。原终端和飞书不要同时向同一个 UUID 发任务。回到终端可以在原目录执行 `claude --resume UUID`。

任务回复由 cc-connect 直接发送，不安装额外的终端完成通知钩子。

`cc-connect send` 的作用是向平台发送消息，不会给模型派发任务。请勿把它作为终端提交 AI 任务的接口。

测试：`python3 -m unittest discover -s contrib/claude-handoff -v`。本工具不涉及 Go 变更。
