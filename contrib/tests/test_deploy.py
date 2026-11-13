import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

SOURCE = Path(__file__).resolve().parents[1] / 'deploy.py'
spec = importlib.util.spec_from_file_location('deploy', SOURCE)
deploy = importlib.util.module_from_spec(spec)
spec.loader.exec_module(deploy)


class DeployTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.home = Path(self.temp.name)
        self.codex = self.home / '.codex'
        self.config = self.home / 'reply.json'
        self.config.write_text('{}')

    def test_launcher_uses_repository_source_after_update_without_reinstall(self):
        repo = self.home / 'repo'
        source = repo / 'contrib/codex-sync/sync_reply.py'
        source.parent.mkdir(parents=True)
        source.write_text('print("version-one")')
        launcher = deploy.register_sync(repo, self.home, self.codex, self.config)
        self.assertEqual(subprocess.check_output([sys.executable, str(launcher)], text=True).strip(), 'version-one')
        source.write_text('print("version-two")')
        self.assertEqual(subprocess.check_output([sys.executable, str(launcher)], text=True).strip(), 'version-two')

    def test_registration_replaces_managed_hooks_preserves_others_and_is_idempotent(self):
        self.codex.mkdir()
        path = self.codex / 'hooks.json'
        path.write_text(json.dumps({'hooks': {'Stop': [{'hooks': [
            {'command': '/bin/bash /old/.codex/hooks/feishu-stop.sh'},
            {'command': 'keep-existing-hook'},
        ]}], 'PermissionRequest': [{'hooks': [{'command': 'keep-approval'}]}]}}))
        for _ in range(2):
            deploy.register_sync(self.home / 'repo', self.home, self.codex, self.config)
        data = json.loads(path.read_text())
        stops = [hook for group in data['hooks']['Stop'] for hook in group['hooks']]
        self.assertEqual(len(stops), 2)
        self.assertEqual(stops[0]['command'], 'keep-existing-hook')
        self.assertEqual(data['hooks']['PermissionRequest'][0]['hooks'][0]['command'], 'keep-approval')
        self.assertEqual(path.stat().st_mode & 0o777, 0o600)

    def test_stop_refuses_unrelated_pid_without_signalling_it(self):
        (self.home / 'service.pid').write_text(str(os.getpid()))
        with patch.object(deploy.os, 'kill') as kill:
            with self.assertRaises(RuntimeError):
                deploy.stop_previous_bridge(self.home, self.home / 'unrelated-config.toml')
            kill.assert_not_called()

    def test_systemd_paths_are_quoted_without_shell_interpolation(self):
        text = deploy.unit_text(Path('/repo with spaces'), Path('/config%name'), Path('/bin/path'), self.home)
        self.assertIn('WorkingDirectory="/repo with spaces"', text)
        self.assertIn('--config "/config%%name"', text)

    @unittest.skipIf(sys.version_info < (3, 11), 'deployment TOML parsing needs Python 3.11+')
    def test_single_command_builds_binary_installs_hook_and_reuses_saved_target(self):
        config = self.home / 'config.toml'
        data = self.home / 'data'
        config.write_text('data_dir = ' + json.dumps(str(data)) + '\n[[projects]]\nname="test"\n[projects.agent]\ntype="codex"\n')
        go = self.home / 'fake-go'
        go.write_text('#!' + sys.executable + '\nimport pathlib,sys\np=pathlib.Path(sys.argv[sys.argv.index("-o")+1]);p.write_text("fake binary")\n')
        go.chmod(0o700)
        env = dict(os.environ, HOME=str(self.home), CODEX_HOME=str(self.codex))
        args = [sys.executable, str(SOURCE), '--config', str(config), '--go', str(go)]
        subprocess.run(args + ['--sync-session', 'feishu:test:owner'], env=env, check=True, capture_output=True)
        subprocess.run(args, env=env, check=True, capture_output=True)
        self.assertEqual((self.home / '.local/bin/cc-connect').read_text(), 'fake binary')
        self.assertEqual(json.loads((data / 'reply-sync.json').read_text())['session_key'], 'feishu:test:owner')
        self.assertTrue((self.home / '.config/systemd/user/cc-connect-test.service').exists())
        hooks = json.loads((self.codex / 'hooks.json').read_text())['hooks']['Stop']
        self.assertEqual(len(hooks), 1)


if __name__ == '__main__':
    unittest.main()
