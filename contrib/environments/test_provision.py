import base64
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from provision import INSTALL

class ProvisionTests(unittest.TestCase):
    def test_install_preserves_other_hooks_and_is_idempotent(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            hook = home / 'hooks.json'
            original = {'hooks': {'Stop': [{'hooks': [{'type': 'command', 'command': 'echo existing'}]}]}}
            hook.write_text(json.dumps(original))
            payload = {'codex_home': str(home), 'codex_binary': '/bin/true', 'environment': 'dev', 'project': 'test', 'session_key': 'test:owner', 'files': {'sync_reply.py': base64.b64encode(b'# test').decode()}}
            first = subprocess.run([sys.executable, '-c', INSTALL], input=json.dumps(payload), text=True, capture_output=True, check=True)
            self.assertTrue(json.loads(first.stdout)['hooks_changed'])
            before = hook.read_text()
            second = subprocess.run([sys.executable, '-c', INSTALL], input=json.dumps(payload), text=True, capture_output=True, check=True)
            self.assertFalse(json.loads(second.stdout)['hooks_changed'])
            self.assertEqual(before, hook.read_text())
            self.assertIn('echo existing', before)
            self.assertEqual((home / 'cc-connect-relay/dev/config.json').stat().st_mode & 0o777, 0o600)
            self.assertEqual(len(list(home.glob('hooks.before-cc-connect-*'))), 1)
