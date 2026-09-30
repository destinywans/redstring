import http.client
import json
import os
from pathlib import Path
import sys
import tempfile
import threading
import time
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from runner import Runner, command_env
from server import make_server
from store import Store, MAX_LOG_BYTES, render_report, scrub


class StoreTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.store = Store(self.tmp.name)
        self.run = self.store.create_run("abc", "test")
        self.suite = self.store.add_suite(self.run["id"], {"id": "x"})

    def case(self, status):
        case = self.store.add_case(self.run["id"], self.suite["id"], name="case")
        attempt = self.store.start_attempt(case)
        self.store.finish_attempt(case, attempt, status, "actual")
        return case, attempt

    def test_failed_and_skip_are_not_passed_and_old_attempt_cannot_change(self):
        case, attempt = self.case("failed")
        self.case("skipped")
        self.case("external_todo")
        self.assertEqual(self.store.detail(self.run["id"])["summary"]["passed"], 0)
        with self.assertRaises(ValueError):
            self.store.update("attempts", attempt["id"], status="passed")
        second = self.store.start_attempt(case)
        self.store.finish_attempt(case, second, "passed", "fixed")
        case = self.store.detail(self.run["id"])["cases"][0]
        self.assertEqual([a["status"] for a in case["attempts"]], ["failed", "passed"])

    def test_dead_process_marks_running_and_queued_interrupted(self):
        case = self.store.add_case(self.run["id"], self.suite["id"], name="running")
        self.store.start_attempt(case)
        self.store.update("runs", self.run["id"], status="running", pid=999999999)
        self.store.reconcile_dead_runs()
        result = self.store.detail(self.run["id"])
        self.assertEqual(result["run"]["status"], "interrupted")
        self.assertEqual(result["cases"][0]["attempts"][0]["status"], "interrupted")

    def test_evidence_redacted_bounded_and_not_traversable(self):
        ident = self.store.write_evidence(self.run["id"], "password=hunter22 Bearer abcd.1234 https://user:secret77@host " + "x" * (MAX_LOG_BYTES * 2))
        raw, _ = self.store.read_evidence(ident)
        self.assertNotIn(b"hunter22", raw)
        self.assertNotIn(b"secret77", raw)
        self.assertNotIn(b"abcd.1234", raw)
        self.assertLess(len(raw), MAX_LOG_BYTES + 256)
        with self.assertRaises(KeyError):
            self.store.read_evidence("../state.sqlite3")
        target = self.store.evidence_dir / (ident + ".txt")
        target.unlink()
        target.symlink_to(self.store.database)
        with self.assertRaises(KeyError):
            self.store.read_evidence(ident)

    def test_multiple_cookie_values_and_structured_secret_never_persist(self):
        ident = self.store.write_evidence(self.run["id"], "Cookie: sid=abc123; session=secret456\nSet-Cookie: sid=another789; HttpOnly\n")
        raw, _ = self.store.read_evidence(ident)
        for secret in (b"abc123", b"secret456", b"another789"):
            self.assertNotIn(secret, raw)
        self.store.add_record(self.run["id"], "environment", {"authorization": "secret-top-level", "nested": {"password": "secret-nested"}})
        record = self.store.detail(self.run["id"])["environment"][0]
        self.assertEqual(record["authorization"], "[REDACTED]")
        self.assertEqual(record["nested"]["password"], "[REDACTED]")

    def test_events_resume_and_report_html_escaped(self):
        self.case("failed")
        events = self.store.events(self.run["id"])
        tail = self.store.events(self.run["id"], events[-2]["id"])
        self.assertEqual([e["id"] for e in tail], [events[-1]["id"]])
        self.store.add_record(self.run["id"], "defects", {"name": "<script>alert(1)</script>"})
        self.assertNotIn("<script>alert", render_report(self.store.detail(self.run["id"]), "html"))

    def test_detail_shows_latest_events_without_changing_resume(self):
        first = self.store.events(self.run["id"])[0]["id"]
        last = first
        for index in range(220):
            last = self.store.emit(self.run["id"], "progress", {"index": index})
        events = self.store.detail(self.run["id"])["events"]
        self.assertEqual(len(events), 200)
        self.assertEqual(events[-1]["id"], last)
        self.assertGreater(events[0]["id"], first)
        self.assertEqual(self.store.events(self.run["id"], first, limit=1)[0]["id"], first+1)


class RunnerTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        (self.root / "tools/testlab").mkdir(parents=True)

    def run_fixture(self, lines, parser="go_json", exit_code=0, extra=None):
        fixture = self.root / "fixture.py"
        fixture.write_text("import json\n" + "\n".join("print(" + repr(json.dumps(line)) + ", flush=True)" for line in lines) + f"\nraise SystemExit({exit_code})\n")
        command = {"argv": [sys.executable, str(fixture)], "parser": parser, "timeout_seconds": 5}
        command.update(extra or {})
        manifest = self.root / "tools/testlab/suites.json"
        manifest.write_text(json.dumps({"suites": [{"id": "fixture", "commands": [command]}]}))
        runner = Runner(self.root)
        with patch("runner.source_fingerprint", return_value="fixture-source"):
            return runner.run("fixture")

    def test_go_results_respect_skip_and_package_failure(self):
        detail = self.run_fixture([
            {"Action": "run", "Package": "p", "Test": "TestSkipped"},
            {"Action": "output", "Package": "p", "Test": "TestSkipped", "Output": "needs external config"},
            {"Action": "skip", "Package": "p", "Test": "TestSkipped"},
            {"Action": "fail", "Package": "p"},
        ], exit_code=1)
        self.assertEqual(detail["summary"]["skipped"], 1)
        self.assertGreater(detail["summary"]["failed"], 0)
        self.assertEqual(detail["run"]["status"], "failed")

    def test_empty_go_stream_and_missing_command_not_green(self):
        detail = self.run_fixture([])
        self.assertEqual(detail["run"]["status"], "blocked")
        detail = self.run_fixture([], parser="command", extra={"argv": ["/nonexistent-testlab-command"]})
        self.assertEqual(detail["run"]["status"], "blocked")
        self.assertEqual(detail["summary"]["running"], 0)

    def test_custom_result_and_metric(self):
        detail = self.run_fixture([{"id": "a", "name": "HTTP response", "status": "passed", "module": "api", "actual": "200", "metrics": {"p95": 10}}, {"id": "b", "name": "external", "status": "external_todo", "actual": "tenant missing"}], parser="testlab_json")
        self.assertEqual(detail["summary"]["passed"], 1)
        self.assertEqual(detail["summary"]["external_todo"], 1)
        self.assertEqual(detail["metrics"][0]["p95"], 10)
        self.assertEqual(detail["run"]["status"], "blocked")

    def test_running_progress_survives_detail_reload(self):
        detail = self.run_fixture([{"name": "持续验收", "status": "running", "elapsed": 63, "planned_seconds": 86400, "actual": "窗口未完成"}, {"id":"soak", "name":"持续验收", "status":"blocked", "actual":"fixture intentionally stopped"}], parser="testlab_json")
        run = detail["run"]
        self.assertEqual(run["current_test"], "持续验收")
        self.assertEqual(run["elapsed"], 63)
        self.assertEqual(run["planned_seconds"], 86400)
        self.assertTrue(run["last_progress"])

    def test_nested_gitops_timing_is_persisted_without_a_fake_completed_case(self):
        detail = self.run_fixture([
            {"name": "gitops.final_receipt", "status": "running", "actual": {"elapsed_seconds": 17, "planned_seconds": 240}},
            {"id": "receipt", "name": "gitops.final_receipt", "status": "failed", "actual": "deadline reached"},
        ], parser="testlab_json", exit_code=1)
        self.assertEqual(detail["run"]["elapsed"], 17)
        self.assertEqual(detail["run"]["planned_seconds"], 240)
        self.assertEqual(detail["summary"]["passed"], 0)
        self.assertEqual(detail["summary"]["failed"], 2)  # receipt plus parent command
        progress = [e for e in detail["events"] if e.get("type") == "progress"]
        self.assertEqual(len(progress), 1)

    def test_environment_not_inherited_and_expansion_explicit(self):
        with patch.dict(os.environ, {"TEST_DATABASE_URL": "production", "AIOPS_ENABLE_EXECUTION": "true"}):
            env = command_env({"TEST_DATABASE_URL": "${TEMP_DSN}"}, {"TEMP_DSN": "isolated"})
        self.assertEqual(env["TEST_DATABASE_URL"], "isolated")
        self.assertNotIn("AIOPS_ENABLE_EXECUTION", env)
        with self.assertRaises(ValueError):
            command_env({"TEST_DATABASE_URL": "${MISSING}"}, {})

    def test_timeout_never_reports_pass(self):
        detail = self.run_fixture([], parser="command", extra={"argv": [sys.executable, "-c", "import time;time.sleep(20)"], "timeout_seconds": .1})
        self.assertEqual(detail["run"]["status"], "failed")
        self.assertEqual(detail["cases"][0]["actual"], "命令超时")

    def test_running_process_stop_preserves_interrupted_attempt(self):
        manifest = self.root / "tools/testlab/suites.json"
        manifest.write_text(json.dumps({"suites": [{"id": "fixture", "commands": [{"argv": [sys.executable, "-c", "import time; time.sleep(20)"], "timeout_seconds": 30}]}]}))
        runner = Runner(self.root)
        result = []
        thread = threading.Thread(target=lambda: result.append(runner.run("fixture")))
        thread.start()
        for _ in range(100):
            if runner.process:
                break
            time.sleep(.01)
        runner.request_stop()
        thread.join(timeout=5)
        self.assertFalse(thread.is_alive())
        self.assertEqual(result[0]["run"]["status"], "interrupted")
        self.assertEqual(result[0]["cases"][0]["attempts"][0]["status"], "interrupted")

    def test_external_empty_suite_preserved_not_pass(self):
        manifest = self.root / "tools/testlab/suites.json"
        manifest.write_text(json.dumps({"suites": [{"id": "fixture", "commands": [], "status": "external_todo", "reason": "missing tenant"}]}))
        result = Runner(self.root).run("fixture")
        self.assertEqual(result["summary"]["external_todo"], 1)
        self.assertEqual(result["summary"]["passed"], 0)


class ServerTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.server = make_server(self.tmp.name, 0)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)
        self.store = Store(self.tmp.name)
        self.run = self.store.create_run("abc", "test")

    def request(self, method, path, headers=None):
        conn = http.client.HTTPConnection("127.0.0.1", self.server.server_port, timeout=3)
        conn.request(method, path, headers=headers or {})
        res = conn.getresponse()
        body = res.read()
        conn.close()
        return res.status, body

    def test_readonly_and_origin_and_traversal(self):
        status, body = self.request("GET", "/api/runs")
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(body)["items"][0]["id"], self.run["id"])
        self.assertEqual(self.request("POST", "/api/runs")[0], 405)
        self.assertEqual(self.request("GET", "/api/runs", {"Host": "evil.example"})[0], 403)
        self.assertEqual(self.request("GET", "/api/runs", {"Origin": "https://evil.example"})[0], 403)
        self.assertEqual(self.request("GET", "/api/evidence/%2e%2e%2fstate.sqlite3")[0], 404)

    def test_sse_last_event_id_resume(self):
        first = self.store.events(self.run["id"])[-1]["id"]
        second = self.store.emit(self.run["id"], "progress", {"stage": "two"})
        conn = http.client.HTTPConnection("127.0.0.1", self.server.server_port, timeout=3)
        conn.request("GET", f"/api/events?run_id={self.run['id']}&after=0", headers={"Last-Event-ID": str(first)})
        res = conn.getresponse()
        self.assertEqual(res.status, 200)
        line = res.readline().decode()
        self.assertEqual(line, f"id: {second}\n")
        res.close()
        conn.close()

    def test_reports_download_same_persisted_run(self):
        run_id = self.run["id"]
        for fmt in ("json", "html", "md"):
            status, body = self.request("GET", f"/api/runs/{run_id}/report.{fmt}")
            self.assertEqual(status, 200)
            self.assertIn(run_id.encode(), body)
            self.assertIn(b"abc", body)
        self.assertEqual(self.request("GET", "/api/runs/00000000-0000-0000-0000-000000000000/report.json")[0], 404)
        self.assertEqual(self.request("GET", f"/api/runs/{run_id}/report.pdf")[0], 404)


if __name__ == "__main__":
    unittest.main()
