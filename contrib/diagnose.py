#!/usr/bin/env python3
"""Read-only check of the actual reply renderer, service and sync entrypoint."""
import argparse
import ast
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys

ROOT = Path(__file__).resolve().parent.parent


def launcher_source(path):
    if not path.exists():
        return None
    try:
        for node in ast.walk(ast.parse(path.read_text())):
            if isinstance(node, ast.Call) and isinstance(node.func, ast.Attribute) and node.func.attr == 'run_path' and node.args:
                return ast.literal_eval(node.args[0])
    except (SyntaxError, ValueError):
        pass
    return 'source copy or unrecognized launcher'


def preview(sync, config):
    """This endpoint never sends; old servers return 404, without fallback."""
    request = {'project': config['project'], 'session_key': config.get('session_key') or 'diagnostic:preview',
               'session_reply': {'content': 'Read-only renderer preview; no message is sent.',
                                 'model': 'diagnostic-model', 'effort': 'medium', 'work_dir': str(ROOT),
                                 'session_id': '00000000-0000-0000-0000-000000000000',
                                 'usage': {'input_tokens': 193100, 'output_tokens': 272, 'cache_read_tokens': 189200,
                                           'cache_write_tokens': 0, 'used_tokens': 193372, 'context_window': 470000}}}
    conn = sync.UnixHTTPConnection(Path(config['data_dir']).expanduser() / 'run/api.sock')
    try:
        conn.request('POST', '/reply/preview', json.dumps(request).encode(), {'Content-Type': 'application/json'})
        response = conn.getresponse()
        raw = response.read()
        if response.status != 200:
            return {'http_status': response.status, 'error': raw.decode(errors='replace')[:300]}
        return json.loads(raw)
    except OSError as exc:
        return {'error': str(exc)}
    finally:
        conn.close()


def main():
    if sys.version_info < (3, 11):
        for name in ('python3.13', 'python3.12', 'python3.11'):
            executable = shutil.which(name)
            if executable:
                os.execv(executable, [executable, str(Path(__file__).resolve()), *sys.argv[1:]])
        raise RuntimeError('diagnosis requires Python 3.11+')
    import tomllib
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', type=Path, required=True)
    parser.add_argument('--project')
    parser.add_argument('--sync-config', type=Path)
    parser.add_argument('--service')
    args = parser.parse_args()
    path = args.config.expanduser().resolve()
    data = tomllib.loads(path.read_text())
    selected = [p for p in data.get('projects', []) if not args.project or p.get('name') == args.project]
    if len(selected) != 1:
        raise RuntimeError('specify --project to select exactly one project')
    project = selected[0]
    home = Path.home()
    configured_data = Path(data.get('data_dir') or home / '.cc-connect').expanduser().resolve()
    sync_path = args.sync_config.expanduser().resolve() if args.sync_config else configured_data / 'reply-sync.json'
    if not args.sync_config and not sync_path.exists():
        sync_path = home / '.config/cc-connect/reply-sync.json'
    target = json.loads(sync_path.read_text()) if sync_path.exists() else {'project': project['name'], 'data_dir': str(configured_data)}
    spec = importlib.util.spec_from_file_location('reply_collector', ROOT / 'contrib/codex-sync/sync_reply.py')
    sync = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(sync)
    report = {'config': str(path), 'project': project['name'], 'configured_data_dir': str(configured_data),
              'sync_config': str(sync_path), 'sync_data_dir': target['data_dir'], 'sync_project': target['project'],
              'sync_target_matches_config': Path(target['data_dir']).expanduser().resolve() == configured_data and target['project'] == project['name'],
              'preview_uses_sample_usage': True, 'api_reply_preview': preview(sync, target)}
    service = args.service or 'cc-connect-' + project['name']
    try:
        pid = int(subprocess.check_output(['systemctl', '--user', 'show', service, '-p', 'MainPID', '--value'], text=True, stderr=subprocess.DEVNULL, timeout=3).strip())
        report['service_pid'] = pid
        if pid:
            report['service_executable'] = os.readlink('/proc/' + str(pid) + '/exe')
            report['running_process_version'] = subprocess.check_output(['/proc/' + str(pid) + '/exe', '--version'], text=True, timeout=5).strip()
    except (OSError, ValueError, subprocess.SubprocessError) as exc:
        report['service_check_error'] = str(exc)
    codex_home = Path(target.get('codex_home') or os.environ.get('CODEX_HOME') or home / '.codex').expanduser()
    hooks = codex_home / 'hooks.json'
    if hooks.exists():
        stops = [h for group in json.loads(hooks.read_text()).get('hooks', {}).get('Stop', []) for h in group.get('hooks', [])]
        report['stop_hooks_total'] = len(stops)
        report['managed_sync_hooks'] = sum('/sync_reply.py' in h.get('command', '') or '/hooks/feishu-stop.sh' in h.get('command', '') for h in stops)
    report['launcher_sources'] = {str(p): launcher_source(p) for p in (home / '.local/share/cc-connect/sync_reply.py', home / '.local/share/cc-handoff/sync_reply.py')}
    receipts = Path(target['data_dir']).expanduser() / 'reply-sync/deliveries.jsonl'
    if receipts.exists():
        lines = receipts.read_text().splitlines()
        if lines:
            record = json.loads(lines[-1])
            report['last_delivery'] = {key: record.get(key) for key in ('time', 'session_id', 'api_pid', 'api_version', 'format')}
    print(json.dumps(report, ensure_ascii=False, indent=2))


if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError, RuntimeError, KeyError) as exc:
        print('cc-connect diagnose: ' + str(exc), file=sys.stderr)
        sys.exit(1)
