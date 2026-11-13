# 同步当前界面的最终回复到飞书

cc-connect 自动引用并回复飞书发起的问题。本钩子补充终端或编辑器发起的任务：通过本机 cc-connect API 转发最后一条正文，附本轮问题摘要、实际工作目录、模型和 UUID，不添加完成提醒或接续命令，不读取机器人密钥。

问题和模型从对应轮次的本机会话记录中读取，避免串用其他轮次的信息。长问题截取前 180 个字符；已有引用链不会完整重复。缺少可靠元数据时省略对应字段。

**需要“终端或编辑器发起任务，最终回复自动进飞书”时，本钩子必装。** clone 仓库、编译、填写机器人 `config.toml` 都不会自动安装或注册它。它只同步最终回复，不转发终端或编辑器的交互审批。

## 1. 安装脚本

在仓库根目录执行，需要 Python 3：

```bash
mkdir -p "$HOME/.local/share/cc-connect" "$HOME/.config/cc-connect"
install -m 700 contrib/codex-sync/sync_reply.py "$HOME/.local/share/cc-connect/sync_reply.py"
```

## 2. 配置目标聊天

创建 `~/.config/cc-connect/reply-sync.json`，填入新服务器自己的值：

```json
{
  "data_dir": "/home/你的用户/.cc-connect/codex",
  "project": "server-codex",
  "session_key": "目标飞书会话的完整键"
}
```

默认读取 `$CODEX_HOME/sessions`（未设置时为 `~/.codex/sessions`）；使用其他位置时，可在 JSON 中添加 `codex_home`。

- `data_dir` 必须与正在运行的机器人 `config.toml` 中的 `data_dir` 一致。
- `project` 必须与 `[[projects]].name` 一致。
- `session_key` 是完整的平台聊天键，不是 Codex UUID，也不是单独的 OpenID。先在飞书给机器人发一条消息，从机器人 `message received` 日志的 `session=` 字段获取，例如 `feishu:oc_聊天ID:ou_用户ID`。不要复制带工作目录或 `:session:s1` 后缀的内部执行键。
- 本配置只保存在服务器本地，并执行 `chmod 600 ~/.config/cc-connect/reply-sync.json`。

## 3. 注册 Codex Stop 钩子

下面的命令将新钩子合并到 `$CODEX_HOME/hooks.json`（默认 `~/.codex/hooks.json`），保留其他钩子，并为已有文件创建备份：

```bash
python3 - <<'PY'
import json
import os
from pathlib import Path
import shlex
import shutil
import sys
import time

home = Path.home()
script = home / '.local/share/cc-connect/sync_reply.py'
config = home / '.config/cc-connect/reply-sync.json'
assert script.is_file() and config.is_file(), '先完成脚本安装和同步配置'
values = json.loads(config.read_text())
assert all(values.get(key) for key in ('data_dir', 'project', 'session_key'))
codex_home = Path(os.environ.get('CODEX_HOME') or home / '.codex').expanduser()
path = codex_home / 'hooks.json'
path.parent.mkdir(parents=True, exist_ok=True)
data = json.loads(path.read_text()) if path.exists() else {}
if path.exists():
    backup = path.with_name('hooks.before-reply-sync-' + str(time.time_ns()) + '.json')
    shutil.copy2(path, backup)
    backup.chmod(0o600)
command = shlex.join([sys.executable, str(script), '--config', str(config)])
stops = data.setdefault('hooks', {}).setdefault('Stop', [])
if not any(hook.get('command') == command for group in stops for hook in group.get('hooks', [])):
    stops.append({'hooks': [{'type': 'command', 'command': command, 'timeout': 10}]})
temporary = path.with_name('hooks.json.reply-sync-tmp')
temporary.write_text(json.dumps(data, ensure_ascii=False, indent=2) + '\n')
temporary.chmod(0o600)
temporary.replace(path)
print('已注册最终回复同步钩子：', path)
PY
```

需要支持 Stop 钩子的 Codex 版本。如果运行中的客户端或 App Server 未重新加载钩子配置，等当前任务结束后重启对应进程，再开始验收。

钩子最终执行的命令形如：

```text
python3 /绝对路径/sync_reply.py --config /绝对路径/reply-sync.json
```

不要同时启用旧完成通知。脚本会跳过 cc-connect 发起的回合，并使用共享连接的轮次标记避免重复；重复 Stop 也会去重。其他回合只发往配置指定的聊天，不改变主会话。cc-connect 必须运行；转发失败会报错，不记录成功。

## 4. 验证

1. 确认机器人已启动，`data_dir/run/api.sock` 存在。
2. 在终端或编辑器发起一个新轮次，让它只回复一段测试文字。
3. 确认目标飞书聊天收到正文，附本轮问题、目录、模型和 UUID。
4. 再从飞书发起一个任务，确认只收到一次正常回复，没有钩子重复转发。

没有送达时，检查 Codex 的钩子错误日志中的 `codex-reply-sync:`。API 连接失败检查 `data_dir` 和机器人是否运行；HTTP 错误检查项目名、目标聊天键及机器人日志。脚本不会因为有共享会话连接就自动启用。

脚本测试：`python3 -m unittest discover -s contrib/codex-sync -v`。
