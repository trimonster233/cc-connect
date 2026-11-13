#!/usr/bin/env python3
"""Idempotently install hooks and a private stdio relay for registered containers."""
import argparse
import base64
import json
import os
from pathlib import Path
import shlex
import subprocess
import sys
import tomllib

ROOT = Path(__file__).resolve().parents[2]

INSTALL = r'''
import base64,json,os,pathlib,pwd,sys,time,shlex,shutil
payload=json.load(sys.stdin)
home=pathlib.Path(pwd.getpwuid(os.getuid()).pw_dir)
codex=pathlib.Path(payload.get('codex_home') or os.environ.get('CODEX_HOME') or home/'.codex')
binaries=[pathlib.Path(payload['codex_binary'])] if payload.get('codex_binary') else list((home/'.nvm/versions/node').glob('*/bin/codex'))+[home/'.local/bin/codex',pathlib.Path('/usr/local/bin/codex')]
if shutil.which('codex'):binaries.append(pathlib.Path(shutil.which('codex')))
if not any(path.exists() for path in binaries):raise RuntimeError('Codex not detected; configure codex_binary')
base=codex/'cc-connect-relay'/payload['environment']
base.mkdir(parents=True,exist_ok=True,mode=0o700)
for name,content in payload['files'].items():
 path=base/name;temporary=path.with_suffix(path.suffix+'.next');temporary.write_bytes(base64.b64decode(content));temporary.chmod(0o600);os.replace(temporary,path)
config={'project':payload['project'],'session_key':payload['session_key'],'environment':payload['environment'],'codex_home':str(codex),'data_dir':str(base/'state'),'relay_dir':str(base/'spool')}
temporary=base/'config.next';temporary.write_text(json.dumps(config));temporary.chmod(0o600);os.replace(temporary,base/'config.json')
path=codex/'hooks.json'
hooks=json.loads(path.read_text()) if path.exists() else {}
changed=False
for event,script,timeout in [('Stop','sync_reply.py',15),('PermissionRequest','approve.py',270)]:
 command=shlex.join(['python3',str(base/script),'--config',str(base/'config.json')])
 groups=hooks.setdefault('hooks',{}).get(event,[])
 retained=[]
 for group in groups:
  handlers=[h for h in group.get('hooks',[]) if str(base) not in h.get('command','')]
  if handlers:retained.append(dict(group,hooks=handlers))
 retained.append({'hooks':[{'type':'command','command':command,'timeout':timeout}]})
 if groups!=retained:changed=True
 hooks['hooks'][event]=retained
if changed:
 if path.exists():
  backup=path.with_name('hooks.before-cc-connect-'+str(time.time_ns())+'.json');backup.write_text(path.read_text());backup.chmod(0o600)
 temporary=path.with_suffix('.cc-connect.tmp');temporary.write_text(json.dumps(hooks,indent=2)+'\n');temporary.chmod(0o600);os.replace(temporary,path)
print(json.dumps({'worker':str(base/'relay_worker.py'),'spool':str(base/'spool'),'codex_home':str(codex),'hooks_changed':changed}))
'''


def provision(entry_path, project, session_key, data_dir):
    entry = tomllib.loads(entry_path.read_text())
    if not entry.get('enabled', True) or entry.get('auto_configure', True) is False:
        return {'status': 'disabled'}
    name = entry['name']
    if not name or any(c not in 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-' for c in name):
        raise ValueError('invalid environment name')
    container = entry['container']
    user = entry.get('user', str(os.getuid()) + ':' + str(os.getgid()))
    files = {name: base64.b64encode((ROOT / 'contrib/codex-sync' / name).read_bytes()).decode() for name in ('sync_reply.py', 'approve.py', 'spool.py')}
    files['relay_worker.py'] = base64.b64encode((ROOT / 'contrib/environments/relay_worker.py').read_bytes()).decode()
    payload = dict(project=project, session_key=entry.get('sync_session', session_key), environment=name, files=files, codex_home=entry.get('codex_home'), codex_binary=entry.get('codex_binary'))
    result = subprocess.run(['docker', 'exec', '-i', '--user', user, container, 'python3', '-c', INSTALL], input=json.dumps(payload), text=True, capture_output=True, check=False, timeout=20)
    if result.returncode:
        raise RuntimeError('container ' + name + ': ' + result.stderr.strip()[-600:])
    remote = json.loads(result.stdout)
    config = dict(remote, container=container, user=user, project=project, session_key=payload['session_key'], environment=name, data_dir=str(data_dir))
    folder = data_dir / 'container-relays'
    folder.mkdir(parents=True, exist_ok=True, mode=0o700)
    config_path = folder / (name + '.json')
    encoded = json.dumps(config, indent=2)
    config_path.write_text(encoded)
    config_path.chmod(0o600)
    service = 'cc-connect-relay-' + project + '-' + name
    unit = Path.home() / '.config/systemd/user' / (service + '.service')
    unit.parent.mkdir(parents=True, exist_ok=True)
    argv = [sys.executable, str(ROOT / 'contrib/environments/relay.py'), '--config', str(config_path)]
    def quote(value):
        return '"' + value.replace('\\', '\\\\').replace('"', '\\"').replace('%', '%%') + '"'
    text = '[Unit]\nDescription=CC Connect container reply relay\n[Service]\nExecStart=' + ' '.join(quote(v) for v in argv) + '\nRestart=on-failure\nRestartSec=3\n[Install]\nWantedBy=default.target\n'
    unit.write_text(text)
    subprocess.run(['systemctl', '--user', 'daemon-reload'], check=True, timeout=10)
    subprocess.run(['systemctl', '--user', 'enable', '--now', service], check=True, timeout=10, stdout=subprocess.DEVNULL)
    # Configuration is reloaded by restarting only this relay, never Codex.
    subprocess.run(['systemctl', '--user', 'restart', service], check=True, timeout=10)
    return dict(status='installed; Codex hook trust/loading must be checked', **remote)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--entry', type=Path, required=True)
    parser.add_argument('--project', required=True)
    parser.add_argument('--session-key', required=True)
    parser.add_argument('--data-dir', type=Path, required=True)
    args = parser.parse_args()
    print(json.dumps(provision(args.entry, args.project, args.session_key, args.data_dir), ensure_ascii=False))

if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as exc:
        print('container-provision: ' + str(exc), file=sys.stderr)
        sys.exit(1)
