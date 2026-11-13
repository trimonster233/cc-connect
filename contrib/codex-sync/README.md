# 同步当前界面的最终回复到飞书

cc-connect 自动引用并回复飞书发起的问题。本钩子补充终端或编辑器发起的任务：通过本机 cc-connect API 转发最后一条正文，附本轮问题摘要、实际工作目录、模型和 UUID，不添加完成提醒或接续命令，不读取机器人密钥。

问题和模型从对应轮次的本机会话记录中读取，避免串用其他轮次的信息。长问题截取前 180 个字符；已有引用链不会完整重复。缺少可靠元数据时省略对应字段。

把 `sync_reply.py` 安装到用户目录，创建仅本机使用的 JSON 配置：

```json
{
  "data_dir": "/home/你的用户/.cc-connect/codex",
  "project": "local-codex",
  "session_key": "目标飞书会话的完整键"
}
```

默认读取 `$CODEX_HOME/sessions`（未设置时为 `~/.codex/sessions`）；使用其他位置时，可在 JSON 中添加 `codex_home`。

在 Codex 的 `hooks.json` 中添加 Stop command：

```text
python3 /绝对路径/sync_reply.py --config /绝对路径/reply-sync.json
```

不要同时启用旧完成通知。脚本会跳过 cc-connect 发起的回合，并使用共享连接的轮次标记避免重复；重复 Stop 也会去重。其他回合只发往配置指定的聊天，不改变主会话。cc-connect 必须运行；转发失败会报错，不记录成功。

验证：`python3 -m unittest discover -s contrib/codex-sync -v`。
