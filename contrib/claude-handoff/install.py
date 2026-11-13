#!/usr/bin/env python3
"""Install the reviewed local helper, preserving the existing Feishu setup."""
import importlib.util
import json
import os
from pathlib import Path
import re
import subprocess
import time

source = Path(__file__).with_name("handoff.py")
spec = importlib.util.spec_from_file_location("handoff", source)
handoff = importlib.util.module_from_spec(spec)
spec.loader.exec_module(handoff)


def fix_admin_scope(text, project="local-claude"):
    """Move an existing platform admin allowlist to its project TOML table."""
    lines = text.splitlines(keepends=True)
    section, selected, name_index = "", False, None
    misplaced, existing = [], None
    for index, raw in enumerate(lines):
        line = raw.strip()
        if line.startswith("["):
            section = line
            if line == "[[projects]]":
                selected = False
        match = re.match(r'^(name|admin_from)\s*=\s*("(?:[^"\\]|\\.)*")\s*(?:#.*)?$', line)
        if not match:
            continue
        key, value = match.group(1), json.loads(match.group(2))
        if section == "[[projects]]" and key == "name":
            selected = value == project
            if selected:
                name_index = index
        elif selected and key == "admin_from":
            if section == "[[projects]]":
                existing = value
            elif section == "[projects.platforms.options]":
                misplaced.append((index, value))
    if name_index is None:
        raise ValueError(f"找不到项目 {project}")
    if not misplaced:
        return text
    values = {value for _, value in misplaced}
    if existing is None and len(values) != 1:
        raise ValueError("各平台 admin_from 不一致，不能自动合并项目管理员")
    removed = {index for index, _ in misplaced}
    result = []
    for index, line in enumerate(lines):
        if index not in removed:
            result.append(line)
        if index == name_index and existing is None:
            result.append("# 管理员权限属于项目配置\nadmin_from = " +
                          json.dumps(next(iter(values))) + "\n")
    return "".join(result)


def installation_files(home, config_text):
    """Install session tools only; replies are sent by cc-connect itself."""
    helper = home / ".local/share/cc-handoff/handoff.py"
    wrapper = home / ".local/bin/cc-takeover"
    return {
        helper: source.read_text(),
        wrapper: '#!/bin/sh\nexec /usr/bin/python3 "' + str(helper) + '" "$@"\n',
        home / ".cc-connect/config.toml": config_text,
    }


def install():
    home = Path.home()
    data_dir = home / ".cc-connect"
    config = data_dir / "config.toml"
    binary = home / ".local/lib/node_modules/cc-connect/bin/cc-connect"
    local_binary=home/".local/bin/cc-connect-session-routing"
    if local_binary.exists(): binary=local_binary
    wrapper = home / ".local/bin/cc-takeover"
    pid, _, _ = handoff.daemon_process(binary, config)
    handoff.ensure_no_agent_children(pid)
    env = dict(os.fsdecode(item).split("=", 1) for item in
               Path(f"/proc/{pid}/environ").read_bytes().split(b"\0") if b"=" in item)
    config_text = fix_admin_scope(config.read_text())
    if 'name = "cc-sessions"' not in config_text:
        config_text += ('\n# 全局终端 Claude 会话目录（只读，不执行用户提供的 shell 参数）\n'
                        '[[commands]]\nname = "cc-sessions"\n'
                        'description = "列出本机全部目录的 Claude 会话及飞书接续命令"\n'
                        'exec = "' + str(wrapper) + ' --limit 8"\n')
    updates = installation_files(home, config_text)
    subprocess.run(["bash", "-n"], input=updates[wrapper], text=True, check=True)
    originals = {p: (p.read_bytes(), p.stat().st_mode & 0o777) if p.exists() else None for p in updates}
    backup = data_dir / "handoff-backups" / ("install-" + str(time.time_ns()))
    backup.mkdir(parents=True, mode=0o700)
    for index, (path, previous) in enumerate(originals.items()):
        if previous:
            saved = backup / f"{index}-{path.name}"
            saved.write_bytes(previous[0])
            saved.chmod(0o600)
    handoff.stop_daemon(pid)
    try:
        for path, content in updates.items():
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(content)
            path.chmod(0o600 if path == config else 0o700)
        handoff.start_daemon(binary, config, data_dir, env)
    except Exception:
        for path, previous in originals.items():
            if previous:
                path.write_bytes(previous[0])
                path.chmod(previous[1])
            else:
                path.unlink(missing_ok=True)
        handoff.start_daemon(binary, config, data_dir, env)
        raise
    print(f"安装成功；备份：{backup}")
    print("飞书：/cc-sessions；终端：cc-takeover")


if __name__ == "__main__":
    install()
