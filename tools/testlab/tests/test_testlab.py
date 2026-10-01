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

    def run_fixture(self, lines, parser="go_json", exit_code=0, extra=None, source_fingerprints=None):
        fixture = self.root / "fixture.py"
        fixture.write_text("import json\n" + "\n".join("print(" + repr(json.dumps(line)) + ", flush=True)" for line in lines) + f"\nraise SystemExit({exit_code})\n")
        command = {"argv": [sys.executable, str(fixture)], "parser": parser, "timeout_seconds": 5}
        command.update(extra or {})
        manifest = self.root / "tools/testlab/suites.json"
        manifest.write_text(json.dumps({"suites": [{"id": "fixture", "commands": [command]}]}))
        runner = Runner(self.root)
        with patch("runner.source_fingerprint", return_value="fixture-source", side_effect=source_fingerprints):
            return runner.run("fixture")

    def completion(self, detail):
        records = [row for row in detail["environment"] if row.get("kind") == "go_command_completion"]
        self.assertEqual(len(records), 1)
        self.assertEqual(len(records[0]["evidence_ids"]), 1)
        store = Store(self.root)
        raw, mime = store.read_evidence(records[0]["evidence_ids"][0])
        self.assertEqual(mime, "application/json")
        return json.loads(raw), raw, store

    def test_go_completion_preserves_actual_nonzero_exit_and_immutable_attempt_links(self):
        detail = self.run_fixture([
            {"Action": "run", "Package": "p", "Test": "TestOne"},
            {"Action": "pass", "Package": "p", "Test": "TestOne"},
            {"Action": "pass", "Package": "p"},
        ], exit_code=3, extra={"env": {"GOFLAGS": "", "GOWORK": "off", "GOENV": "off", "TEST_DSN": "postgres://owned:private-secret@localhost/test"}})
        self.assertEqual(detail["run"]["status"], "failed")
        self.assertEqual(detail["summary"]["passed"], 1)
        self.assertEqual(detail["summary"]["failed"], 1)
        completion, raw, store = self.completion(detail)
        self.assertIs(type(completion["exit_code"]), int)
        self.assertEqual(completion["exit_code"], 3)
        self.assertEqual(completion["go_environment"], {"GOFLAGS": "", "GOWORK": "off", "GOENV": "off"})
        self.assertEqual(completion["cwd"], str(self.root.resolve()))
        self.assertEqual(completion["argv"], [sys.executable, str(self.root / "fixture.py")])
        self.assertEqual((completion["version"], completion["kind"], completion["parser"]), (1, "go_command_completion", "go_json"))
        self.assertEqual(completion["run_id"], detail["run"]["id"])
        self.assertEqual(completion["suite_id"], detail["suites"][0]["id"])
        self.assertFalse(completion["timed_out"] or completion["cancelled"] or completion["malformed"])
        case = next(row for row in detail["cases"] if row["name"] == "TestOne")
        attempt = case["attempts"][0]
        self.assertEqual(completion["case_attempts"], [{"case_id": case["id"], "attempt_id": attempt["id"]}])
        self.assertEqual(attempt["evidence_ids"], [])  # terminal Go attempt remains immutable
        association = next(row for row in detail["environment"] if row.get("kind") == "command_evidence")
        self.assertEqual(association["evidence_id"], completion["command_evidence_id"])
        output, _ = store.read_evidence(completion["command_evidence_id"])
        self.assertTrue(output.startswith(b"\xe5\x91\xbd\xe4\xbb\xa4\xef\xbc\x9a"))
        self.assertNotIn(b"private-secret", raw + output)
        self.assertNotIn(b"TEST_DSN", raw)
        with self.assertRaises(ValueError):
            store.update("attempts", attempt["id"], status="failed")

    def test_go_completion_keeps_unknown_environment_and_command_source_boundaries(self):
        detail = self.run_fixture([
            {"Action": "run", "Package": "p", "Test": "TestOne"},
            {"Action": "run", "Package": "p", "Test": "TestOne/Child"},
            {"Action": "pass", "Package": "p", "Test": "TestOne/Child"},
            {"Action": "pass", "Package": "p", "Test": "TestOne"},
            {"Action": "pass", "Package": "p"},
        ], source_fingerprints=["run-source", "command-before", "command-after", "run-source"])
        self.assertEqual(detail["run"]["status"], "passed")
        self.assertEqual(detail["summary"]["passed"], 2)
        self.assertEqual(len(detail["cases"]), 2)
        completion, _, _ = self.completion(detail)
        self.assertEqual(completion["exit_code"], 0)
        self.assertEqual(completion["go_environment"], {"GOFLAGS": None, "GOWORK": None, "GOENV": None})
        self.assertEqual(completion["source_tree_sha"], "command-before")
        self.assertEqual(completion["source_tree_sha_end"], "command-after")
        self.assertEqual({(row["case_id"], row["attempt_id"]) for row in completion["case_attempts"]},
                         {(case["id"], case["attempts"][0]["id"]) for case in detail["cases"]})
        detail = self.run_fixture([], parser="command")
        self.assertFalse(any(row.get("kind") == "go_command_completion" for row in detail["environment"]))

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

    def test_explicit_environment_exit_two_never_becomes_pass_or_hides_failure(self):
        detail = self.run_fixture([{"id": "ready", "name": "owned environment", "status": "blocked", "actual": "observed OOMKilled"}], parser="testlab_json", exit_code=2)
        self.assertEqual(detail["run"]["status"], "blocked")
        self.assertEqual(detail["summary"]["failed"], 0)
        self.assertEqual(detail["summary"]["blocked"], 2)
        for rows in ([{"id": "ready", "status": "passed"}], [{"id": "ready", "status": "blocked"}, {"id": "assertion", "status": "failed"}]):
            with self.subTest(rows=rows):
                detail = self.run_fixture(rows, parser="testlab_json", exit_code=2)
                self.assertEqual(detail["run"]["status"], "failed")

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

    def test_timeout_leaves_time_for_helper_finally_and_stays_failed(self):
        marker = self.root / "cleanup-completed"
        helper = self.root / "helper.py"
        helper.write_text("import signal,time\nfrom pathlib import Path\n"
                          "def stop(signum,frame): raise KeyboardInterrupt()\n"
                          "signal.signal(signal.SIGTERM,stop)\n"
                          "try: time.sleep(20)\n"
                          "finally:\n time.sleep(3.5)\n Path(" + repr(str(marker)) + ").write_text('done')\n")
        manifest = self.root / "tools/testlab/suites.json"
        manifest.write_text(json.dumps({"suites": [{"id": "fixture", "commands": [{"argv": [sys.executable, str(helper)], "parser": "command", "timeout_seconds": .25, "cleanup_grace_seconds": 5}]}]}))
        with patch("runner.source_fingerprint", return_value="fixture-source"):
            detail = Runner(self.root).run("fixture")
        self.assertTrue(marker.exists())
        self.assertEqual(detail["run"]["status"], "failed")

    def test_flat_http_evidence_becomes_persistent_matrix_cells(self):
        base={'method':'GET','path':'/api/v1/me','expected':{'status':[200]},'actual':{'status':200},'status':'passed'}
        detail=self.run_fixture([
            {'name':'normal','status':'passed','interface':dict(base,scenario='normal')},
            {'name':'denied','status':'passed','interface':dict(base,scenario='authorization',expected={'status':[403]},actual={'status':403})},
        ],parser='testlab_json')
        actual=[row for row in detail['interfaces'] if row.get('evidence_mode')=='real_local']
        self.assertEqual(len(actual),2)
        self.assertEqual(actual[0]['scenarios']['normal']['actual_status'],200)
        self.assertEqual(actual[0]['scenarios']['normal']['status'],'passed')
        self.assertTrue(actual[0]['registration_evidence'])
        self.assertFalse(actual[1]['registration_evidence'])
        self.assertEqual(actual[1]['scenarios']['authorization']['status'],'passed')

    def test_contradictory_interface_evidence_fails_command_audit(self):
        row={'method':'GET','path':'/api/v1/me','scenario':'normal','expected':{'status':[200]},'actual':{'status':500},'status':'passed'}
        detail=self.run_fixture([{'name':'false-positive','status':'passed','interface':row}],parser='testlab_json')
        self.assertEqual(detail['run']['status'],'failed')
        self.assertFalse(any(r.get('evidence_mode')=='real_local' for r in detail['interfaces']))
        self.assertTrue(any(r.get('kind')=='interface_evidence_error' for r in detail['environment']))

    def test_protocol_mock_survives_flat_http_conversion_with_scope(self):
        row={'method':'PUT','path':'/api/v1/security','scenario':'normal',
             'expected':{'status':[200]},'actual':{'status':200},'status':'passed',
             'evidence_mode':'protocol_mock','upstream_mode':'protocol_mock',
             'proof_scope':'HTTP adapter only; real enforcement unverified'}
        detail=self.run_fixture([{'name':'protocol.normal','status':'passed',
                                 'evidence_mode':'protocol_mock','interface':row}],parser='testlab_json')
        actual=[r for r in detail['interfaces'] if r.get('evidence_mode')!='source']
        self.assertEqual(len(actual),1)
        self.assertEqual(actual[0]['evidence_mode'],'protocol_mock')
        self.assertEqual(actual[0]['upstream_mode'],'protocol_mock')
        self.assertEqual(actual[0]['proof_scope'],row['proof_scope'])
        self.assertTrue(actual[0]['registration_evidence'])

    def test_mock_upstream_cannot_claim_real_local_during_conversion(self):
        row={'method':'GET','path':'/api/v1/security','scenario':'normal',
             'expected':{'status':[200]},'actual':{'status':200},'status':'passed',
             'evidence_mode':'real_local','upstream_mode':'protocol_mock'}
        detail=self.run_fixture([{'name':'false-real','status':'passed','interface':row}],parser='testlab_json')
        self.assertEqual(detail['run']['status'],'failed')
        self.assertFalse(any(r.get('evidence_mode')=='real_local' for r in detail['interfaces']))

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

    def test_overall_sse_replays_new_runs_after_persisted_cursor(self):
        cursor = self.store.events(self.run['id'])[-1]['id']
        other = self.store.create_run('def', 'test')
        expected = self.store.events(None, cursor)
        self.assertEqual(expected[0]['run_id'], other['id'])
        conn = http.client.HTTPConnection('127.0.0.1', self.server.server_port, timeout=3)
        conn.request('GET', '/api/overview/events?after=0', headers={'Last-Event-ID': str(cursor)})
        response = conn.getresponse()
        self.assertEqual(response.status, 200)
        self.assertEqual(response.readline().decode(), f"id: {expected[0]['id']}\n")
        self.assertIn(other['id'].encode(), response.readline())
        response.close()
        conn.close()
        self.assertEqual(self.request('HEAD', '/api/overview/events')[0], 405)
        self.assertEqual(self.request('GET', '/api/overview/events?after=-1')[0], 400)

    def test_overall_reports_are_attachments_and_do_not_allow_writes(self):
        import overview
        snapshot = {
            'overall_status': 'incomplete', 'generated_at': 'now', 'next_actions': [],
            'source': {'fingerprint': 'current'},
            'coverage': {
                'main': {'normal_success': 1, 'total': 2, 'missing': 1, 'scope': 'partial'},
                'routes': [], 'auxiliary': {'normal_success': 0, 'routes': []},
            },
            'gates': [], 'work_packages': [], 'gaps': [], 'defects': [],
            'external_todos': [], 'paused_todos': [], 'limitations': [],
        }
        with patch.object(overview, 'get_overview', return_value=snapshot):
            self.assertEqual(json.loads(self.request('GET', '/api/overview')[1]), snapshot)
            for fmt in ('json', 'html', 'md'):
                conn = http.client.HTTPConnection('127.0.0.1', self.server.server_port, timeout=3)
                self.addCleanup(conn.close)
                conn.request('GET', '/api/overview/report.' + fmt)
                response = conn.getresponse()
                body = response.read()
                self.assertEqual(response.status, 200)
                self.assertEqual(response.getheader('Content-Disposition'), f'attachment; filename="testlab-overview.{fmt}"')
                self.assertIn(b'current', body)
                conn.close()
        self.assertEqual(self.request('POST', '/api/overview')[0], 405)
        self.assertEqual(self.request('GET', '/api/overview', {'Origin': 'https://evil.example'})[0], 403)
        self.assertEqual(self.request('GET', '/api/overview/report.pdf')[0], 404)

    def test_reports_download_same_persisted_run(self):
        run_id = self.run["id"]
        for fmt in ("json", "html", "md"):
            conn = http.client.HTTPConnection("127.0.0.1", self.server.server_port, timeout=3)
            conn.request("GET", f"/api/runs/{run_id}/report.{fmt}")
            response = conn.getresponse()
            body = response.read()
            conn.close()
            self.assertEqual(response.status, 200)
            self.assertEqual(response.getheader("Content-Disposition"), f'attachment; filename="testlab-{run_id}.{fmt}"')
            self.assertIn(run_id.encode(), body)
            self.assertIn(b"abc", body)
        self.assertEqual(self.request("GET", "/api/runs/00000000-0000-0000-0000-000000000000/report.json")[0], 404)
        self.assertEqual(self.request("GET", f"/api/runs/{run_id}/report.pdf")[0], 404)


if __name__ == "__main__":
    unittest.main()
