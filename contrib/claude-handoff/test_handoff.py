import copy
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest
from types import SimpleNamespace
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("handoff", Path(__file__).with_name("handoff.py"))
handoff = importlib.util.module_from_spec(spec)
spec.loader.exec_module(handoff)
installer_spec = importlib.util.spec_from_file_location("handoff_installer", Path(__file__).with_name("install.py"))
installer = importlib.util.module_from_spec(installer_spec)
installer_spec.loader.exec_module(installer)


class HandoffTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.item = dict(id="12345678-1234-1234-1234-123456789abc", cwd=str(self.root), summary="new")
        self.snapshot = dict(sessions={"s1": dict(id="s1", agent_session_id="old", history=[{"content": "keep"}])},
                             active_session={"chat": "s1"}, user_sessions={"chat": ["s1"]}, counter=1,
                             version=1, past_id_tracking=True)

    def transcript(self, rows):
        path = self.root / (self.item["id"] + ".jsonl")
        path.write_text("\n".join(json.dumps(x, ensure_ascii=False) for x in rows) + '\n{"partial":')
        return path

    def test_attach_preserves_previous_history_and_switches_back(self):
        before = copy.deepcopy(self.snapshot)
        attached = handoff.attach(before, self.item)
        self.assertEqual(attached["sessions"]["s1"], before["sessions"]["s1"])
        self.assertEqual(attached["active_session"]["chat"], "s2")
        self.assertEqual(attached["sessions"]["s2"]["agent_session_id"], self.item["id"])
        again = handoff.attach(attached, self.item)
        self.assertEqual(again["counter"], 2)
        self.assertEqual(again["sessions"]["s1"], before["sessions"]["s1"])
        self.assertEqual(self.snapshot, before)

    def test_instructions_create_new_slot_before_dir_clears_history(self):
        commands = handoff.instructions(self.item).splitlines()
        self.assertEqual(commands, ["/new", "/dir " + self.item["cwd"], "/switch " + self.item["id"]])

    def test_attach_requires_unambiguous_chat(self):
        self.snapshot["active_session"]["other"] = "s1"
        with self.assertRaises(ValueError):
            handoff.attach(self.snapshot, self.item)
        self.assertEqual(handoff.attach(self.snapshot, self.item, "chat")["active_session"]["other"], "s1")

    def test_merge_preserves_independent_stores_with_colliding_ids(self):
        other = copy.deepcopy(self.snapshot)
        other["sessions"]["s1"]["history"] = [{"content": "other project"}]
        merged = handoff.merge_snapshots(self.snapshot, other)
        self.assertEqual(merged["active_session"]["chat"], "s1")
        self.assertEqual([merged["sessions"][s]["history"] for s in merged["user_sessions"]["chat"]],
                         [[{"content": "keep"}], [{"content": "other project"}]])

    def test_merge_does_not_duplicate_older_copied_history(self):
        self.snapshot["sessions"]["s1"]["created_at"] = "today"
        old = copy.deepcopy(self.snapshot)
        self.snapshot["sessions"]["s1"]["history"].append({"content": "next turn"})
        merged = handoff.merge_snapshots(self.snapshot, old)
        self.assertEqual(len(merged["sessions"]), 1)

    def test_catalog_reads_cwd_from_transcript_not_lossy_directory_encoding(self):
        project = self.root / "projects" / "-a-b-c"
        project.mkdir(parents=True)
        path = self.transcript([{"type": "user", "cwd": "/a_b/c-d", "message": {"content": "work"}}])
        path.rename(project / path.name)
        items = handoff.catalog(self.root)
        self.assertEqual(items[0]["cwd"], "/a_b/c-d")
        with self.assertRaises(ValueError):
            handoff.select_session(items + items, "1234")

    def test_installer_does_not_reinstall_legacy_completion_notifications(self):
        updates = installer.installation_files(self.root, "test-config")
        self.assertEqual(set(updates), {
            self.root / ".local/share/cc-handoff/handoff.py",
            self.root / ".local/bin/cc-takeover",
            self.root / ".cc-connect/config.toml",
        })
        self.assertNotIn("--notify", updates[self.root / ".local/share/cc-handoff/handoff.py"])

    def test_session_store_matches_go_hash_and_legacy_path(self):
        import hashlib
        expected = "local-claude_" + hashlib.sha256(str(self.root).encode()).hexdigest()[:8] + ".json"
        self.assertEqual(handoff.session_store(self.root, "local-claude", str(self.root)), self.root / "sessions" / expected)
        (self.root / expected).write_text("{}")
        self.assertEqual(handoff.session_store(self.root, "local-claude", str(self.root)), self.root / expected)

    def test_project_config_scopes_fields_to_selected_project(self):
        config = self.root / "config.toml"
        config.write_text('[[projects]]\nname = "other"\n[projects.agent.options]\nwork_dir = "/other"\n'
                          '[[projects]]\nname = "local-claude"\n[projects.agent]\ntype = "claudecode"\n'
                          '[projects.agent.options]\nwork_dir = "/target" # comment\n')
        self.assertEqual(handoff.project_config(config, "local-claude")["work_dir"], "/target")

    def test_install_admin_from_moves_from_platform_to_project(self):
        config = ('[[projects]]\nname = "local-claude"\nreset_on_idle_mins = 0\n'
                  '[projects.agent]\ntype = "claudecode"\n'
                  '[[projects.platforms]]\ntype = "feishu"\n[projects.platforms.options]\n'
                  'allow_from = "owner"\nadmin_from = "owner"\n'
                  '[[projects]]\nname = "other"\nadmin_from = "another-owner"\n')
        fixed = installer.fix_admin_scope(config)
        project_fields = fixed.split('[projects.agent]')[0]
        self.assertIn('admin_from = "owner"', project_fields)
        platform_fields = fixed.split('[projects.platforms.options]')[1].split('[[projects]]')[0]
        self.assertNotIn('admin_from', platform_fields)
        self.assertIn('allow_from = "owner"', platform_fields)
        self.assertIn('name = "other"\nadmin_from = "another-owner"', fixed)
        self.assertEqual(installer.fix_admin_scope(fixed), fixed)

    def test_install_admin_from_preserves_existing_project_allowlist(self):
        config = ('[[projects]]\nname = "local-claude"\nadmin_from = "project-owner"\n'
                  '[projects.platforms.options]\nadmin_from = "old-owner"\n')
        fixed = installer.fix_admin_scope(config)
        self.assertIn('admin_from = "project-owner"', fixed)
        self.assertNotIn('old-owner', fixed)

    def test_atomic_json_preserves_unicode_and_private_permissions(self):
        path = self.root / "sessions" / "test.json"
        handoff.atomic_json(path, {"reply": "完成"})
        self.assertEqual(json.loads(path.read_text()), {"reply": "完成"})
        self.assertEqual(path.stat().st_mode & 0o777, 0o600)

    def takeover_args(self):
        config = self.root / "config.toml"
        config.write_text('[[projects]]\nname = "local-claude"\n[projects.agent]\n'
                          'type = "claudecode"\n[projects.agent.options]\nwork_dir = "/old"\n')
        source = handoff.session_store(self.root, "local-claude", "/old")
        handoff.atomic_json(source, self.snapshot)
        args = SimpleNamespace(config=config, project="local-claude", data_dir=self.root,
                               session_key=None, dry_run=False, binary=Path("/fake-binary"))
        return args, source

    def test_takeover_reads_history_flushed_during_shutdown(self):
        args, source = self.takeover_args()
        handoff.atomic_json(self.root / "projects/local-claude.state.json", {"session_work_dirs": {"chat": "/old"}})

        def flush(_pid):
            self.snapshot["sessions"]["s1"]["history"].append({"content": "flushed on exit"})
            handoff.atomic_json(source, self.snapshot)

        with patch.object(handoff, "daemon_process", return_value=(os.getpid(), ["binary", "--config", str(args.config)], "/tmp")), \
                patch.object(handoff, "ensure_no_agent_children"), \
                patch.object(handoff, "stop_daemon", side_effect=flush), \
                patch.object(handoff, "start_daemon"), patch("builtins.print"):
            handoff.takeover(args, self.item)
        target = handoff.session_store(self.root, args.project, str(self.root))
        result = json.loads(target.read_text())
        self.assertEqual(result["sessions"]["s1"]["history"][-1]["content"], "flushed on exit")
        self.assertEqual(result["sessions"]["s2"]["agent_session_id"], self.item["id"])
        state = json.loads((self.root / "projects/local-claude.state.json").read_text())
        self.assertEqual(state["work_dir_override"], str(self.root))
        self.assertEqual(state["session_work_dirs"]["chat"], str(self.root))

    def test_takeover_rolls_back_files_and_restarts_original_on_startup_error(self):
        args, source = self.takeover_args()
        before = source.read_bytes()
        with patch.object(handoff, "daemon_process", return_value=(os.getpid(), ["binary", "--config", str(args.config)], "/tmp")), \
                patch.object(handoff, "ensure_no_agent_children"), \
                patch.object(handoff, "stop_daemon"), \
                patch.object(handoff, "start_daemon", side_effect=[RuntimeError("startup failed"), None]) as start, \
                patch("builtins.print"):
            with self.assertRaisesRegex(RuntimeError, "startup failed"):
                handoff.takeover(args, self.item)
        self.assertEqual(source.read_bytes(), before)
        self.assertFalse(handoff.session_store(self.root, args.project, str(self.root)).exists())
        self.assertFalse((self.root / "projects/local-claude.state.json").exists())
        self.assertEqual(start.call_count, 2)


if __name__ == "__main__":
    unittest.main()
