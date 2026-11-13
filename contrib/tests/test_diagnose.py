import importlib.util
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import MagicMock

SOURCE = Path(__file__).resolve().parents[1] / 'diagnose.py'
spec = importlib.util.spec_from_file_location('diagnose', SOURCE)
diagnose = importlib.util.module_from_spec(spec)
spec.loader.exec_module(diagnose)


class DiagnoseTests(unittest.TestCase):
    def test_old_api_returns_404_without_falling_back_to_send(self):
        conn = MagicMock()
        conn.getresponse.return_value.status = 404
        conn.getresponse.return_value.read.return_value = b'404 page not found'
        collector = SimpleNamespace(UnixHTTPConnection=lambda path: conn)
        result = diagnose.preview(collector, {'data_dir': '/tmp/test', 'project': 'test'})
        self.assertEqual(result['http_status'], 404)
        conn.request.assert_called_once()
        self.assertEqual(conn.request.call_args.args[:2], ('POST', '/reply/preview'))
        conn.close.assert_called_once()

    def test_success_returns_actual_api_identity_and_footer(self):
        conn = MagicMock()
        conn.getresponse.return_value.status = 200
        conn.getresponse.return_value.read.return_value = json.dumps({'pid': 123, 'version': 'version', 'footer': 'rendered'}).encode()
        collector = SimpleNamespace(UnixHTTPConnection=lambda path: conn)
        self.assertEqual(diagnose.preview(collector, {'data_dir': '/tmp/test', 'project': 'test'}), {'pid': 123, 'version': 'version', 'footer': 'rendered'})

    def test_launcher_inspection_does_not_execute_source(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / 'launcher.py'
            path.write_text('import runpy\nraise RuntimeError("must not execute")\nrunpy.run_path("/repo/sync_reply.py", run_name="__main__")\n')
            self.assertEqual(diagnose.launcher_source(path), '/repo/sync_reply.py')


if __name__ == '__main__':
    unittest.main()
