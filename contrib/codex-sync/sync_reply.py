#!/usr/bin/env python3
"""Forward external Codex final replies through the local cc-connect API."""
import argparse
import hashlib
import http.client
import json
import os
from pathlib import Path
import re
import socket
import sys
import time


class UnixHTTPConnection(http.client.HTTPConnection):
    def __init__(self, path):
        super().__init__('localhost', timeout=6)
        self.path = path

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        self.sock.connect(str(self.path))


def turn_details(payload, config, sid):
    """Read the matching turn, never a later turn's model or question."""
    details = {key: payload.get(key, '') for key in ('cwd', 'model')}
    details['question'] = payload.get('user_message') or payload.get('last_user_message') or ''
    home = Path(config.get('codex_home') or os.environ.get('CODEX_HOME') or Path.home() / '.codex').expanduser()
    path = payload.get('transcript_path')
    if path:
        path = Path(path)
    else:
        path = next((home / 'sessions').rglob('*-' + sid + '.jsonl'), None)
    if path is None:
        return details
    target = payload.get('turn_id') or payload.get('turnId')
    current = None
    try:
        with path.open() as stream:
            for line in stream:
                try:
                    row = json.loads(line)
                except ValueError:
                    continue  # The daemon may still be appending its last record.
                item = row.get('payload', {})
                if row.get('type') == 'session_meta':
                    if (item.get('id') or item.get('session_id')) != sid:
                        return {key: payload.get(key, '') for key in ('cwd', 'model')}
                    if not details['cwd']:
                        details['cwd'] = item.get('cwd', '')
                elif row.get('type') == 'turn_context':
                    current = item.get('turn_id')
                    if not target or current == target:
                        for key in ('cwd', 'model'):
                            if item.get(key):
                                details[key] = item[key]
                elif row.get('type') == 'response_item' and item.get('type') == 'message' and item.get('role') == 'user':
                    metadata = item.get('internal_chat_message_metadata_passthrough') or {}
                    turn = metadata.get('turn_id') or current
                    if not target or turn == target:
                        details['question'] = '\n'.join(part.get('text', '') for part in item.get('content', []) if part.get('type') in ('input_text', 'text'))
                elif row.get('type') == 'event_msg' and item.get('type') == 'user_message':
                    if not target or (item.get('turn_id') or current) == target:
                        details['question'] = item.get('message', '')
    except OSError as exc:
        print('codex-reply-sync: cannot read turn details: ' + str(exc), file=sys.stderr)
    return details


def question_excerpt(text):
    # Quote the new user request, not the entire quoted conversation chain.
    if text.startswith('--- Reply chain ('):
        _, separator, request = text.partition('\n---\n\n')
        if separator:
            text = request
    elif text.startswith('[Quoted message from ') and '\n\n\n' in text:
        text = text.rsplit('\n\n\n', 1)[-1]
    text = ' '.join(text.split())
    if len(text) > 180:
        text = text[:180] + '…'
    return re.sub(r'([\\`*_\[\]<>])', r'\\\1', text)


def format_reply(reply, sid, details):
    lines = reply.rstrip().splitlines()
    # Replace an existing trailing UUID with a single consistent footer.
    while lines and (not lines[-1].strip() or re.fullmatch(r'\*?UUID:\s*' + re.escape(sid) + r'\*?\s*', lines[-1])):
        lines.pop()
    text = '\n'.join(lines)
    question = question_excerpt(details.get('question', ''))
    if question:
        text = '> 问题：' + question + '\n\n' + text
    footer = []
    if details.get('cwd'):
        footer.append('目录：' + details['cwd'])
    if details.get('model'):
        footer.append('模型：' + details['model'])
    footer.append('UUID: ' + sid)
    return text + '\n\n' + '\n'.join(footer)


def forward(payload, config):
    if os.environ.get('CC_PROJECT') or os.environ.get('CC_SESSION_KEY') or payload.get('stop_hook_active'):
        return False
    sid = payload.get('session_id') or payload.get('thread_id') or ''
    reply = payload.get('last_assistant_message') or payload.get('last-assistant-message') or ''
    if not re.fullmatch(r'[0-9a-fA-F]{8}(?:-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}', sid) or not reply.strip():
        return False
    data = Path(config['data_dir']).expanduser()
    marker = data / 'bridge-turns' / (sid + '.json')
    try:
        active = json.loads(marker.read_text())
        turn = payload.get('turn_id') or payload.get('turnId')
        if 0 <= time.time() - active['started_at'] < 43200 and (not turn or turn == active.get('turn_id')):
            return False
    except FileNotFoundError:
        pass
    # Stable key also handles Stop deliveries without a turn ID.
    key = hashlib.sha256((sid + '\0' + str(payload.get('turn_id') or payload.get('turnId') or '') + '\0' + reply).encode()).hexdigest()
    sent = data / 'reply-sync' / key
    if sent.exists():
        return False
    text = format_reply(reply, sid, turn_details(payload, config, sid))
    body = json.dumps({'project': config['project'], 'session_key': config['session_key'], 'message': text}, ensure_ascii=False).encode()
    conn = UnixHTTPConnection(data / 'run/api.sock')
    try:
        conn.request('POST', '/send', body, {'Content-Type': 'application/json'})
        response = conn.getresponse()
        response.read()
        if response.status != 200:
            raise RuntimeError('cc-connect reply sync HTTP ' + str(response.status))
    finally:
        conn.close()
    sent.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    sent.touch(mode=0o600)
    return True


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--config', type=Path, required=True)
    args = parser.parse_args()
    if os.environ.get('CC_PROJECT') or os.environ.get('CC_SESSION_KEY'):
        return
    forward(json.load(sys.stdin), json.loads(args.config.read_text()))


if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError, KeyError, TypeError, RuntimeError, http.client.HTTPException) as exc:
        print('codex-reply-sync: ' + str(exc), file=sys.stderr)
        sys.exit(1)
