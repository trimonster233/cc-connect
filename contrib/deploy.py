#!/usr/bin/env python3
"""Build and deploy the bridge and its repository-backed Codex reply hook."""
import argparse
import json
import os
from pathlib import Path
import shlex
import shutil
import signal
import subprocess
import sys
import tempfile
import time

REPO = Path(__file__).resolve().parent.parent


def atomic_text(path, text, mode=0o600):
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, name = tempfile.mkstemp(prefix='.' + path.name, dir=path.parent)
    try:
        with os.fdopen(fd, 'w') as stream:
            stream.write(text)
        os.chmod(name, mode)
        os.replace(name, path)
    finally:
        Path(name).unlink(missing_ok=True)


def register_sync(repo, home, codex_home, sync_config):
    """Install launchers, not source copies, and merge only our managed hook."""
    launcher = home / '.local/share/cc-connect/sync_reply.py'
    source = repo / 'contrib/codex-sync/sync_reply.py'
    launcher_text = '#!/usr/bin/env python3\nimport runpy\nrunpy.run_path(' + repr(str(source)) + ', run_name="__main__")\n'
    atomic_text(launcher, launcher_text, 0o700)
    command = shlex.join([sys.executable, str(launcher), '--config', str(sync_config)])
    path = codex_home / 'hooks.json'
    hooks = json.loads(path.read_text()) if path.exists() else {}
    if path.exists():
        backup = path.with_name('hooks.before-deploy-' + str(time.time_ns()) + '.json')
        atomic_text(backup, path.read_text())
    groups = []
    managed = ('/sync_reply.py', '/codex_notify.py', '/hooks/feishu-stop.sh')
    for group in hooks.setdefault('hooks', {}).get('Stop', []):
        kept = [hook for hook in group.get('hooks', []) if not any(token in hook.get('command', '') for token in managed)]
        if kept:
            groups.append(dict(group, hooks=kept))
    groups.append({'hooks': [{'type': 'command', 'command': command, 'timeout': 10}]})
    hooks['hooks']['Stop'] = groups
    approval_command = shlex.join([sys.executable, str(repo / 'contrib/codex-sync/approve.py'), '--config', str(sync_config)])
    approval_groups = []
    for group in hooks['hooks'].get('PermissionRequest', []):
        kept = [hook for hook in group.get('hooks', []) if '/contrib/codex-sync/approve.py' not in hook.get('command', '')]
        if kept:
            approval_groups.append(dict(group, hooks=kept))
    approval_groups.append({'hooks': [{'type': 'command', 'command': approval_command, 'timeout': 270}]})
    hooks['hooks']['PermissionRequest'] = approval_groups
    atomic_text(path, json.dumps(hooks, ensure_ascii=False, indent=2) + '\n')
    # Existing running clients may retain an old command until their config
    # reloads. Keep those managed entrypoints forwarding to the same source.
    legacy = home / '.local/share/cc-handoff/sync_reply.py'
    if legacy.exists():
        atomic_text(legacy, launcher_text, 0o700)
    wrapper = codex_home / 'hooks/feishu-stop.sh'
    if wrapper.exists():
        atomic_text(wrapper, '#!/bin/sh\nexec ' + command + '\n', 0o700)
    return launcher


def unit_text(repo, config, binary, home):
    def quote(value):
        return '"' + str(value).replace('\\', '\\\\').replace('"', '\\"').replace('%', '%%') + '"'
    return '\n'.join([
        '[Unit]', 'Description=CC Connect bridge', 'After=network-online.target', '',
        '[Service]', 'Type=simple', 'WorkingDirectory=' + str(repo).replace('%', '%%'),
        'Environment=' + quote('PATH=' + str(home / '.local/bin') + ':/usr/local/bin:/usr/bin:/bin'),
        'ExecStart=' + quote(binary) + ' --config ' + quote(config),
        'Restart=on-failure', 'RestartSec=5', 'TimeoutStopSec=30', '',
        '[Install]', 'WantedBy=default.target', '',
    ])


def stop_previous_bridge(data, config):
    """Stop only a positively identified bridge, never its shared App Server."""
    path = data / 'service.pid'
    if not path.exists():
        return
    pid = int(path.read_text().strip())
    proc = Path('/proc') / str(pid)
    if not proc.exists():
        return
    args = proc.joinpath('cmdline').read_bytes().split(b'\0')
    if str(config).encode() not in args or b'--config' not in args:
        raise RuntimeError('service.pid does not identify this config; refusing to stop an unrelated process')
    if proc.joinpath('task', str(pid), 'children').read_text().strip():
        raise RuntimeError('bridge has child processes; wait for its tasks to finish before deploying')
    os.kill(pid, signal.SIGTERM)
    deadline = time.monotonic() + 25
    while time.monotonic() < deadline:
        try:
            if proc.joinpath('stat').read_text().rsplit(')', 1)[1].strip().startswith('Z'):
                return
        except FileNotFoundError:
            return
        time.sleep(0.1)
    raise RuntimeError('previous bridge did not exit; new service was not started')


def main():
    # Hook execution works on Python 3.10; only this deployment command needs
    # the stdlib TOML reader. Prefer an available modern Python automatically.
    if sys.version_info < (3, 11):
        for name in ('python3.13', 'python3.12', 'python3.11'):
            executable = shutil.which(name)
            if executable:
                os.execv(executable, [executable, str(Path(__file__).resolve()), *sys.argv[1:]])
        raise RuntimeError('deployment requires Python 3.11+ for TOML parsing')
    import tomllib
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', type=Path, required=True)
    parser.add_argument('--project')
    parser.add_argument('--sync-session', help='target platform session key; saved for subsequent updates')
    parser.add_argument('--sync-config', type=Path)
    parser.add_argument('--no-sync', action='store_true', help='deploy only the bridge; do not enable external reply sync')
    parser.add_argument('--service', help='systemd user service name, without .service')
    parser.add_argument('--restart', action='store_true', help='restart this bridge after its tasks have finished')
    parser.add_argument('--go', default=shutil.which('go'))
    args = parser.parse_args()
    if args.restart:
        subprocess.run(['systemctl', '--user', 'show-environment'], check=True, stdout=subprocess.DEVNULL)
    config = args.config.expanduser().resolve()
    parsed = tomllib.loads(config.read_text())
    projects = parsed.get('projects', [])
    selected = [project for project in projects if project.get('name') == args.project] if args.project else projects
    if len(selected) != 1:
        raise RuntimeError('specify --project to select exactly one configured project')
    project = selected[0]
    home = Path.home()
    data = Path(parsed.get('data_dir') or home / '.cc-connect').expanduser().resolve()
    options = project.get('agent', {}).get('options', {})
    codex_home = Path(options.get('codex_home') or os.environ.get('CODEX_HOME') or home / '.codex').expanduser().resolve()
    sync_config = (args.sync_config.expanduser().resolve() if args.sync_config else data / 'reply-sync.json')
    previous_config = home / '.config/cc-connect/reply-sync.json'
    if not args.sync_config and not sync_config.exists() and previous_config.exists():
        sync_config = previous_config
    service = args.service or ('cc-connect-' + project['name'])
    if not service or any(char not in 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_.' for char in service):
        raise RuntimeError('invalid service name')
    values = {}
    if not args.no_sync:
        if project.get('agent', {}).get('type') != 'codex':
            raise RuntimeError('external reply sync currently requires a Codex project; use --no-sync for other agents')
        if sync_config.exists():
            values = json.loads(sync_config.read_text())
        key = args.sync_session or values.get('session_key')
        if not key or ':' not in key:
            raise RuntimeError('first deployment requires --sync-session (full platform chat key); use --no-sync to bootstrap the bridge first')
        values.update(data_dir=str(data), project=project['name'], session_key=key, codex_home=str(codex_home))
    if not args.go:
        raise RuntimeError('Go 1.25+ is required; install it or specify --go /path/to/go')
    binary = home / '.local/lib/cc-connect/cc-connect'
    binary.parent.mkdir(parents=True, exist_ok=True)
    temp_binary = binary.with_name('cc-connect.next-' + str(os.getpid()))
    try:
        revision = subprocess.check_output(['git', 'describe', '--always', '--dirty'], cwd=REPO, text=True, stderr=subprocess.DEVNULL).strip()
    except subprocess.CalledProcessError:
        revision = 'local'
    try:
        subprocess.run([args.go, 'build', '-tags', 'no_web', '-ldflags', '-X main.version=session-routing-' + revision, '-o', str(temp_binary), './cmd/cc-connect'], cwd=REPO, check=True)
        temp_binary.chmod(0o755)
        os.replace(temp_binary, binary)
    finally:
        temp_binary.unlink(missing_ok=True)
    link = home / '.local/bin/cc-connect'
    link.parent.mkdir(parents=True, exist_ok=True)
    temporary_link = link.with_name('cc-connect.deploy-new')
    temporary_link.unlink(missing_ok=True)
    temporary_link.symlink_to(binary)
    os.replace(temporary_link, link)
    if not args.no_sync:
        atomic_text(sync_config, json.dumps(values, ensure_ascii=False, indent=2) + '\n')
        register_sync(REPO, home, codex_home, sync_config)
        watcher_service = 'cc-connect-environments-' + project['name']
        watcher_command = [sys.executable, str(REPO / 'contrib/environments/watch.py'), '--sync-config', str(sync_config)]
        if options.get('environment_dir'):
            watcher_command += ['--directory', str(Path(options['environment_dir']).expanduser().resolve())]
        def systemd_quote(value):
            return '"' + value.replace('\\', '\\\\').replace('"', '\\"').replace('%', '%%') + '"'
        watcher_unit = '[Unit]\nDescription=CC Connect registered container setup\n[Service]\nExecStart=' + ' '.join(systemd_quote(v) for v in watcher_command) + '\nRestart=on-failure\nRestartSec=3\n[Install]\nWantedBy=default.target\n'
        atomic_text(home / '.config/systemd/user' / (watcher_service + '.service'), watcher_unit, 0o644)

    unit = home / '.config/systemd/user' / (service + '.service')
    atomic_text(unit, unit_text(REPO, config, binary, home), 0o644)
    if args.restart:
        verifier = shutil.which('systemd-analyze')
        if verifier:
            subprocess.run([verifier, '--user', 'verify', str(unit)], check=True)
        subprocess.run(['systemctl', '--user', 'stop', service + '.service'], check=False)
        stop_previous_bridge(data, config)
        subprocess.run(['systemctl', '--user', 'daemon-reload'], check=True)
        subprocess.run(['systemctl', '--user', 'enable', '--now', service + '.service'], check=True)
        if not args.no_sync:
            subprocess.run(['systemctl', '--user', 'enable', '--now', watcher_service + '.service'], check=True)
            subprocess.run(['systemctl', '--user', 'restart', watcher_service + '.service'], check=True)

        time.sleep(2)
        subprocess.run(['systemctl', '--user', 'is-active', '--quiet', service + '.service'], check=True)
        pid = subprocess.check_output(['systemctl', '--user', 'show', service + '.service', '-p', 'MainPID', '--value'], text=True).strip()
        atomic_text(data / 'service.pid', pid + '\n')
    print('Deployed binary:', binary)
    print('Source revision:', revision)
    print('User service:', service + '.service')
    print('Reply sync:', 'not changed (--no-sync)' if args.no_sync else str(sync_config))
    if not args.restart:
        print('Run the same command with --restart after bridge tasks have finished to activate the new binary.')


if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as exc:
        print('cc-connect deploy: ' + str(exc), file=sys.stderr)
        sys.exit(1)
