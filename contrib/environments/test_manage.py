import tempfile
from pathlib import Path
import unittest
import manage

class RegistryTests(unittest.TestCase):
    def test_only_explicit_enabled_entries_are_loaded(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            for name, enabled in [('dev', 'true'), ('disabled', 'false')]:
                folder = root / name
                folder.mkdir()
                (folder / 'environment.toml').write_text('name="'+name+'"\ncontainer="'+name+'"\nenabled='+enabled)
            self.assertEqual(list(manage.load(root)), ['dev'])

    def test_custom_script_and_arguments_are_not_shell_interpolated(self):
        result = manage.command({'_directory': Path('/tmp/dev'), 'connect_script': './connect.sh', 'connect_args': ['$(touch bad)']}, 'connect')
        self.assertEqual(result, ['/tmp/dev/connect.sh', '$(touch bad)'])

    def test_default_connect_has_no_tty_and_start_requires_configuration(self):
        entry = {'container': 'dev', 'user': '1000:1000'}
        self.assertEqual(manage.command(entry, 'connect'), ['docker', 'exec', '-i', '--user', '1000:1000', 'dev', 'codex', 'app-server', 'proxy'])
        with self.assertRaises(ValueError):
            manage.command(entry, 'start')
