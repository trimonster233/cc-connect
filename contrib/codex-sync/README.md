# 统一最终回复与部署

飞书发起、终端发起和编辑器发起的任务，统一由 cc-connect 核心格式化最终回复：

```text
> 问题：本轮问题摘要

回复正文

gpt-6.1-sol · effort:medium · out 272 · in 193.1k cw 0 cr 189.2k · ctx 41%
~/project/cc-connect
UUID: 完整会话UUID
```

长问题截取前 180 个字符，引用链不完整重复。状态页脚固定为“模型 · effort · out/in/cw/cr · ctx”一行，目录和 UUID 各占一行；用户主目录压缩为 `~`。用量取对应轮次最新一次推理的统计，不取整个会话的累计总量；缺少可靠元数据时省略对应字段，不伪造用量。语言随项目配置。飞书发起的任务还会原生引用触发消息。卡片可保留本轮工具步骤，但问题、正文和身份页脚使用同一渲染逻辑。

钩子只收集数据并通过本机 `/send` API 的 `session_reply` 字段提交，**不再自行拼接任何展示格式**。不读取机器人密钥，不转发终端或编辑器的权限审批。

## 首次部署

需要 Go 1.25+、Python 3.11+ 和 Linux systemd 用户服务。钩子执行本身兼容 Python 3.10；部署命令会优先使用本机已有的较新 Python。

1. 在仓库的 `feat/session-routing` 分支填写机器人 `config.toml`，先启动管理目标会话的共享 Codex App Server。
2. 尚未知道目标聊天键时，在仓库根目录执行：

   ```bash
   python3 contrib/deploy.py --config /绝对路径/config.toml --project server-codex --no-sync --restart
   ```

3. 向新飞书机器人发送一条消息。从机器人 `message received` 日志的 `session=` 字段获取完整聊天键，例如 `feishu:oc_聊天ID:ou_用户ID`。如果被拒绝访问，临时启用 debug 日志取得 OpenID，填入 `admin_from` 和 `allow_from` 后重新部署。
4. 等机器人当前任务结束后，启用最终回复同步：

   ```bash
   python3 contrib/deploy.py --config /绝对路径/config.toml --project server-codex \
     --sync-session 'feishu:oc_聊天ID:ou_用户ID' --restart
   ```

`session_key` 不是 Codex UUID、单独的 OpenID，或带目录和 `:session:s1` 后缀的内部执行键。已有同步配置时无需重复传 `--sync-session`；非默认配置位置可用 `--sync-config /路径/reply-sync.json`。

部署命令会构建并原子更新二进制、保存本机同步配置、注册 Stop 钩子、安装 systemd 用户服务，并在指定 `--restart` 时重启桥接服务。默认服务名为 `cc-connect-server-codex.service`，可用 `--service` 指定已有的服务名。它不会停止共享 App Server。

钩子入口是指向 Git 仓库源码的启动器，**不再复制一份需要手动更新的脚本源码**。旧托管入口也会转为启动器；其他用户钩子会保留。不要移动或删除部署所用的仓库目录，移动后需要重新执行部署命令。

## 后续更新：一条命令

在仓库根目录，等桥接任务结束后执行：

```bash
git pull --ff-only && python3 contrib/deploy.py --config /绝对路径/config.toml --project server-codex --restart
```

这会一起更新二进制和钩子。**仅 `git pull` 不代表正在运行的二进制已经更新。** 不带 `--restart` 时只安装文件，等待你稍后重启；`--no-sync` 只跳过本次同步安装，不会删除以前注册的同步钩子。

已运行的 Codex 客户端或 App Server 若没有重新加载新增的钩子，需要等其任务结束后重载配置或重启。已有钩子启动器的后续源码更新不需要重新复制。

## 本机配置与排错

同步配置默认保存在 `config.toml` 的 `data_dir` 下的 `reply-sync.json`。部署命令会复用旧的 `~/.config/cc-connect/reply-sync.json`，或 `--sync-config` 指定的文件。配置包括实际 `data_dir`、项目名、完整目标聊天键和 `codex_home`，仅保存在服务器本地，权限 0600。

`codex_home` 来自项目选项、`CODEX_HOME` 或默认 `~/.codex`。钩子按对应轮次从其中读取问题、目录和模型。cc-connect 必须运行，并提供 `data_dir/run/api.sock`。

- 终端有回复但飞书没有：检查 Stop 钩子是否加载、聊天键是否正确，以及钩子日志中的 `codex-reply-sync:` 错误。
- `/send` 返回 400：可能仍在运行不支持结构化回复的旧二进制，执行上述更新命令并重启。没有旧格式降级，避免悄悄发送另一套格式。
- API 连接失败：检查 `data_dir` 与机器人运行状态。
- 重复消息：检查是否仍启用独立的旧完成通知。同步脚本会跳过桥接轮次，并按轮次和正文去重。
- 权限审批没有同步：终端或编辑器审批转发当前未实现，最终回复同步不包含这项功能。

### 版本正确但显示格式不符

不要只检查磁盘上的 `cc-connect --version`。在仓库根目录运行只读诊断：

```bash
python3 contrib/diagnose.py --config /绝对路径/config.toml --project server-codex
```

非默认同步配置或服务名仍可用 `--sync-config`、`--service` 指定。诊断不会运行模型或发送飞书消息，也不会输出应用凭据。预览中的用量是明确标记的样例数据，不是真实消费。

报告会同时检查实际运行进程的版本、同步所指向的 `data_dir`、钩子入口引用的源码路径，并调用实际 API 的 `/reply/preview` 获取正文和页脚。旧 API 返回 404 时不会降级成发送请求。

- `api_reply_preview.footer` 是接收该同步请求的进程实际生成的页脚，`pid`、`version` 标识真实接收者。
- `sync_target_matches_config=false` 表示钩子与机器人配置指向不同的项目或 API 数据目录。
- `stop_hooks_total` 大于预期时，检查是否还有独立的旧推送钩子。
- `last_delivery` 来自 `data_dir/reply-sync/deliveries.jsonl`，记录最近一次成功同步的 API PID、版本和格式，不保存问题或回复正文。
- 预览正确、消息正文却仍含旧标签时，检查标签是否由模型写在正文里。核心会清理与当前 UUID、目录和模型完全匹配的旧页脚，不会猜测删除普通正文。

## 验收

1. 飞书发起一个问题，确认回复包含问题摘要、正文、目录、模型和 UUID。
2. 终端或编辑器发起新轮次，确认相同布局的最终回复进入指定聊天，且只发一次。
3. 引用回复和 `/stop` 仍定位原 UUID，主会话不变。
4. 完成一次 Git 更新和统一部署，再重复前两项，确认运行版本与仓库一致。

测试：

```bash
python3 -m unittest discover -s contrib/codex-sync -v
python3.12 -m unittest discover -s contrib/tests -v
go test ./core/ -run 'TestCUJ_B3_ExternalAndInteractiveRepliesUseIdenticalFormat|TestFormatSessionReply'
```
