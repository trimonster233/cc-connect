import json
import os
from pathlib import Path
import shlex
import subprocess
import sys
import tempfile
import unittest

from configure_shell import configure_shell


class ConfigureShellTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.rcfile = self.root / 'bashrc'
        self.rcfile.write_text('# existing settings\n')
        self.codex_home = self.root / 'codex-state'
        self.server_url = 'unix://' + str(self.codex_home / 'app-server.sock')
        self.cwd = self.root / 'project with spaces'
        self.cwd.mkdir()
        bin_dir = self.root / 'bin'
        bin_dir.mkdir()
        cli = bin_dir / 'codex'
        cli.write_text(f'#!{sys.executable}\n'
                       'import json, os, sys\n'
                       'print(json.dumps(sys.argv[1:]))\n'
                       'sys.exit(int(os.environ.get("CC_CONNECT_TEST_EXIT", "0")))\n')
        cli.chmod(0o700)
        self.env = dict(os.environ, PATH=str(bin_dir) + os.pathsep + os.environ['PATH'])

    def configure(self):
        return configure_shell(self.rcfile, self.codex_home, self.server_url, 'dev')

    def run_cli(self, *args, exit_code=0):
        result = subprocess.run(
            ['bash', '--noprofile', '--norc', '-c',
             'source "$1"\nshift\ncodex "$@"', 'shell-test', str(self.rcfile), *args],
            cwd=self.cwd, env=dict(self.env, CC_CONNECT_TEST_EXIT=str(exit_code)),
            text=True, capture_output=True)
        self.assertEqual(result.returncode, exit_code, result.stderr)
        return json.loads(result.stdout)

    def test_legacy_remote_wrapper_forwards_terminal_cwd_after_upgrade(self):
        self.rcfile.write_text(
            '# CC-Connect dev shared server\n'
            f'export CODEX_HOME={shlex.quote(str(self.codex_home))}\n'
            f'codex() {{ command codex --remote {shlex.quote(self.server_url)} "$@"; }}\n')
        self.assertEqual(self.run_cli(), ['--remote', self.server_url])
        self.configure()
        self.assertEqual(self.run_cli(),
                         ['--remote', self.server_url, '--cd', str(self.cwd)])

    def test_explicit_directory_is_preserved_without_duplicate_flags(self):
        self.configure()
        for args in [('-C', '/tmp'), ('--cd', '/tmp'), ('-C/tmp',),
                     ('-C=/tmp',), ('--cd=/tmp',), ('--cd', '/project with spaces'),
                     ('resume', 'thread-id', '-C', '/tmp')]:
            with self.subTest(args=args):
                self.assertEqual(self.run_cli(*args), ['--remote', self.server_url, *args])

    def test_prompt_after_separator_does_not_override_directory(self):
        self.configure()
        for prompt in ['--cd=/literal-prompt', '-C', 'a prompt with spaces']:
            with self.subTest(prompt=prompt):
                self.assertEqual(self.run_cli('--', prompt),
                                 ['--remote', self.server_url, '--cd', str(self.cwd), '--', prompt])

    def test_install_and_upgrade_preserve_settings_backup_and_file_mode(self):
        original = self.rcfile.read_text()
        self.rcfile.chmod(0o640)
        self.assertTrue(self.configure())
        suffix = '\n# user settings after the managed block\nexport KEEP_SETTING=yes\n'
        self.rcfile.write_text(self.rcfile.read_text() + suffix)
        self.server_url = 'unix:///different-server.sock'
        self.assertTrue(self.configure())
        updated = self.rcfile.read_text()
        self.assertTrue(updated.startswith(original))
        self.assertTrue(updated.endswith(suffix))
        self.assertEqual(updated.count('# CC-Connect dev shared server'), 1)
        self.assertFalse(self.configure())
        self.assertEqual(self.rcfile.read_text(), updated)
        self.assertEqual(self.rcfile.with_suffix('.before-cc-connect').read_text(), original)
        self.assertEqual(self.rcfile.stat().st_mode & 0o777, 0o640)

    def test_custom_unrecognized_block_is_not_overwritten(self):
        original = '# CC-Connect dev shared server\n# manually customized setup\n'
        self.rcfile.write_text(original)
        with self.assertRaisesRegex(ValueError, 'Cannot safely update'):
            self.configure()
        self.assertEqual(self.rcfile.read_text(), original)

    def test_shell_values_are_quoted_and_exit_status_is_preserved(self):
        self.codex_home = self.root / "state with ' quotes"
        self.server_url = 'unix://' + str(self.codex_home / 'app-server.sock')
        self.configure()
        subprocess.run(['bash', '-n', str(self.rcfile)], check=True, capture_output=True)
        self.assertEqual(self.run_cli('quoted prompt', exit_code=37),
                         ['--remote', self.server_url, '--cd', str(self.cwd), 'quoted prompt'])

    def test_invalid_environment_name_does_not_modify_shell(self):
        original = self.rcfile.read_text()
        with self.assertRaisesRegex(ValueError, 'invalid environment name'):
            configure_shell(self.rcfile, self.codex_home, self.server_url, 'bad\nname')
        self.assertEqual(self.rcfile.read_text(), original)


if __name__ == '__main__':
    unittest.main()
