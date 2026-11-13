#!/usr/bin/env python3
"""Ask the configured chat administrator about one Codex PermissionRequest."""
import argparse
import json
from pathlib import Path
import sys
from sync_reply import UnixHTTPConnection


def request(payload, config):
    if config.get('relay_dir'):
        from spool import SpoolConnection
        conn = SpoolConnection(config['relay_dir'])
    else:
        conn = UnixHTTPConnection(Path(config['data_dir']) / 'run/api.sock')
    conn.timeout = 250
    body = {key: payload.get(key, '') for key in ('session_id', 'tool_name', 'tool_input', 'cwd')}
    body.update(project=config['project'], session_key=config['session_key'])
    try:
        conn.request('POST', '/approval/request', json.dumps(body), {'Content-Type': 'application/json'})
        # UnixHTTPConnection uses its own default socket timeout.
        conn.sock.settimeout(250)
        response = conn.getresponse()
        if response.status != 200:
            raise RuntimeError('approval API HTTP ' + str(response.status))
        behavior = json.loads(response.read())['behavior']
        if behavior not in ('allow', 'deny'):
            raise ValueError('invalid approval decision')
        return behavior
    finally:
        conn.close()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--config', type=Path, required=True)
    args = parser.parse_args()
    behavior = 'deny'
    try:
        behavior = request(json.load(sys.stdin), json.loads(args.config.read_text()))
    except Exception as exc:
        print('codex-approval: ' + str(exc), file=sys.stderr)
    print(json.dumps({'hookSpecificOutput': {'hookEventName': 'PermissionRequest', 'decision': {'behavior': behavior}}}))


if __name__ == '__main__':
    main()
