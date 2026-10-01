"""Bounded observations of the original SSE workload; never a new acceptance gate.

Construct DiagnosticStreams like perf.Streams, with an explicit fresh absolute
evidence_path. start(), close(), seed, emit, affinity, cursors and all original
timeouts remain the original workload. snapshot() remains the original result;
diagnostic_status() separately reports evidence completeness.

The consumer/producer execute the original code objects with private IO/emit
wrappers in copied globals. No shared module, HTTP factory or parser is patched.
"""
from __future__ import annotations

import http.client
import json
import os
from pathlib import Path
import stat
import threading
import time
import types

import perf


def _error_fields(error):
    # Never serialize str(error), repr(error), headers, paths or response data.
    name = type(error).__name__
    name = "".join(c for c in name[:64] if c.isascii() and (c.isalnum() or c == "_"))
    errno = getattr(error, "errno", None)
    return {"exception_class": name, "errno": errno if type(errno) is int else None}


class _JsonlEvidence:
    RESERVE = 1024

    def __init__(self, path, max_bytes):
        path = Path(path)
        if not path.is_absolute():
            raise ValueError("evidence_path must be absolute")
        if type(max_bytes) is not int or not 2048 <= max_bytes <= 64 * 1024 * 1024:
            raise ValueError("evidence capacity must be between2048 and64MiB")
        # Create each new directory with0700 without changing process umask or
        # chmod-ing an existing directory owned by another caller.
        missing = []
        current = path.parent
        while not current.exists():
            if current.is_symlink():
                raise ValueError("symlink evidence parent")
            missing.append(current)
            current = current.parent
        if any(p.is_symlink() for p in (path.parent, *path.parent.parents)):
            raise ValueError("symlink evidence parent")
        for directory in reversed(missing):
            directory.mkdir(mode=0o700)
        parent = path.parent.stat()
        if parent.st_uid != os.getuid() or stat.S_IMODE(parent.st_mode) != 0o700:
            raise ValueError("evidence parent must be owned and0700")
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
        self._file = os.fdopen(fd, "wb")
        self._lock = threading.Lock()
        self._limit = max_bytes
        self._bytes = 0
        self._events = 0
        self._dropped = 0
        self._truncated = False
        self._closed = False
        self._write_errors = 0
        self._last_error = None

    def _append(self, encoded):
        self._file.write(encoded)
        self._bytes += len(encoded)
        self._events += 1

    def emit(self, event, *, flush=False):
        with self._lock:
            if self._closed or self._write_errors or self._truncated:
                self._dropped += 1
                return
            try:
                encoded = (json.dumps(event, separators=(",", ":"), allow_nan=False) + "\n").encode()
                if self._bytes + len(encoded) > self._limit - self.RESERVE:
                    self._truncated = True
                    self._dropped += 1
                    self._append((json.dumps({"event": "truncated", "capacity_bytes": self._limit,
                        "monotonic": time.monotonic()}, separators=(",", ":")) + "\n").encode())
                    self._file.flush()
                    return
                self._append(encoded)
                if flush:
                    self._file.flush()
            except Exception as error:
                self._write_errors += 1
                self._last_error = _error_fields(error)

    def status(self):
        with self._lock:
            return dict(capacity_bytes=self._limit, bytes_written=self._bytes,
                events_written=self._events, dropped_events=self._dropped,
                truncated=self._truncated, evidence_closed=self._closed,
                write_errors=self._write_errors, last_write_error=self._last_error)

    def close(self):
        with self._lock:
            if self._closed:
                return
            try:
                final = {"event": "evidence_closed", "monotonic": time.monotonic(),
                    "truncated": self._truncated, "dropped_events": self._dropped,
                    "write_errors": self._write_errors}
                encoded = (json.dumps(final, separators=(",", ":")) + "\n").encode()
                if not self._write_errors and self._bytes + len(encoded) <= self._limit:
                    self._append(encoded)
                self._file.flush()
            except Exception as error:
                self._write_errors += 1
                self._last_error = _error_fields(error)
            finally:
                try:
                    self._file.close()
                except Exception as error:
                    self._write_errors += 1
                    self._last_error = _error_fields(error)
                self._closed = True


def _original_with_globals(function, **replacements):
    namespace = dict(function.__globals__)
    namespace.update(replacements)
    result = types.FunctionType(function.__code__, namespace, function.__name__,
        function.__defaults__, function.__closure__)
    result.__kwdefaults__ = function.__kwdefaults__
    return result


class _ObservedResponse:
    def __init__(self, connection, raw):
        self._connection = connection
        self._raw = raw

    def __getattr__(self, name):
        return getattr(self._raw, name)

    def getheader(self, name, *args):
        value = self._raw.getheader(name, *args)
        if name.lower() == "content-type" and value != "text/event-stream":
            self._connection.reason = "protocol_rejection"
        return value

    def _read(self, phase, *args):
        value = self._connection.call(phase, getattr(self._raw, phase), *args)
        if not value:
            self._connection.reason = "peer_EOF"
        return value

    def read(self, *args):
        return self._read("read", *args)

    def readline(self, *args):
        return self._read("readline", *args)


class _ObservedConnection:
    def __init__(self, owner, index, raw):
        self.owner, self.index, self.raw = owner, index, raw
        self.epoch = owner._new_epoch(index)
        self.reason = None
        self.response_at = None
        self._closed = False
        self._close_lock = threading.Lock()

    def __getattr__(self, name):
        return getattr(self.raw, name)

    def call(self, phase, function, *args, **kwargs):
        started = time.monotonic()
        self.owner._phase_start(self.index, phase, started)
        try:
            value = function(*args, **kwargs)
        except BaseException as error:
            self.reason = "IO_exception" if isinstance(error, (OSError, http.client.HTTPException)) else "exception"
            self.owner._phase_end(self.index, phase, started, error=error)
            raise
        self.owner._phase_end(self.index, phase, started,
            byte_count=len(value) if isinstance(value, (bytes, bytearray)) else 0)
        return value

    def connect(self):
        return self.call("connect", self.raw.connect)

    def request(self, *args, **kwargs):
        return self.call("request", self.raw.request, *args, **kwargs)

    def getresponse(self):
        response = self.call("getresponse", self.raw.getresponse)
        self.response_at = time.monotonic()
        self.owner._http_status(self.index, response.status)
        if response.status != 200:
            self.reason = "session_renewal" if response.status == 403 and time.monotonic() < self.owner.renew_until else "protocol_rejection"
        return _ObservedResponse(self, response)

    def close(self):
        try:
            return self.raw.close()
        finally:
            with self._close_lock:
                emit_close = not self._closed
                self._closed = True
            if emit_close:
                reason = self.reason
                if reason is None:
                    if self.owner.done.is_set() or perf.cancel_requested():
                        reason = "local_shutdown"
                    elif (self.index < self.owner.normal and self.response_at is not None
                          and time.monotonic() - self.response_at >= self.owner.reconnect_seconds):
                        reason = "scheduled_reconnect"
                    else:
                        reason = "clean_loop_exit_unknown"
                self.owner._connection_closed(self.index, self.epoch, reason)


class _ObservedParser(perf.SSEParser):
    def __init__(self, owner, index, cursor):
        super().__init__(cursor)
        self.owner, self.index = owner, index
        with owner._diagnostic_lock:
            owner._states[index]["cursor"] = cursor

    def feed(self, line):
        try:
            complete = super().feed(line)
        except BaseException as error:
            self.owner._context.connection.reason = "protocol_exception" if isinstance(error, perf.SSEProtocolError) else "parser_exception"
            self.owner._event("parser_exception", self.index, **_error_fields(error))
            raise
        if complete:
            self.owner._frame(self.index, self.cursor)
        return complete


class DiagnosticStreams(perf.Streams):
    def __init__(self, data, run, normal=50, slow=10, reconnect_seconds=60, large=True,
                 *, evidence_path, max_bytes=8 * 1024 * 1024):
        self._evidence = _JsonlEvidence(evidence_path, max_bytes)
        self._diagnostic_lock = threading.Lock()
        self._context = threading.local()
        self._cleanup_finalizer = None
        self._producer_successes = self._producer_failures = 0
        self._producer_active_since = None
        self._last_aggregate = time.monotonic()
        try:
            super().__init__(data, run, normal, slow, reconnect_seconds, large)
        except BaseException:
            self._evidence.close()
            raise
        self._states = {index: dict(index=index, API_ordinal=index % 2,
            kind="slow" if index >= normal else "normal", epoch=0, phase="not_started",
            phase_since=time.monotonic(), cursor=0, frames=0, first_frame_epoch=None, first_byte_epoch=None,
            HTTP_status=None, phases={}, IO_exceptions=0, last_exception=None, close_reason=None)
            for index in self.clients}
        constructor = perf.http.client.HTTPConnection
        self._connection_constructor = constructor
        private_http = types.SimpleNamespace(client=types.SimpleNamespace(
            HTTPConnection=self._make_connection, HTTPException=perf.http.client.HTTPException))
        self._original_consume = _original_with_globals(perf.Streams.consume,
            http=private_http, SSEParser=self._make_parser)
        self._emit = perf.stream_emit
        self._original_produce = _original_with_globals(perf.Streams.produce, stream_emit=self._observed_emit)
        self._evidence.emit(dict(event="diagnostic_started", monotonic=time.monotonic(),
            normal_target=normal, slow_target=slow, reconnect_seconds=reconnect_seconds,
            socket_timeout_seconds=5, progress_deadline_seconds=15, reconnect_grace_seconds=5,
            large=large, original_consume_code_object=True, original_produce_code_object=True), flush=True)

    def _rows_locked(self, now, index=None):
        # Called with the ORIGINAL stats lock: base state and gate ages match.
        with self._diagnostic_lock:
            items = self._states.items() if index is None else ((index, self._states[index]),)
            return [dict(index=index, API_ordinal=state["API_ordinal"], kind=state["kind"],
                epoch=state["epoch"], phase=state["phase"],
                phase_started_monotonic=state["phase_since"],
                phase_blocked_seconds=max(0, now - state["phase_since"]) if state["phase"].endswith("_blocked") else 0,
                cursor=state["cursor"], frames=state["frames"], HTTP_status=state["HTTP_status"],
                active=self.clients[index]["active"],
                last_progress_monotonic=self.clients[index]["last_progress"],
                disconnected_at_monotonic=self.clients[index]["disconnected_at"],
                progress_age_seconds=max(0, now - self.clients[index]["last_progress"]),
                inactive_age_seconds=max(0, now - self.clients[index]["disconnected_at"]) if not self.clients[index]["active"] else 0,
                close_reason=state["close_reason"], IO_exceptions=state["IO_exceptions"],
                last_exception=state["last_exception"], phases={key: dict(value) for key, value in state["phases"].items()})
                for index, state in items]

    def _event(self, event, index, **fields):
        now = time.monotonic()
        with self.lock:
            row = self._rows_locked(now, index)[0]
        self._evidence.emit(dict(event=event, monotonic=now, client=row, **fields))

    def _new_epoch(self, index):
        with self._diagnostic_lock:
            state = self._states[index]
            state["epoch"] += 1
            state["close_reason"] = None
            state["HTTP_status"] = None
            epoch = state["epoch"]
        self._event("connection_attempt", index)
        return epoch

    def _phase_start(self, index, phase, now):
        with self._diagnostic_lock:
            state = self._states[index]
            state["phase"], state["phase_since"] = phase + "_blocked", now
            counters = state["phases"].setdefault(phase,
                dict(calls=0, completed=0, exceptions=0, bytes=0, duration_seconds=0, max_seconds=0))
            counters["calls"] += 1

    def _phase_end(self, index, phase, started, byte_count=0, error=None):
        elapsed = time.monotonic() - started
        first_byte = False
        with self._diagnostic_lock:
            state = self._states[index]
            counters = state["phases"][phase]
            counters["duration_seconds"] += elapsed
            counters["max_seconds"] = max(counters["max_seconds"], elapsed)
            counters["bytes"] += byte_count
            counters["exceptions" if error is not None else "completed"] += 1
            state["phase"], state["phase_since"] = phase + "_returned", time.monotonic()
            if phase == "read" and byte_count and state["first_byte_epoch"] != state["epoch"]:
                state["first_byte_epoch"] = state["epoch"]
                first_byte = True
            if error is not None:
                state["last_exception"] = _error_fields(error)
                state["IO_exceptions"] += isinstance(error, (OSError, http.client.HTTPException))
        if error is not None:
            self._event("IO_exception" if isinstance(error, (OSError, http.client.HTTPException)) else "call_exception",
                index, failed_phase=phase, duration_seconds=elapsed,
                during_shutdown=self.done.is_set() or perf.cancel_requested(), **_error_fields(error))
        if first_byte:
            self._event("first_slow_byte", index)

    def _http_status(self, index, status):
        with self._diagnostic_lock:
            self._states[index]["HTTP_status"] = status if type(status) is int else None

    def _connection_closed(self, index, epoch, reason):
        with self._diagnostic_lock:
            state = self._states[index]
            state["close_reason"] = reason
            state["phase"], state["phase_since"] = "closed", time.monotonic()
        self._event("connection_closed", index, closed_epoch=epoch, close_reason=reason)

    def _make_connection(self, *args, **kwargs):
        connection = _ObservedConnection(self, self._context.index, self._connection_constructor(*args, **kwargs))
        self._context.connection = connection
        return connection

    def _make_parser(self, cursor):
        return _ObservedParser(self, self._context.index, cursor)

    def _frame(self, index, cursor):
        now = time.monotonic()
        with self._diagnostic_lock:
            state = self._states[index]
            state["cursor"] = cursor
            state["frames"] += 1
            first = state["first_frame_epoch"] != state["epoch"]
            state["first_frame_epoch"] = state["epoch"]
            aggregate = None
            if now - self._last_aggregate >= 1:
                aggregate = dict(event="normal_frame_aggregate", monotonic=now,
                    interval_seconds=now - self._last_aggregate,
                    clients=[dict(index=i, API_ordinal=s["API_ordinal"], frames=s["frames"], cursor=s["cursor"])
                        for i, s in self._states.items() if s["kind"] == "normal"])
                self._last_aggregate = now
        if first:
            self._event("first_frame", index)
        if aggregate:
            self._evidence.emit(aggregate)

    def consume(self, index):
        self._context.index = index
        try:
            return self._original_consume(self, index)
        finally:
            self._event("client_stopped", index)

    def _observed_emit(self, *args):
        started = time.monotonic()
        with self._diagnostic_lock:
            self._producer_active_since = started
        failure = None
        try:
            return self._emit(*args)
        except BaseException as error:
            failure = _error_fields(error)
            raise
        finally:
            with self._diagnostic_lock:
                self._producer_active_since = None
                self._producer_failures += failure is not None
                self._producer_successes += failure is None
                counts = dict(successes=self._producer_successes, failures=self._producer_failures)
            self._evidence.emit(dict(event="producer_failure" if failure else "producer_success",
                monotonic=time.monotonic(), duration_seconds=time.monotonic() - started,
                **counts, **(failure or {})))

    def produce(self):
        return self._original_produce(self)

    def sample_health(self, elapsed, duration, startup=15):
        # These five gate statements are the unchanged original sample_health.
        # Capture all client states while its ORIGINAL lock is still held.
        now = time.monotonic()
        with self.lock:
            if elapsed<startup or elapsed>duration-1 or now<self.renew_until:return
            unhealthy=[i for i,s in self.clients.items() if now-s['last_progress']>15 or not s['active'] and now-s['disconnected_at']>5]
            self.stats['stable_samples']+=1;self.stats['window_violations']+=bool(unhealthy)
            self.stats['last_window']={'elapsed_seconds':elapsed,'unhealthy_clients':unhealthy,'progress_deadline_seconds':15,'reconnect_grace_seconds':5,'renewal_grace_seconds':30}
            rows = self._rows_locked(now)
            counters = {key: value for key, value in self.stats.items() if type(value) in (int, bool)}
            window = dict(self.stats["last_window"])
            with self._diagnostic_lock:
                producer = dict(successes=self._producer_successes, failures=self._producer_failures,
                    call_blocked_seconds=max(0, now-self._producer_active_since)
                        if self._producer_active_since is not None else 0)
        self._evidence.emit(dict(event="health_window", monotonic=now, window=window,
            original_counters=counters, clients=rows, producer=producer), flush=True)

    def diagnostic_status(self):
        with self._diagnostic_lock:
            producer = dict(producer_successes=self._producer_successes,
                producer_failures=self._producer_failures,
                producer_call_blocked_seconds=max(0, time.monotonic() - self._producer_active_since)
                    if self._producer_active_since is not None else 0,
                normal_frames_observed=sum(s["frames"] for s in self._states.values()))
        return dict(self._evidence.status(), **producer,
            pending_original_workers=sum(thread.is_alive() for thread in self.threads))

    def _finish_evidence(self):
        # If original close raises with pending workers, do NOT extend its join
        # budget or run SQL again. A daemon finalizer only waits for those exact
        # existing threads and closes the diagnostic file after they stop.
        for thread in self.threads:
            if thread is not threading.current_thread():
                thread.join()
        self._evidence.emit(dict(event="original_workers_stopped", monotonic=time.monotonic(),
            original_counters={key: value for key, value in self.snapshot().items()
                if type(value) in (int, bool)}), flush=True)
        self._evidence.close()

    def close(self):
        try:
            return super().close()
        finally:
            if any(thread.is_alive() for thread in self.threads):
                self._evidence.emit(dict(event="original_worker_cleanup_pending", monotonic=time.monotonic()), flush=True)
                if self._cleanup_finalizer is None:
                    self._cleanup_finalizer = threading.Thread(target=self._finish_evidence,
                        name="SSE-diagnostic-finalizer", daemon=True)
                    self._cleanup_finalizer.start()
            else:
                self._finish_evidence()
