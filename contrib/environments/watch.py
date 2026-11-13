#!/usr/bin/env python3
"""Watch only registered containers and provision after registration/restart."""
import argparse
import hashlib
import json
from pathlib import Path
import subprocess
import sys
import time
import tomllib
from provision import provision


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--sync-config', type=Path, required=True)
    parser.add_argument('--directory', type=Path, default=Path.home() / '.config/cc-connect/environments')
    args = parser.parse_args()
    completed = {}
    disabled = set()
    while True:
        try:
            sync = json.loads(args.sync_config.read_text())
            enabled_names = set()
            for path in sorted(args.directory.glob('*/environment.toml')):
                try:
                    entry = tomllib.loads(path.read_text())
                    if entry.get('kind', 'docker') != 'docker' or not entry.get('enabled', True):
                        continue
                    enabled_names.add(entry['name'])
                    if not entry.get('auto_configure', True):
                        continue
                    state = subprocess.check_output(['docker', 'inspect', '--format', '{{.State.Running}} {{.State.StartedAt}}', entry['container']], text=True, timeout=5).strip()
                    if not state.startswith('true '):
                        continue
                    source = b''.join(p.read_bytes() for p in sorted((Path(__file__).parent.parent / 'codex-sync').glob('*.py')))
                    fingerprint = hashlib.sha256(path.read_bytes() + json.dumps(sync, sort_keys=True).encode() + state.encode() + source).hexdigest()
                    if completed.get(str(path), (None, None))[0] == fingerprint:
                        continue
                    result = provision(path, sync['project'], sync['session_key'], Path(sync['data_dir']))
                    completed[str(path)] = (fingerprint, entry['name'])
                    disabled.discard(entry['name'])
                    print(entry['name'] + ': ' + json.dumps(result), flush=True)
                except (OSError, ValueError, KeyError, RuntimeError, subprocess.SubprocessError) as exc:
                    print(str(path) + ': ' + str(exc), file=sys.stderr, flush=True)
            for config_path in (Path(sync['data_dir']) / 'container-relays').glob('*.json'):
                installed = json.loads(config_path.read_text())
                name = installed.get('environment')
                if installed.get('project') == sync['project'] and name not in enabled_names and name not in disabled:
                    disabled.add(name)
                    completed = {key: value for key, value in completed.items() if value[1] != name}
                    service = 'cc-connect-relay-' + sync['project'] + '-' + name
                    subprocess.run(['systemctl', '--user', 'disable', '--now', service], check=False, timeout=10, stdout=subprocess.DEVNULL)
        except (OSError, ValueError, KeyError) as exc:
            print('environment watcher: ' + str(exc), file=sys.stderr, flush=True)
        time.sleep(10)


if __name__ == '__main__':
    main()
