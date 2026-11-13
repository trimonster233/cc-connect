import json
from pathlib import Path
import tempfile
import time
import unittest
from unittest.mock import patch
import relay

SID = '11111111-1111-1111-1111-111111111111'

class RelayTests(unittest.TestCase):
    def test_configured_target_overrides_container_supplied_target(self):
        config = {'project': 'trusted', 'session_key': 'trusted:chat', 'environment': 'dev', 'data_dir': '/tmp/no-relay-data'}
        item = {'path': '/send', 'body': {'project': 'forged', 'session_key': 'forged', 'session_reply': {'session_id': SID, 'content': 'preview', 'environment': 'forged'}}}
        with patch.object(relay.sync, 'UnixHTTPConnection') as factory:
            connection = factory.return_value
            connection.getresponse.return_value.status = 200
            connection.getresponse.return_value.read.return_value = b'{}'
            relay.forward(item, config)
            body = json.loads(connection.request.call_args.args[2])
            self.assertEqual(body['project'], 'trusted')
            self.assertEqual(body['session_key'], 'trusted:chat')
            self.assertEqual(body['session_reply']['environment'], 'dev')
    def test_non_whitelisted_endpoint_is_rejected(self):
        with self.assertRaises(ValueError):
            relay.forward({'path': '/cron/add', 'body': {}}, {})
    def test_bridge_turn_does_not_duplicate_native_reply(self):
        with tempfile.TemporaryDirectory() as directory:
            marker = Path(directory) / 'bridge-turns' / (SID + '.json')
            marker.parent.mkdir()
            marker.write_text(json.dumps({'turn_id': 'turn', 'started_at': time.time()}))
            config = {'project': 'p', 'session_key': 'chat', 'environment': 'dev', 'data_dir': directory}
            item = {'path': '/send', 'body': {'source_turn_id': 'turn', 'session_reply': {'session_id': SID, 'content': 'already sent'}}}
            with patch.object(relay.sync, 'UnixHTTPConnection') as factory:
                self.assertEqual(relay.forward(item, config)[0], 200)
                factory.assert_not_called()
