"""Only offline fakes and one ephemeral owned loopback SSE server; no PG/load."""
import ast
import copy
import http.server
import inspect
import json
import os
from pathlib import Path
import stat
import tempfile
import textwrap
import threading
import unittest
from unittest.mock import patch

import perf
import perf_sse_diagnostics as diagnostics


SECRET = "synthetic-cookie-password-body-exception-secret"
FRAME = b'id: 1\ndata: {"type":"text.delta","delta":"' + SECRET.encode() + b'"}\n\n'


class FakeSocket:
    def __init__(self):
        self.buffers = []

    def setsockopt(self, *args):
        self.buffers.append(args)

    def shutdown(self, *args):
        pass


class FakeResponse:
    status = 200

    def __init__(self, lines=None):
        self.lines = iter(lines if lines is not None else FRAME.splitlines(keepends=True) + [b""])

    def getheader(self, name):
        return "text/event-stream"

    def readline(self, limit):
        return next(self.lines)

    def read(self, count):
        return next(self.lines)


class FakeConnection:
    def __init__(self, response=None, failure=None):
        self.sock = FakeSocket()
        self.response = response or FakeResponse()
        self.failure = failure
        self.requests = []
        self.closed = 0

    def connect(self):
        pass

    def request(self, *args, **kwargs):
        self.requests.append((args, kwargs))

    def getresponse(self):
        if self.failure is not None:
            raise self.failure
        return self.response

    def close(self):
        self.closed += 1


class DiagnosticTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        self.data = {"base_urls": ["http://127.0.0.1:18561", "http://127.0.0.1:18562"],
                     "users": [{"cookie": SECRET, "id": "synthetic-" + str(i)} for i in range(20)]}
        self.enterContext(patch.object(perf, "stream_seed", return_value="synthetic-sse-"))
        self.cleanup = self.enterContext(patch.object(perf, "cleanup_stream_finish"))
        self.enterContext(patch.object(perf, "cancel_requested", return_value=False))

    def make(self, **kwargs):
        path = self.root / ("case-" + str(len(list(self.root.iterdir())))) / "events.jsonl"
        stream = diagnostics.DiagnosticStreams(self.data, "test", evidence_path=path, **kwargs)
        self.addCleanup(lambda: stream.close() if not stream.diagnostic_status()["evidence_closed"] else None)
        return stream, path

    def records(self, path):
        return [json.loads(line) for line in path.read_text().splitlines()]

    def consume_once(self, diagnostic, index=0, response=None, failure=None, renewal=False, **parameters):
        connection = FakeConnection(response, failure)
        constructor_calls = []

        def constructor(*args, **kwargs):
            constructor_calls.append((args, kwargs))
            return connection

        with patch.object(perf.http.client, "HTTPConnection", side_effect=constructor):
            if diagnostic:
                stream, path = self.make(**parameters)
            else:
                stream, path = perf.Streams(self.data, "test", **parameters), None
            if renewal:
                stream.begin_renewal()
            with patch.object(stream.done, "wait", side_effect=lambda seconds: stream.done.set()):
                stream.consume(index)
        result = stream.snapshot()
        stream.close()
        return result, connection, constructor_calls, stream, path

    def test_original_code_objects_and_shared_factories_remain_unchanged(self):
        factory, parser = perf.http.client.HTTPConnection, perf.SSEParser
        stream, _ = self.make()
        self.assertIs(stream._original_consume.__code__, perf.Streams.consume.__code__)
        self.assertIs(stream._original_produce.__code__, perf.Streams.produce.__code__)
        self.assertIs(diagnostics.DiagnosticStreams.start, perf.Streams.start)
        self.assertIs(diagnostics.DiagnosticStreams.snapshot, perf.Streams.snapshot)
        self.assertIs(perf.http.client.HTTPConnection, factory)
        self.assertIs(perf.SSEParser, parser)
        self.assertEqual((stream.normal, stream.slow, stream.reconnect_seconds, stream.large), (50, 10, 60, True))

    def test_health_gate_source_prefix_and_threshold_boundaries_match_original(self):
        original = ast.parse(textwrap.dedent(inspect.getsource(perf.Streams.sample_health))).body[0]
        observed = ast.parse(textwrap.dedent(inspect.getsource(diagnostics.DiagnosticStreams.sample_health))).body[0]
        self.assertEqual(ast.dump(original.body[0]), ast.dump(observed.body[0]))
        self.assertEqual([ast.dump(n) for n in original.body[1].body],
                         [ast.dump(n) for n in observed.body[1].body[:len(original.body[1].body)]])
        plain = perf.Streams(self.data, "test")
        stream, path = self.make()
        states = {i: dict(active=True, last_progress=100, disconnected_at=100) for i in range(60)}
        states[0]["last_progress"] = 85  # exactly15s is permitted
        states[1]["last_progress"] = 84.9
        states[2] = dict(active=False, last_progress=99, disconnected_at=95)  # exactly5s permitted
        states[3] = dict(active=False, last_progress=99, disconnected_at=94.9)
        plain.clients, stream.clients = copy.deepcopy(states), copy.deepcopy(states)
        with patch.object(perf.time, "monotonic", return_value=100):
            for elapsed in (14.9, 15, 59, 59.1):
                plain.sample_health(elapsed, 60)
                stream.sample_health(elapsed, 60)
                self.assertEqual(plain.snapshot(), stream.snapshot())
            plain.renew_until = stream.renew_until = 101
            plain.sample_health(30, 60)
            stream.sample_health(30, 60)
            self.assertEqual(plain.snapshot(), stream.snapshot())
        self.assertEqual(stream.snapshot()["last_window"]["unhealthy_clients"], [1, 3])
        self.assertEqual(perf.sse_accepted(stream.snapshot()), perf.sse_accepted(plain.snapshot()))
        windows = [row for row in self.records(path) if row["event"] == "health_window"]
        self.assertEqual(len(windows), 2)
        self.assertTrue(all(len(row["clients"]) == 60 for row in windows))
        self.assertEqual(windows[0]["clients"][1]["progress_age_seconds"], 15.099999999999994)
        plain.close()

    def test_normal_EOF_counters_cursor_affinity_and_wire_inputs_match_original(self):
        plain, raw_plain, calls_plain, _, _ = self.consume_once(False, index=2)
        observed, raw_diag, calls_diag, stream, path = self.consume_once(True, index=2)
        self.assertEqual(plain, observed)
        self.assertEqual(calls_plain, calls_diag)
        self.assertEqual(calls_diag[0], (("127.0.0.1", 18561), {"timeout": 5}))
        self.assertEqual(raw_plain.requests, raw_diag.requests)
        self.assertEqual(raw_plain.sock.buffers, raw_diag.sock.buffers)
        self.assertEqual(observed["normal_frames"], 1)
        self.assertEqual((observed["reconnects"], observed["expected_disconnects"]), (1, 1))
        rows = self.records(path)
        first = next(row for row in rows if row["event"] == "first_frame")
        self.assertEqual((first["client"]["index"], first["client"]["API_ordinal"], first["client"]["cursor"]), (2, 0, 1))
        self.assertEqual(next(row for row in rows if row["event"] == "connection_closed")["close_reason"], "peer_EOF")
        self.assertNotIn(SECRET, path.read_text())
        self.assertTrue(stream.diagnostic_status()["evidence_closed"])

    def test_timeout_is_observed_without_changing_original_expected_disconnect(self):
        error = TimeoutError(110, SECRET)
        plain, _, _, _, _ = self.consume_once(False, failure=error)
        observed, _, _, stream, path = self.consume_once(True, failure=error)
        self.assertEqual(plain, observed)
        self.assertEqual((observed["expected_disconnects"], observed["protocol_errors"]), (1, 0))
        event = next(row for row in self.records(path) if row["event"] == "IO_exception")
        self.assertEqual((event["failed_phase"], event["exception_class"], event["errno"]), ("getresponse", "TimeoutError", 110))
        self.assertEqual(event["client"]["IO_exceptions"], 1)
        self.assertNotIn(SECRET, path.read_text())
        self.assertEqual(stream.diagnostic_status()["normal_frames_observed"], 0)

    def test_scheduled_reconnect_preserves_partial_frame_and_original_counters(self):
        plain, _, _, _, _ = self.consume_once(False, reconnect_seconds=0)
        observed, _, _, _, path = self.consume_once(True, reconnect_seconds=0)
        self.assertEqual(plain, observed)
        self.assertEqual((observed["normal_frames"], observed["reconnects"], observed["expected_disconnects"]), (0, 1, 1))
        closed = next(row for row in self.records(path) if row["event"] == "connection_closed")
        self.assertEqual((closed["close_reason"], closed["client"]["cursor"]), ("scheduled_reconnect", 0))

    def test_renewal403_remains_distinct_from_protocol_or_IO_failure(self):
        plain_response, observed_response = FakeResponse(), FakeResponse()
        plain_response.status = observed_response.status = 403
        plain, _, _, _, _ = self.consume_once(False, response=plain_response, renewal=True)
        observed, _, _, _, path = self.consume_once(True, response=observed_response, renewal=True)
        self.assertEqual(plain, observed)
        self.assertEqual((observed["renewal_disconnects"], observed["protocol_errors"], observed["expected_disconnects"]), (1, 0, 0))
        closed = next(row for row in self.records(path) if row["event"] == "connection_closed")
        self.assertEqual(closed["close_reason"], "session_renewal")

    def test_duplicate_cursor_still_fails_original_protocol_gate(self):
        duplicate = FRAME.splitlines(keepends=True) * 2 + [b""]
        plain, _, _, _, _ = self.consume_once(False, response=FakeResponse(duplicate))
        observed, _, _, _, path = self.consume_once(True, response=FakeResponse(duplicate))
        self.assertEqual(plain, observed)
        self.assertEqual((observed["normal_frames"], observed["protocol_errors"]), (1, 1))
        self.assertEqual(next(row for row in self.records(path) if row["event"] == "connection_closed")["close_reason"], "protocol_exception")
        self.assertNotIn(SECRET, path.read_text())

    def test_many_normal_frames_are_aggregated_once_per_second_not_per_frame(self):
        lines = []
        for cursor in range(1, 1001):
            lines.extend([b'id: ' + str(cursor).encode() + b'\n',
                          b'data: {"type":"text.delta","delta":"' + SECRET.encode() + b'"}\n', b'\n'])
        lines.append(b"")
        response = FakeResponse(lines)
        original_readline = response.readline
        reads = 0
        with patch.object(perf.time, "monotonic", return_value=100) as clock:
            def readline(limit):
                nonlocal reads
                reads += 1
                if reads > 1500:
                    clock.return_value = 101
                return original_readline(limit)
            response.readline = readline
            result, _, _, stream, path = self.consume_once(True, response=response)
        self.assertEqual(result["normal_frames"], 1000)
        self.assertEqual(stream.diagnostic_status()["normal_frames_observed"], 1000)
        events = self.records(path)
        self.assertEqual(sum(row["event"] == "first_frame" for row in events), 1)
        self.assertEqual(sum(row["event"] == "normal_frame_aggregate" for row in events), 1)
        self.assertLess(len(events), 20)
        self.assertNotIn(SECRET, path.read_text())

    def test_slow_reader_still_reads_one_byte_without_advancing_frame_cursor(self):
        plain, raw_plain, _, _, _ = self.consume_once(False, index=50, response=FakeResponse([b"x", b""]))
        observed, raw_diag, _, _, path = self.consume_once(True, index=50, response=FakeResponse([b"x", b""]))
        self.assertEqual(plain, observed)
        self.assertEqual(raw_plain.sock.buffers, raw_diag.sock.buffers)
        self.assertEqual((observed["slow_bytes"], observed["normal_frames"]), (1, 0))
        first = next(row for row in self.records(path) if row["event"] == "first_slow_byte")
        self.assertEqual((first["client"]["kind"], first["client"]["cursor"]), ("slow", 0))

    def test_producer_failures_and_successes_keep_original_counter_and_wait(self):
        calls = []
        def emit(*args):
            calls.append(args)
            if len(calls) == 2:
                raise RuntimeError(SECRET)
        with patch.object(perf, "stream_emit", side_effect=emit):
            stream, path = self.make()
            def wait(seconds):
                self.assertEqual(seconds, 1)
                if len(calls) == 2:
                    stream.done.set()
            with patch.object(stream.done, "wait", side_effect=wait):
                stream.produce()
        stream.close()
        self.assertEqual(stream.snapshot()["producer_errors"], 1)
        self.assertEqual(stream.diagnostic_status()["producer_successes"], 1)
        self.assertEqual(stream.diagnostic_status()["producer_failures"], 1)
        self.assertTrue(all(call == (self.data, "synthetic-sse-", True) for call in calls))
        self.assertNotIn(SECRET, path.read_text())

    def test_capacity_and_mode_are_bounded_with_explicit_truncation(self):
        stream, path = self.make(max_bytes=2048)
        for _ in range(50):
            stream._evidence.emit({"event": "capacity_case", "count": 1, "padding": "x" * 100})
        stream.close()
        self.assertLessEqual(path.stat().st_size, 2048)
        self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)
        self.assertEqual(stat.S_IMODE(path.parent.stat().st_mode), 0o700)
        rows = self.records(path)
        self.assertEqual(sum(row["event"] == "truncated" for row in rows), 1)
        self.assertEqual(rows[-1]["event"], "evidence_closed")
        self.assertTrue(rows[-1]["truncated"])
        self.assertGreater(rows[-1]["dropped_events"], 0)

    def test_existing_file_and_symlink_parent_are_rejected_before_seeding(self):
        existing = self.root / "existing.jsonl"
        existing.write_text("original")
        existing.chmod(0o600)
        with patch.object(perf, "stream_seed") as seed:
            with self.assertRaises(FileExistsError):
                diagnostics.DiagnosticStreams(self.data, "test", evidence_path=existing)
            target = self.root / "real"
            target.mkdir(mode=0o700)
            link = self.root / "link"
            link.symlink_to(target, target_is_directory=True)
            with self.assertRaises(ValueError):
                diagnostics.DiagnosticStreams(self.data, "test", evidence_path=link / "events.jsonl")
            seed.assert_not_called()
        self.assertEqual(existing.read_text(), "original")

    def test_evidence_IO_failure_does_not_turn_original_frame_into_protocol_error(self):
        plain, _, _, _, _ = self.consume_once(False)
        connection = FakeConnection()
        with patch.object(perf.http.client, "HTTPConnection", return_value=connection):
            stream, path = self.make()
        raw_file = stream._evidence._file
        class BrokenWriter:
            def write(self, data):
                raise OSError(28, SECRET)
            def flush(self):
                raw_file.flush()
            def close(self):
                raw_file.close()
        stream._evidence._file = BrokenWriter()
        with patch.object(stream.done, "wait", side_effect=lambda seconds: stream.done.set()):
            stream.consume(0)
        stream.close()
        self.assertEqual(plain, stream.snapshot())
        self.assertEqual(stream.diagnostic_status()["write_errors"], 1)
        self.assertEqual(stream.diagnostic_status()["last_write_error"], {"exception_class": "OSError", "errno": 28})
        self.assertNotIn(SECRET, path.read_text())

    def test_cleanup_failure_stays_primary_and_evidence_waits_for_original_thread(self):
        stream, path = self.make()
        release = threading.Event()
        worker = threading.Thread(target=release.wait)
        worker.start()
        stream.threads.append(worker)
        def failed_close():
            stream.done.set()
            raise RuntimeError(SECRET)
        try:
            with patch.object(perf.Streams, "close", side_effect=failed_close):
                with self.assertRaisesRegex(RuntimeError, SECRET):
                    stream.close()
            self.assertFalse(stream.diagnostic_status()["evidence_closed"])
            self.assertEqual(stream.diagnostic_status()["pending_original_workers"], 1)
        finally:
            release.set()
            worker.join(1)
            stream._cleanup_finalizer.join(1)
        self.assertTrue(stream.diagnostic_status()["evidence_closed"])
        names = [row["event"] for row in self.records(path)]
        self.assertLess(names.index("original_worker_cleanup_pending"), names.index("original_workers_stopped"))
        self.assertLess(names.index("original_workers_stopped"), names.index("evidence_closed"))
        self.assertNotIn(SECRET, path.read_text())

    def test_blocked_response_phase_is_in_all_client_health_snapshot(self):
        entered, release = threading.Event(), threading.Event()
        connection = FakeConnection()
        def response():
            entered.set()
            release.wait(2)
            return FakeResponse()
        connection.getresponse = response
        with patch.object(perf.http.client, "HTTPConnection", return_value=connection):
            stream, path = self.make()
        def worker():
            stream._context.index = 0
            stream._make_connection("127.0.0.1", 18561, timeout=5).getresponse()
        thread = threading.Thread(target=worker)
        stream.threads.append(thread)
        thread.start()
        self.assertTrue(entered.wait(1))
        try:
            stream.sample_health(20, 60)
            sample = next(row for row in self.records(path) if row["event"] == "health_window")
            self.assertEqual(len(sample["clients"]), 60)
            self.assertEqual(sample["clients"][0]["phase"], "getresponse_blocked")
            self.assertGreaterEqual(sample["clients"][0]["phase_blocked_seconds"], 0)
            self.assertEqual(sample["clients"][0]["phases"]["getresponse"]["completed"], 0)
        finally:
            release.set()
            thread.join(2)
        stream.close()
        self.assertEqual(stream.diagnostic_status()["pending_original_workers"], 0)

    def test_owned_loopback_socket_reads_real_protocol_and_closes_workers_first(self):
        class Handler(http.server.BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"
            def log_message(self, *args):
                pass
            def do_GET(self):
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.end_headers()
                self.wfile.write(FRAME)
                self.wfile.flush()
                self.close_connection = True
        server = http.server.HTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever)
        thread.start()
        try:
            self.data["base_urls"] = ["http://127.0.0.1:" + str(server.server_port)] * 2
            stream, path = self.make(normal=1, slow=0)
            with patch.object(stream.done, "wait", side_effect=lambda seconds: stream.done.set()):
                stream.consume(0)
            stream.close()
            self.assertEqual(stream.snapshot()["normal_frames"], 1)
            self.assertTrue(stream.diagnostic_status()["evidence_closed"])
            names = [row["event"] for row in self.records(path)]
            self.assertLess(names.index("original_workers_stopped"), names.index("evidence_closed"))
            self.assertNotIn(SECRET, path.read_text())
        finally:
            server.shutdown()
            thread.join(2)
            server.server_close()


if __name__ == "__main__":
    unittest.main()
