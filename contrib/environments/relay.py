#!/usr/bin/env python3
"""Host-side relay: no listening TCP port and no bot credentials in containers."""
import argparse
from concurrent.futures import ThreadPoolExecutor
import importlib.util
import json
from pathlib import Path
import subprocess
import threading
import time
import uuid
import sys

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('reply_sync', ROOT / 'codex-sync/sync_reply.py')
sync = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sync)


def forward(item, config):
    path = item['path']
    if path not in ('/send', '/approval/request'):
        raise ValueError('unsupported container relay endpoint')
    body = item['body']
    body['project'] = config['project']
    body['session_key'] = config['session_key']
    if path == '/send':
        reply = body.get('session_reply')
        if not isinstance(reply, dict):
            raise ValueError('only structured replies may be relayed')
        if str(uuid.UUID(reply['session_id'])) != reply['session_id']:
            raise ValueError('invalid session UUID')
        reply['environment'] = config['environment']
        marker = Path(config['data_dir']) / 'bridge-turns' / (reply['session_id'] + '.json')
        try:
            active = json.loads(marker.read_text())
            if path == '/send' and active.get('turn_id') == body.get('source_turn_id') and time.time() - active['started_at'] < 43200:
                return 200, {'format': 'bridge-turn-skipped'}
        except FileNotFoundError:
            pass
    conn = sync.UnixHTTPConnection(Path(config['data_dir']) / 'run/api.sock')
    conn.timeout = 250 if path == '/approval/request' else 6
    try:
        conn.request('POST', path, json.dumps(body), {'Content-Type': 'application/json'})
        response = conn.getresponse()
        raw = response.read()
        try:
            result = json.loads(raw)
        except ValueError:
            result = {'error': raw.decode(errors='replace')}
        return response.status, result
    finally:
        conn.close()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--config', type=Path, required=True)
    config = json.loads(parser.parse_args().config.read_text())
    while True:
        command = ['docker', 'exec', '-i', '--user', config['user'], config['container'], 'python3', config['worker'], config['spool']]
        process = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True)
        lock = threading.Lock()
        def handle(item):
            try:
                status, body = forward(item, config)
            except Exception as exc:
                status, body = 502, {'error': str(exc)}
            with lock:
                try:
                    process.stdin.write(json.dumps({'id': item['id'], 'status': status, 'body': body}) + '\n')
                    process.stdin.flush()
                except (OSError, ValueError):
                    pass
        with ThreadPoolExecutor(max_workers=16) as executor:
            for line in process.stdout:
                try:
                    item = json.loads(line)
                    executor.submit(handle, item)
                except ValueError as exc:
                    print('container-relay: ' + str(exc), file=sys.stderr)
        process.wait()
        time.sleep(2)


if __name__ == '__main__':
    main()
