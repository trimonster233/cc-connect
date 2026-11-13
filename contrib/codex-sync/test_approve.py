import json
from pathlib import Path
import subprocess
import sys
import unittest
from unittest.mock import patch
import approve


class ApprovalTests(unittest.TestCase):
    def test_unavailable_api_denies_instead_of_approving(self):
        result = subprocess.run([sys.executable, str(Path(approve.__file__)), '--config', '/nonexistent/approval-config'], input='{}', text=True, capture_output=True)
        self.assertEqual(result.returncode, 0)
        self.assertEqual(json.loads(result.stdout)['hookSpecificOutput']['decision']['behavior'], 'deny')

    def test_decision_is_returned_from_api(self):
        config = {'data_dir': '/tmp/test', 'project': 'test', 'session_key': 'chat'}
        with patch.object(approve, 'UnixHTTPConnection') as factory:
            conn = factory.return_value
            conn.getresponse.return_value.status = 200
            conn.getresponse.return_value.read.return_value = b'{"behavior":"allow"}'
            self.assertEqual(approve.request({'session_id': 'sid', 'tool_name': 'Bash', 'tool_input': {'command': 'pwd'}}, config), 'allow')
            self.assertEqual(json.loads(conn.request.call_args.args[2])['tool_input'], {'command': 'pwd'})
            conn.close.assert_called_once()

    def test_invalid_decision_is_rejected(self):
        with patch.object(approve, 'UnixHTTPConnection') as factory:
            factory.return_value.getresponse.return_value.status = 200
            factory.return_value.getresponse.return_value.read.return_value = b'{"behavior":"always"}'
            with self.assertRaises(ValueError):
                approve.request({}, {'data_dir': '/tmp', 'project': 'p', 'session_key': 's'})
