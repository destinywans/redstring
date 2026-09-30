import io
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import faults
from env import _private
from store import Store


class FaultProtectionTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        owner = "testlab-abcdef1234"
        work = self.root / ".local/testlab/environments" / owner
        dsn = "postgres://testlab:synthetic-private-password@127.0.0.1:15461/aiops_testlab_app"
        self.environment = dict(owner=owner, work=str(work), container="aiops-" + owner + "-pg", env={"TESTLAB_APP_DSN": dsn})
        self.data = dict(owner=owner, work=str(work / "backend"), binary=str(work / "backend/aiops"), database_url=dsn,
                         postgres_container=self.environment["container"], base_urls=list(faults.BASES), environment="test", other_environment="staging",
                         supervisor_pid=12345, source_tree_sha="abc", control_secret="private-synthetic-control")
        self.current = dict(owner=owner, pid=12345, api_pids=[23456, 34567], ready=True, source_tree_sha="abc")

    def test_fixture_exact_owner_database_binary_and_origins(self):
        self.assertEqual(faults.validate_fixture(self.root, self.environment, self.data), self.data)
        for key, value in [("owner", "foreign"), ("database_url", "postgres://user:pw@db.example.com/business"),
                           ("binary", "/bin/sleep"), ("work", str(self.root)), ("base_urls", ["https://example.com"]),
                           ("environment", "prod"), ("supervisor_pid", True), ("control_secret", "")]:
            with self.subTest(key=key), self.assertRaises(faults.Blocked):
                faults.validate_fixture(self.root, self.environment, dict(self.data, **{key: value}))

    def test_private_fixture_rejects_symlink_and_broad_permissions(self):
        path = self.root / "fixture.json"
        _private(path, self.data)
        self.assertEqual(faults.private_json(path)["owner"], self.data["owner"])
        os.chmod(path, 0o644)
        with self.assertRaises(faults.Blocked):
            faults.private_json(path)
        link = self.root / "alias.json"
        link.symlink_to(path)
        with self.assertRaises(faults.Blocked):
            faults.private_json(link)

    def test_identity_rejects_reused_pid_changed_owner_source_or_dead_child(self):
        self.assertEqual(faults.validate_identity(self.data, self.current, 1), 34567)
        for changed in [None, dict(self.current, owner="other"), dict(self.current, pid=12346), dict(self.current, ready=False),
                        dict(self.current, source_tree_sha="old"), dict(self.current, api_pids=[23456, 23456]),
                        dict(self.current, api_pids=[True, 34567]), dict(self.current, api_pids=[23456])]:
            with self.subTest(identity=changed), self.assertRaises(faults.Blocked):
                faults.validate_identity(self.data, changed)
        with self.assertRaises(faults.Blocked):
            faults.validate_identity(self.data, self.current, 2)

    def test_fault_request_only_registered_pid_with_authenticated_controller(self):
        with patch.object(faults, "exclusive_runs"), patch.object(faults, "attest", return_value=(self.data, self.current)), patch.object(faults, "request", return_value=(202, {"pid": 34567, "signal": "SIGKILL"}, [])) as sender:
            self.assertEqual(faults.fire(self.root, self.data, "SIGKILL", 1), 34567)
            args, kwargs = sender.call_args
            self.assertEqual(args[:3], (faults.CONTROL, "POST", "/fault"))
            self.assertEqual(args[3], dict(owner=self.data["owner"], supervisor_pid=12345, api_pid=34567, signal="SIGKILL"))
            self.assertEqual(kwargs["bearer"], self.data["control_secret"])

    def test_changed_identity_never_sends_signal_request(self):
        with patch.object(faults, "exclusive_runs"), patch.object(faults, "attest", return_value=(dict(self.data, supervisor_pid=99999), self.current)), patch.object(faults, "request") as sender:
            with self.assertRaises(faults.Blocked):
                faults.fire(self.root, self.data, "SIGTERM")
            sender.assert_not_called()
        with patch.object(faults, "request") as sender:
            with self.assertRaises(faults.Blocked):
                faults.fire(self.root, self.data, "SIGSTOP")
            sender.assert_not_called()

    def test_unknown_controller_receipt_is_failure_not_pass(self):
        with patch.object(faults, "exclusive_runs"), patch.object(faults, "attest", return_value=(self.data, self.current)), patch.object(faults, "request", return_value=(202, {"pid": 99999, "signal": "SIGTERM"}, [])):
            with self.assertRaises(AssertionError):
                faults.fire(self.root, self.data, "SIGTERM")

    def test_parallel_active_run_blocks_even_before_target_selection(self):
        store = Store(self.root)
        run = store.create_run()
        store.update("runs", run["id"], status="running", pid=99999)
        with self.assertRaises(faults.Blocked):
            faults.exclusive_runs(self.root, own_pid=12345)
        faults.exclusive_runs(self.root, own_pid=99999)
        with patch.object(faults, "attest") as attest:
            with self.assertRaises(faults.Blocked):
                faults.fire(self.root, self.data, "SIGTERM")
            attest.assert_not_called()

    def test_fault_window_requires_both_opt_in_and_serial_flag(self):
        with patch.dict(os.environ, {}, clear=True):
            with self.assertRaises(faults.Blocked), faults.serial_window(self.root, True):
                pass
        with patch.dict(os.environ, {"AIOPS_TESTLAB_FAULTS_ENABLED": "dedicated-temp-container"}):
            with self.assertRaises(faults.Blocked), faults.serial_window(self.root, False):
                pass
            with faults.serial_window(self.root, True):
                with self.assertRaises(faults.Blocked), faults.serial_window(self.root, True):
                    pass

    def test_request_fixed_origin_and_no_redirect_transport(self):
        with self.assertRaises(faults.Blocked):
            faults.request("http://example.com", "GET", "/identity")
        with self.assertRaises(faults.Blocked):
            faults.request(faults.CONTROL, "GET", "//example.com/identity")
        # http.client follows no redirects and never forwards the control secret.
        with patch.object(faults.http.client, "HTTPConnection") as constructor:
            response = constructor.return_value.getresponse.return_value
            response.status = 302
            response.read.return_value = b"{}"
            response.getheaders.return_value = [("Location", "https://foreign.example")]
            self.assertEqual(faults.request(faults.CONTROL, "GET", "/identity")[0], 302)
            constructor.assert_called_once()
            constructor.return_value.close.assert_called_once()

    def write_exits(self, rows):
        path = Path(self.data["work"]) / "process-exits.jsonl"
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text("".join(json.dumps(r) + "\n" for r in rows))
        os.chmod(path, 0o600)

    def test_exit_proof_requires_exact_pid_time_exit_and_no_forced_kill(self):
        entry = dict(pid=23456, at=100, returncode=0, forced_kill=False)
        self.write_exits([entry])
        faults.exit_proof(self.data, 23456, "SIGTERM", 99)
        for changed in [dict(entry, pid=99999), dict(entry, at=98), dict(entry, forced_kill=True), dict(entry, returncode=-9)]:
            self.write_exits([changed])
            with self.assertRaises(AssertionError):
                faults.exit_proof(self.data, 23456, "SIGTERM", 99)
        self.write_exits([dict(entry, returncode=-9)])
        faults.exit_proof(self.data, 23456, "SIGKILL", 99)

    def test_chat_requires_completed_nonempty_content_and_failed_is_not_pass(self):
        self.assertFalse(faults.completed_messages({"messages": []}))
        self.assertFalse(faults.completed_messages({"messages": [{"role": "assistant", "state": "streaming", "parts": [1]}]}))
        self.assertFalse(faults.completed_messages({"messages": [{"role": "assistant", "state": "completed", "parts": []}]}))
        with self.assertRaises(AssertionError):
            faults.completed_messages({"messages": [{"role": "assistant", "state": "failed"}]})
        self.assertTrue(faults.completed_messages({"messages": [{"role": "assistant", "state": "completed", "parts": [1]}]}))

    def test_failed_exit_assertion_restores_owned_runtime_and_preserves_first_failure(self):
        with patch.object(faults, "fire", return_value=23456), patch.object(faults, "wait_down", return_value=1), patch.object(faults, "exit_proof", side_effect=AssertionError("first exit failure")), patch.object(faults, "restart_owned", return_value=self.data) as restart:
            with self.assertRaisesRegex(AssertionError, "first exit failure"):
                faults.fault_cycle(self.root, self.data, "SIGTERM")
            restart.assert_called_once_with(self.root, self.data)
        with patch.object(faults, "fire", side_effect=faults.Blocked("wrong owner")), patch.object(faults, "restart_owned") as restart:
            with self.assertRaises(faults.Blocked):
                faults.fault_cycle(self.root, self.data, "SIGTERM")
            restart.assert_not_called()

    def test_replay_strict_cursor_and_bounded_frames(self):
        payload = b'id: 11\ndata: {"type":"text.delta","delta":"prefix"}\n\nid: 14\ndata: {"type":"message.completed"}\n\n'
        import time
        rows = faults.read_replay(io.BytesIO(payload), 10, time.monotonic() + 1)
        self.assertEqual([r["seq"] for r in rows], [11, 14])
        for invalid in [b'data: {}\n', b'id: 10\ndata: {}\n', b'id: 11\ndata: {}\nid: 11\ndata: {}\n', b"x" * 65537]:
            with self.subTest(payload=invalid[:50]), self.assertRaises(AssertionError):
                faults.read_replay(io.BytesIO(invalid), 10, time.monotonic() + 1)

    def recovery_fixture(self):
        active = dict(conversation_id="C-own", reply_id="R-own", assistant_id="A-own", cursor=2, prefix_delta="seen")
        full = [dict(seq=1, event={"type": "message.started"}),
                dict(seq=2, event={"type": "text.delta", "message_id": "A-own", "delta": "seen"}),
                dict(seq=3, event={"type": "text.delta", "message_id": "A-own", "delta": " later-persisted"}),
                dict(seq=4, event={"type": "part.added", "part": {"type": "status", "text": "Model reply failed"}}),
                dict(seq=5, event={"type": "message.completed", "message_id": "A-own"})]
        message = dict(id="A-own", role="assistant", state="failed", streaming=False,
                       parts=[{"type": "text", "markdown": "seen later-persisted"}, {"type": "status", "text": "Model reply failed"}])
        return active, full, message

    def test_interruption_failure_is_not_confused_with_completed_sse_envelope(self):
        active, _, message = self.recovery_fixture()
        self.assertEqual(faults.check_recovered_message({"messages": [message]}, active), message)
        self.assertIsNone(faults.check_recovered_message({"messages": [dict(message, state="streaming")]}, active))
        for invalid in [dict(message, state="completed"), dict(message, parts=[]), dict(message, streaming=True), dict(message, state="unknown")]:
            with self.assertRaises(AssertionError):
                faults.check_recovered_message({"messages": [invalid]}, active)

    def test_last_event_id_suffix_retains_terminal_failure_and_all_persisted_deltas(self):
        active, full, message = self.recovery_fixture()
        # The client received only 'seen'; additional committed deltas before
        # process cancellation remain valid and are required in durable replay.
        faults.check_replay(full, full[2:], [], active, message)
        for all_rows, suffix, reconnect, msg in [(full, full[3:], [], message),
                                                (full[:-1], full[2:-1], [], message),
                                                (full, full[2:], full[-1:], message),
                                                (full, full[2:], [], dict(message, parts=[{"type": "text", "markdown": "seen"}]))]:
            with self.assertRaises(AssertionError):
                faults.check_replay(all_rows, suffix, reconnect, active, msg)

    def test_provider_capture_rejects_restart_model_duplicate(self):
        active, _, _ = self.recovery_fixture()
        active["capture_offset"] = 0
        path = Path(self.data["work"]).parent / "captures.jsonl"
        path.parent.mkdir(parents=True, exist_ok=True)
        row = dict(owner=self.data["owner"], kind="model_fault_stream", payload={"phase": "started", "evidence_mode": "protocol_mock"})
        path.write_text(json.dumps(row) + "\n")
        os.chmod(path, 0o600)
        faults.provider_calls(self.data, active)
        path.write_text((json.dumps(row) + "\n") * 2)
        with self.assertRaises(AssertionError):
            faults.provider_calls(self.data, active)

    def test_read_only_observation_proves_reply_lease_terminal_and_audit_together(self):
        active, _, _ = self.recovery_fixture()
        proof = dict(reply_state="failed", message_state="failed", error="reply interrupted after lease expiry", finished=True,
                     lease_cleared=True, active_cleared=True, terminals=1, failed_audits=1)
        from types import SimpleNamespace
        with patch.object(faults, "attest", return_value=(self.data, self.current)), patch.object(faults.subprocess, "run", return_value=SimpleNamespace(returncode=0, stdout=json.dumps(proof))) as query:
            self.assertEqual(faults.observe_reply(self.root, self.data, active), proof)
            args, kwargs = query.call_args
            self.assertEqual(args[0][:4], ["docker", "exec", "-i", self.data["postgres_container"]])
            self.assertIn("BEGIN READ ONLY;", kwargs["input"])
            self.assertNotIn("INSERT", kwargs["input"])
            self.assertNotIn("UPDATE", kwargs["input"])
            for changed in [dict(proof, reply_state="running"), dict(proof, active_cleared=False), dict(proof, terminals=2), dict(proof, failed_audits=0)]:
                query.return_value = SimpleNamespace(returncode=0, stdout=json.dumps(changed))
                with self.assertRaises(AssertionError):
                    faults.observe_reply(self.root, self.data, active)
            with self.assertRaises(faults.Blocked):
                faults.observe_reply(self.root, self.data, dict(active, reply_id="r'; DROP TABLE users; --"))

    def test_error_protocol_never_echoes_dependency_secret(self):
        output = io.StringIO()
        with patch.object(sys, "argv", ["faults.py", "--serial-window"]), patch.object(faults, "execute", side_effect=RuntimeError("private-db-password")), contextlib_redirect(output):
            self.assertEqual(faults.main(), 1)
        row = json.loads(output.getvalue())
        self.assertEqual(row["status"], "failed")
        self.assertNotIn("private-db-password", output.getvalue())


def contextlib_redirect(output):
    from contextlib import redirect_stdout
    return redirect_stdout(output)


if __name__ == "__main__":
    unittest.main()
