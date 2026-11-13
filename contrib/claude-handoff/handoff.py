#!/usr/bin/env python3
"""Local Claude Code / cc-connect handoff helpers (Python stdlib, Linux)."""
import argparse
import copy
import datetime as dt
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import socket
import subprocess
import sys
import tempfile
import time


def records(path):
    with path.open(encoding="utf-8") as stream:
        for line in stream:
            try:
                yield json.loads(line)
            except json.JSONDecodeError:
                # Claude can be appending the last JSONL record during a scan.
                continue


def text_content(row):
    content = row.get("message", {}).get("content", [])
    if isinstance(content, str):
        return content
    return "\n".join(x.get("text", "") for x in content if x.get("type") == "text")


def inspect_session(path):
    cwd, summary, reply = "", "", ""
    for row in records(path):
        cwd = row.get("cwd") or cwd
        text = text_content(row)
        if row.get("type") == "user" and text and not summary:
            summary = text.replace("\n", " ")[:100]
        if row.get("type") == "assistant" and text:
            reply = text
        if row.get("type") == "custom-title":
            summary = row.get("customTitle") or summary
    return dict(id=path.stem, cwd=cwd, summary=summary, reply=reply,
                modified=path.stat().st_mtime, path=path)


def catalog(claude_dir):
    result = []
    for path in (claude_dir / "projects").glob("*/*.jsonl"):
        try:
            item = inspect_session(path)
            if item["cwd"] and re.fullmatch(r"[0-9a-fA-F-]{36}", item["id"]):
                result.append(item)
        except OSError as exc:
            print(f"无法读取会话 {path}: {exc}", file=sys.stderr)
    return sorted(result, key=lambda x: (-x["modified"], x["id"]))


def select_session(items, query):
    found = [x for x in items if x["id"].startswith(query)]
    if len(found) != 1:
        raise ValueError("会话 ID 不存在或前缀不唯一，请使用完整 UUID")
    return found[0]


def instructions(item):
    # /dir resets the active bridge session. Create a disposable slot first
    # so changing directories cannot erase the previous conversation history.
    return f'/new\n/dir {item["cwd"]}\n/switch {item["id"]}'


def print_catalog(items, limit):
    print("终端 Claude 会话（先等待原终端当前任务结束，再接管）")
    for item in items[:limit]:
        date = dt.datetime.fromtimestamp(item["modified"]).strftime("%m-%d %H:%M")
        print(f'\n{date}  {item["summary"]}\n{instructions(item)}')
    print("\n飞书依次发送 /new、目标会话的 /dir 和 /switch，然后直接发下一步需求。")


def atomic_json(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, temp = tempfile.mkstemp(prefix=path.name + ".", dir=path.parent)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as stream:
            json.dump(value, stream, ensure_ascii=False, indent=2)
            stream.write("\n")
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temp, path)
    finally:
        if os.path.exists(temp):
            os.unlink(temp)


def read_json(path, default):
    return json.loads(path.read_text()) if path.exists() else copy.deepcopy(default)


def session_store(data_dir, project, work_dir):
    suffix = hashlib.sha256(os.path.abspath(work_dir).encode()).hexdigest()[:8]
    name = f"{project}_{suffix}.json"
    for path in [data_dir / name, data_dir / name.replace(".json", ".sessions.json")]:
        if path.exists():
            return path
    return data_dir / "sessions" / name


def project_config(path, project):
    # Only parse the few scalar fields needed here. Preserve the TOML verbatim.
    # Unsupported TOML literals fail closed; no third-party package is needed.
    section, selected, result = "", False, {}
    for raw in path.read_text().splitlines():
        line = raw.strip()
        if line == "[[projects]]":
            section, selected = line, False
        elif line.startswith("["):
            section = line
        match = re.match(r'^(name|work_dir|mode|type)\s*=\s*("(?:[^"\\]|\\.)*")\s*(?:#.*)?$', line)
        if not match:
            continue
        key, value = match.group(1), json.loads(match.group(2))
        if section == "[[projects]]" and key == "name":
            selected = value == project
        if selected:
            if section == "[[projects]]" and key == "mode":
                result["mode"] = value
            if section == "[projects.agent]" and key == "type":
                result["type"] = value
            if section == "[projects.agent.options]" and key == "work_dir":
                result["work_dir"] = value
    if result.get("type") != "claudecode" or not result.get("work_dir"):
        raise ValueError("找不到单工作区 Claude 项目配置（需要双引号形式的 work_dir）")
    if result.get("mode") == "multi-workspace":
        raise ValueError("此本地接管工具要求单工作区模式")
    return result


def attach(snapshot, item, session_key=None):
    result = copy.deepcopy(snapshot)
    active = result.setdefault("active_session", {})
    if session_key is None:
        if len(active) != 1:
            raise ValueError("有多个或没有聊天目标；请使用 --session-key 指定")
        session_key = next(iter(active))
    if session_key not in active:
        raise ValueError("聊天目标不存在；先在飞书与机器人对话")
    sessions = result.setdefault("sessions", {})
    owned = result.setdefault("user_sessions", {}).setdefault(session_key, [])
    sid = next((sid for sid in owned if sessions.get(sid, {}).get("agent_session_id") == item["id"]), None)
    now = dt.datetime.now(dt.timezone.utc).isoformat()
    if sid is None:
        counter = result.get("counter", 0) + 1
        while f"s{counter}" in sessions:
            counter += 1
        sid = f"s{counter}"
        result["counter"] = counter
        sessions[sid] = dict(id=sid, name=item["summary"] or item["id"],
                             agent_session_id=item["id"], agent_type="claudecode",
                             history=None, created_at=now, updated_at=now)
        owned.append(sid)
    sessions[sid]["explicit_activated_at"] = now
    active[session_key] = sid
    return result


def daemon_process(binary, config=None):
    matches = []
    for proc in Path("/proc").iterdir():
        if not proc.name.isdigit():
            continue
        try:
            if (proc / "exe").resolve() != binary.resolve():
                continue
            args = (proc / "cmdline").read_bytes().split(b"\0")[:-1]
            argv = [os.fsdecode(a) for a in args]
            cwd = os.readlink(proc / "cwd")
            if config is not None:
                configured = None
                for index, arg in enumerate(argv):
                    if arg in ("--config", "-config", "-c") and index+1<len(argv):
                        configured = argv[index+1]
                    elif arg.startswith("--config="):
                        configured = arg.split("=",1)[1]
                actual = Path(configured) if configured else Path(cwd)/"config.toml"
                if not actual.exists() and not configured:
                    actual=Path.home()/".cc-connect/config.toml"
                if not actual.is_absolute(): actual=Path(cwd)/actual
                if actual.resolve()!=config.resolve(): continue
            matches.append((int(proc.name), argv, cwd))
        except (OSError, RuntimeError):
            continue
    if len(matches) != 1:
        raise ValueError("需要且只能有一个运行中的指定 cc-connect 二进制服务")
    return matches[0]


def merge_snapshots(source, destination):
    """Keep both stores' conversations, remapping colliding local sN IDs."""
    result = copy.deepcopy(source)
    sessions = result.setdefault("sessions", {})
    for user, ids in destination.get("user_sessions", {}).items():
        owned = result.setdefault("user_sessions", {}).setdefault(user, [])
        for old_id in ids:
            entry = destination.get("sessions", {}).get(old_id)
            if not entry:
                continue
            if any(sessions.get(sid) == entry for sid in owned):
                continue
            # A snapshot copied earlier can contain an older version of the
            # same conversation. Keep the current source if history extends it.
            history = entry.get("history") or []
            if any(sessions.get(sid, {}).get("agent_session_id") == entry.get("agent_session_id")
                   and sessions.get(sid, {}).get("created_at") == entry.get("created_at")
                   and (sessions.get(sid, {}).get("history") or [])[:len(history)] == history
                   for sid in owned):
                continue
            counter = result.get("counter", 0) + 1
            while f"s{counter}" in sessions:
                counter += 1
            new_id = f"s{counter}"
            result["counter"] = counter
            sessions[new_id] = dict(copy.deepcopy(entry), id=new_id)
            owned.append(new_id)
        if user not in result.setdefault("active_session", {}) and owned:
            result["active_session"][user] = owned[-1]
    for field in ("session_names", "user_meta"):
        result.setdefault(field, {}).update({k: v for k, v in destination.get(field, {}).items()
                                           if k not in result.get(field, {})})
    return result


def ensure_no_agent_children(pid):
    parents = {}
    for proc in Path("/proc").iterdir():
        if not proc.name.isdigit():
            continue
        try:
            stat = (proc / "stat").read_text().rsplit(")", 1)[1].split()
            parents[int(proc.name)] = int(stat[1])
        except (OSError, ValueError, IndexError):
            continue
    pending = [pid]
    while pending:
        parent = pending.pop()
        children = [child for child, owner in parents.items() if owner == parent]
        for child in children:
            try:
                name = Path(f"/proc/{child}/comm").read_text().strip()
                if name.startswith("claude"):
                    raise ValueError("机器人仍有 Claude 子进程；请等任务结束并在飞书 /stop 后重试")
            except FileNotFoundError:
                continue
        pending.extend(children)


def stop_daemon(pid):
    os.kill(pid, signal.SIGTERM)
    deadline = time.monotonic() + 20
    while time.monotonic() < deadline:
        try:
            stat = Path(f"/proc/{pid}/stat").read_text()
            if stat.rsplit(")", 1)[1].strip().startswith("Z"):
                return
        except FileNotFoundError:
            return
        time.sleep(0.1)
    raise RuntimeError("服务未能正常退出，未修改会话文件")


def start_daemon(binary, config, data_dir, env=None):
    with (data_dir / "cc-connect.out").open("ab") as log:
        proc = subprocess.Popen([str(binary), "--config", str(config)], cwd="/tmp",
                                stdin=subprocess.DEVNULL, stdout=log, stderr=log,
                                start_new_session=True, env=env)
    deadline = time.monotonic() + 25
    sock_path = data_dir / "run" / "api.sock"
    while time.monotonic() < deadline:
        if proc.poll() is not None:
            raise RuntimeError(f"服务启动失败，查看 {data_dir / 'cc-connect.out'}")
        try:
            with socket.socket(socket.AF_UNIX) as sock:
                sock.settimeout(1)
                sock.connect(str(sock_path))
                return proc
        except OSError:
            time.sleep(0.2)
    proc.terminate()
    proc.wait(timeout=10)
    raise RuntimeError("服务 API 启动超时")


def takeover(args, item):
    cfg = project_config(args.config, args.project)
    if not Path(item["cwd"]).is_dir():
        raise ValueError("会话的工作目录已不存在")
    state_path = args.data_dir / "projects" / f"{args.project}.state.json"
    state = read_json(state_path, {})
    old_dir = state.get("work_dir_override") or cfg["work_dir"]
    source = session_store(args.data_dir, args.project, old_dir)
    target = session_store(args.data_dir, args.project, item["cwd"])
    snapshot = read_json(source, {})
    # Validate the destination user before stopping the running bridge.
    attach(snapshot, item, args.session_key)
    print(f'接管会话 {item["id"]}\n目录 {item["cwd"]}\n会话存储 {target}')
    if args.dry_run:
        return
    pid, argv, cwd = daemon_process(args.binary, args.config)
    config_arg = None
    for i, value in enumerate(argv):
        if value in ("--config", "-config", "-c") and i + 1 < len(argv):
            config_arg = argv[i + 1]
        elif value.startswith("--config="):
            config_arg = value.split("=", 1)[1]
    running_config = Path(config_arg) if config_arg else Path(cwd) / "config.toml"
    if not running_config.exists() and not config_arg:
        running_config = Path.home() / ".cc-connect/config.toml"
    if running_config.resolve() != args.config.resolve():
        raise ValueError("运行服务的配置路径与 --config 不同，拒绝重启其他服务")
    ensure_no_agent_children(pid)
    server_env = dict(os.fsdecode(item).split("=", 1) for item in
                      Path(f"/proc/{pid}/environ").read_bytes().split(b"\0") if b"=" in item)
    stop_daemon(pid)
    originals = {}
    try:
        # Read AFTER shutdown: the daemon may flush its latest history on exit.
        snapshot = read_json(source, {})
        for path in (target, state_path):
            originals[path] = path.read_bytes() if path.exists() else None
        backup = args.data_dir / "handoff-backups" / str(time.time_ns())
        backup.mkdir(parents=True, mode=0o700)
        for path in {source, target, state_path}:
            if path.exists():
                (backup / path.name).write_bytes(path.read_bytes())
                (backup / path.name).chmod(0o600)
        if target != source and target.exists():
            snapshot = merge_snapshots(snapshot, read_json(target, {}))
        atomic_json(target, attach(snapshot, item, args.session_key))
        state = read_json(state_path, {})
        state["work_dir_override"] = item["cwd"]
        if "session_work_dirs" in state:
            key = args.session_key or next(iter(snapshot["active_session"]))
            state["session_work_dirs"][key] = item["cwd"]
        atomic_json(state_path, state)
        start_daemon(args.binary, args.config, args.data_dir, server_env)
    except Exception:
        for path, content in originals.items():
            if content is None:
                path.unlink(missing_ok=True)
            else:
                path.write_bytes(content)
        start_daemon(args.binary, args.config, args.data_dir, server_env)
        raise
    print("已接管。现在可在飞书发送下一步需求。原终端请勿同时向此会话发消息。")


def main():
    parser = argparse.ArgumentParser(description="全局列出并接管本机 Claude Code 会话")
    parser.add_argument("session", nargs="?", help="会话 UUID 或唯一前缀；省略时列出")
    parser.add_argument("--limit", type=int, default=12)
    parser.add_argument("--instructions", action="store_true", help="仅输出飞书切换命令，不重启")
    parser.add_argument("--dry-run", action="store_true")
    parser.add_argument("--project", default="local-claude")
    parser.add_argument("--session-key")
    parser.add_argument("--claude-dir", type=Path,
                        default=Path(os.environ.get("CLAUDE_CONFIG_DIR", str(Path.home() / ".claude"))))
    parser.add_argument("--data-dir", type=Path, default=Path.home() / ".cc-connect")
    parser.add_argument("--config", type=Path, default=Path.home() / ".cc-connect/config.toml")
    local_binary=Path.home()/".local/bin/cc-connect-session-routing"
    parser.add_argument("--binary", type=Path, default=local_binary if local_binary.exists() else Path.home() / ".local/lib/node_modules/cc-connect/bin/cc-connect")
    args = parser.parse_args()
    items = catalog(args.claude_dir)
    if args.session is None:
        print_catalog(items, args.limit)
        return
    item = select_session(items, args.session)
    if args.instructions:
        print(instructions(item))
        return
    if args.dry_run:
        takeover(args, item)
        return
    args.data_dir.mkdir(parents=True, exist_ok=True)
    with (args.data_dir / "handoff.lock").open("w") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        takeover(args, item)


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as exc:
        print(f"cc-takeover: {exc}", file=sys.stderr)
        sys.exit(1)
