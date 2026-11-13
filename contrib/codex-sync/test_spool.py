import json
from pathlib import Path
import tempfile
import unittest
from spool import SpoolConnection

class SpoolTests(unittest.TestCase):
    def test_response_roundtrip_and_cleanup(self):
        with tempfile.TemporaryDirectory() as directory:
            connection = SpoolConnection(directory)
            connection.request('POST', '/approval/request', '{"tool_name":"Bash"}', {})
            requests = list(Path(directory).glob('*.request'))
            self.assertEqual(len(requests), 1)
            request = json.loads(requests[0].read_text())
            (Path(directory) / (request['id'] + '.response')).write_text(json.dumps({'status': 200, 'body': {'behavior': 'deny'}}))
            self.assertEqual(json.loads(connection.getresponse().read())['behavior'], 'deny')
            connection.close()
            self.assertEqual(list(Path(directory).iterdir()), [])
    def test_timeout_never_approves(self):
        with tempfile.TemporaryDirectory() as directory:
            connection = SpoolConnection(directory)
            connection.timeout = 0
            connection.request('POST', '/approval/request', '{}', {})
            with self.assertRaises(TimeoutError):
                connection.getresponse()
            connection.close()
