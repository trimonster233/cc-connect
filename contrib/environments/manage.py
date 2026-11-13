#!/usr/bin/env python3
"""Explicit, scriptable Codex environment registry. No automatic container startup."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import sys
import tomllib


def load(root):
    entries = {}
    for path in sorted(root.glob('*/environment.toml')):
        data = tomllib.loads(path.read_text())
        if not data.get('enabled', True):
            continue
        name = data.get('name', path.parent.name)
        if not name or name in entries or name == 'local':
            raise ValueError('invalid or duplicate environment name: ' + name)
        data['_directory'] = path.parent
        entries[name] = data
    return entries


def command(entry, action):
    script = entry.get(action + '_script')
    if script:
        path = Path(script).expanduser()
        if not path.is_absolute():
            path = entry['_directory'] / path
        return [str(path.resolve()), *entry.get(action + '_args', [])]
    if action == 'start':
        raise ValueError('start_script must be configured explicitly')
    if action != 'connect':
        raise ValueError('unknown action')
    result = ['docker', 'exec', '-i']
    if entry.get('user'):
        result += ['--user', entry['user']]
    if entry.get('work_dir'):
        result += ['--workdir', entry['work_dir']]
    if entry.get('codex_home'):
        result += ['-e', 'CODEX_HOME=' + entry['codex_home']]
    result += [entry['container'], entry.get('codex_binary', 'codex'), 'app-server', 'proxy']
    return result


def probe(entry):
    result = subprocess.run(['docker', 'inspect', '--format', '{{.State.Status}}', entry['container']], capture_output=True, text=True, timeout=10)
    return {'container': entry['container'], 'status': result.stdout.strip() if result.returncode == 0 else 'unavailable', 'connect': command(entry, 'connect')}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--directory', type=Path, default=Path.home() / '.config/cc-connect/environments')
    parser.add_argument('action', choices=['list', 'connect', 'start'])
    parser.add_argument('name', nargs='?')
    args = parser.parse_args()
    entries = load(args.directory)
    if args.action == 'list':
        report = {'local': {'status': 'local'}}
        for name, entry in entries.items():
            try:
                report[name] = probe(entry)
            except (OSError, subprocess.SubprocessError) as exc:
                report[name] = {'status': 'unavailable', 'error': str(exc)}
        print(json.dumps(report, ensure_ascii=False, indent=2))
        return
    if args.name not in entries:
        raise ValueError('environment not configured: ' + str(args.name))
    argv = command(entries[args.name], args.action)
    os.execvp(argv[0], argv)


if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError, KeyError, subprocess.SubprocessError) as exc:
        print('cc-connect environments: ' + str(exc), file=sys.stderr)
        sys.exit(1)
