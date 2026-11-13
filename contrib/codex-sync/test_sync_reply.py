import importlib.util
import json
import os
from pathlib import Path
import tempfile
import time
import unittest
from unittest.mock import patch, MagicMock

spec = importlib.util.spec_from_file_location('sync_reply', Path(__file__).with_name('sync_reply.py'))
sync = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sync)
SID = '11111111-1111-1111-1111-111111111111'


class ReplySyncTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.config = {'data_dir': self.temp.name, 'project': 'test', 'session_key': 'test:owner', 'codex_home': self.temp.name + '/codex'}
        self.payload = {'session_id': SID, 'last_assistant_message': '最终正文', 'turn_id': 'turn'}
        self.env = patch.dict(os.environ, {}, clear=True)
        self.env.start()
        self.addCleanup(self.env.stop)

    def connection(self):
        conn = MagicMock()
        conn.getresponse.return_value.status = 200
        return conn

    def test_external_reply_is_forwarded_without_old_completion_notice(self):
        conn = self.connection()
        with patch.object(sync, 'UnixHTTPConnection', return_value=conn):
            self.assertTrue(sync.forward(self.payload, self.config))
        body = json.loads(conn.request.call_args.args[2])
        self.assertEqual(body, {'project': 'test', 'session_key': 'test:owner', 'message': '最终正文\n\nUUID: ' + SID})

    def test_existing_uuid_and_repeated_stop_are_not_duplicated(self):
        self.payload['last_assistant_message'] += '\n\nUUID: ' + SID
        conn = self.connection()
        with patch.object(sync, 'UnixHTTPConnection', return_value=conn):
            self.assertTrue(sync.forward(self.payload, self.config))
            self.assertFalse(sync.forward(self.payload, self.config))
        conn.request.assert_called_once()
        self.assertEqual(json.loads(conn.request.call_args.args[2])['message'].count('UUID:'), 1)

    def test_bridge_turn_is_not_forwarded_twice(self):
        marker = Path(self.temp.name) / 'bridge-turns' / (SID + '.json')
        marker.parent.mkdir()
        marker.write_text(json.dumps({'started_at': time.time(), 'turn_id': 'turn'}))
        with patch.object(sync, 'UnixHTTPConnection') as dial:
            self.assertFalse(sync.forward(self.payload, self.config))
            dial.assert_not_called()

    def test_bridge_environment_skips_without_reading_state(self):
        for key in ('CC_PROJECT', 'CC_SESSION_KEY'):
            with patch.dict(os.environ, {key: 'test'}), patch.object(sync, 'UnixHTTPConnection') as dial:
                self.assertFalse(sync.forward(self.payload, self.config))
                dial.assert_not_called()

    def test_send_failure_is_reported_and_can_be_retried(self):
        conn = self.connection()
        conn.getresponse.return_value.status = 500
        with patch.object(sync, 'UnixHTTPConnection', return_value=conn):
            with self.assertRaises(RuntimeError):
                sync.forward(self.payload, self.config)
            conn.getresponse.return_value.status = 200
            self.assertTrue(sync.forward(self.payload, self.config))

    def test_empty_reply_or_invalid_uuid_is_not_sent(self):
        for payload in ({'session_id': SID}, {'session_id': '../../bad', 'last_assistant_message': 'text'}):
            with patch.object(sync, 'UnixHTTPConnection') as dial:
                self.assertFalse(sync.forward(payload, self.config))
                dial.assert_not_called()

    def transcript(self, rows):
        path = Path(self.config['codex_home']) / 'sessions' / ('rollout-test-' + SID + '.jsonl')
        path.parent.mkdir(parents=True)
        path.write_text('\n'.join(json.dumps(row) for row in rows) + '\n{"incomplete":')
        return path

    def test_reply_quotes_request_and_shows_actual_turn_directory_and_model(self):
        self.transcript([
            {'type': 'session_meta', 'payload': {'id': SID, 'cwd': '/original'}},
            {'type': 'turn_context', 'payload': {'turn_id': 'turn', 'cwd': '/project/target', 'model': 'target-model'}},
            {'type': 'response_item', 'payload': {'type': 'message', 'role': 'user', 'content': [{'type': 'input_text', 'text': '[Quoted message from Bot]:\nold answer\n\n\n请修改这个功能'}]}},
            {'type': 'turn_context', 'payload': {'turn_id': 'later', 'cwd': '/wrong', 'model': 'wrong-model'}},
            {'type': 'response_item', 'payload': {'type': 'message', 'role': 'user', 'content': [{'type': 'input_text', 'text': 'later question'}]}},
        ])
        conn = self.connection()
        with patch.object(sync, 'UnixHTTPConnection', return_value=conn):
            self.assertTrue(sync.forward(self.payload, self.config))
        text = json.loads(conn.request.call_args.args[2])['message']
        self.assertEqual(text, '> 问题：请修改这个功能\n\n最终正文\n\n目录：/project/target\n模型：target-model\nUUID: ' + SID)

    def test_user_turn_metadata_works_before_turn_context(self):
        self.payload['transcript_path'] = str(self.transcript([
            {'type': 'session_meta', 'payload': {'id': SID}},
            {'type': 'response_item', 'payload': {'type': 'message', 'role': 'user', 'internal_chat_message_metadata_passthrough': {'turn_id': 'turn'}, 'content': [{'type': 'input_text', 'text': 'request before context'}]}},
            {'type': 'turn_context', 'payload': {'turn_id': 'turn', 'cwd': '/project', 'model': 'model'}},
        ]))
        self.assertEqual(sync.turn_details(self.payload, self.config, SID), {'cwd': '/project', 'model': 'model', 'question': 'request before context'})

    def test_wrong_transcript_cannot_supply_another_sessions_question(self):
        self.payload['transcript_path'] = str(self.transcript([
            {'type': 'session_meta', 'payload': {'id': 'another-session', 'cwd': '/wrong'}},
            {'type': 'turn_context', 'payload': {'turn_id': 'turn', 'model': 'wrong-model'}},
        ]))
        self.assertEqual(sync.turn_details(self.payload, self.config, SID), {'cwd': '', 'model': ''})

    def test_reply_chain_is_not_repeated_in_question_excerpt(self):
        chain = '--- Reply chain (3 messages) ---\nold conversation\n---\n\n当前问题\n第二行'
        self.assertEqual(sync.question_excerpt(chain), '当前问题 第二行')
        self.assertEqual(sync.question_excerpt('文' * 200), '文' * 180 + '…')
        self.assertEqual(sync.question_excerpt('[label](url)'), r'\[label\](url)')


if __name__ == '__main__':
    unittest.main()
