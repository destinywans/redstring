"""Serial, authenticated faults on exact supervisor-owned V3 child PIDs only.

All business state is created through public APIs. The provider is a labeled
local protocol Mock; these cases do not prove every worker's crash recovery.
No arbitrary process signaling, SQL writes, or business .env discovery occurs.
"""
from __future__ import annotations

import argparse
import contextlib
import fcntl
import hashlib
import http.client
import json
import os
from pathlib import Path
import re
import socket
import sqlite3
import stat
import subprocess
import sys
import time
import urllib.parse
import uuid

ROOT = Path(__file__).resolve().parents[2]
BASES = ("http://127.0.0.1:18561", "http://127.0.0.1:18562")
CONTROL = "http://127.0.0.1:18560"
LIMIT = 4 << 20


class Blocked(RuntimeError):
    pass


def private_json(path):
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or stat.S_IMODE(info.st_mode) != 0o600 or info.st_size > LIMIT:
        raise Blocked("generated private regular fixture required")
    return json.loads(path.read_text())


def validate_fixture(root, environment, data):
    root = Path(root).resolve()
    owner = environment.get("owner", "")
    work = root / ".local/testlab/environments" / owner
    if not re.fullmatch(r"testlab-[a-z0-9]+", owner) or data.get("owner") != owner:
        raise Blocked("fault fixture owner mismatch")
    if Path(environment.get("work", "")).resolve() != work or environment.get("container") != "aiops-" + owner + "-pg":
        raise Blocked("fault environment ownership path mismatch")
    if Path(data.get("work", "")).resolve() != work / "backend" or Path(data.get("binary", "")).resolve() != work / "backend/aiops":
        raise Blocked("fault binary ownership path mismatch")
    if data.get("postgres_container") != environment["container"] or data.get("database_url") != environment.get("env", {}).get("TESTLAB_APP_DSN"):
        raise Blocked("fault database ownership mismatch")
    parsed = urllib.parse.urlsplit(data.get("database_url", ""))
    if parsed.hostname != "127.0.0.1" or parsed.port != 15461 or parsed.path != "/aiops_testlab_app" or parsed.scheme not in ("postgres", "postgresql"):
        raise Blocked("dedicated loopback application database required")
    if tuple(data.get("base_urls", [])) != BASES or data.get("environment") != "test" or data.get("other_environment") != "staging":
        raise Blocked("explicit testlab origins/environments required")
    if not data.get("control_secret") or type(data.get("supervisor_pid")) is not int or data["supervisor_pid"] <= 1:
        raise Blocked("registered private supervisor required")
    return data


def validate_identity(data, current, index=0):
    if not current or current.get("owner") != data["owner"] or current.get("pid") != data["supervisor_pid"]:
        raise Blocked("supervisor identity changed; refusing fault")
    pids = current.get("api_pids", [])
    if not current.get("ready") or current.get("source_tree_sha") != data["source_tree_sha"] or len(pids) != 2 or len(set(pids)) != 2 or any(type(p) is not int or p <= 1 for p in pids):
        raise Blocked("two ready registered child PIDs required")
    if index not in (0, 1):
        raise Blocked("unknown owned API instance")
    return pids[index]


def request(base, method, path, body=None, cookie=None, bearer=None, timeout=15):
    if base not in (*BASES, CONTROL) or not path.startswith("/") or path.startswith("//"):
        raise Blocked("request is outside fixed local origins")
    parsed = urllib.parse.urlsplit(base)
    conn = http.client.HTTPConnection(parsed.hostname, parsed.port, timeout=timeout)
    headers = {"Origin": base, "X-AIOps-Environment": "test"}
    if cookie:
        headers["Cookie"] = "aiops_session=" + cookie
    if bearer:
        headers["Authorization"] = "Bearer " + bearer
    payload = json.dumps(body).encode() if body is not None else None
    if payload is not None:
        headers["Content-Type"] = "application/json"
    try:
        conn.request(method, path, payload, headers)
        response = conn.getresponse()
        raw = response.read(LIMIT + 1)
        if len(raw) > LIMIT:
            raise AssertionError("bounded HTTP response exceeded limit")
        result = json.loads(raw) if raw else {}
        return response.status, result, response.getheaders()
    finally:
        conn.close()


def expect(result, status):
    if result[0] != status:
        raise AssertionError(f"HTTP status={result[0]}, expected={status}")
    return result[1]


def exclusive_runs(root, own_pid=None):
    database = Path(root) / ".local/testlab/state.sqlite3"
    if not database.exists():
        return
    conn = sqlite3.connect(database.as_uri() + "?mode=ro", uri=True)
    try:
        for row in conn.execute("SELECT data FROM runs"):
            run = json.loads(row[0])
            if run.get("status") == "running" and run.get("pid") != own_pid:
                raise Blocked("another testlab run is active; faults require a serial window")
    finally:
        conn.close()


@contextlib.contextmanager
def serial_window(root, enabled):
    if not enabled or os.environ.get("AIOPS_TESTLAB_FAULTS_ENABLED") != "dedicated-temp-container":
        raise Blocked("explicit serial window and dedicated fault opt-in required")
    path = Path(root) / ".local/testlab/faults.lock"
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    fd = os.open(path, os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    try:
        if not stat.S_ISREG(os.fstat(fd).st_mode) or stat.S_IMODE(os.fstat(fd).st_mode) != 0o600:
            raise Blocked("private regular fault lock required")
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise Blocked("another fault process holds the serial window")
        exclusive_runs(root, os.getppid())
        yield
    finally:
        os.close(fd)


def attest(root):
    from env import _inspect, _owner
    from runner import source_fingerprint
    environment = private_json(Path(root) / ".local/testlab/environment.json")
    data = validate_fixture(root, environment, private_json(Path(root) / ".local/testlab/backend-fixture.json"))
    info = _inspect(environment["container"])
    if not info:
        raise Blocked("owned database container is absent")
    _owner(info, data["owner"])
    if data.get("source_tree_sha") != source_fingerprint(root):
        raise Blocked("formal binary does not match current backend sources")
    current = expect(request(CONTROL, "GET", "/identity", timeout=3), 200)
    validate_identity(data, current)
    return data, current


def fire(root, data, selected, index=0):
    if selected not in ("SIGTERM", "SIGKILL"):
        raise Blocked("unsupported fault signal")
    exclusive_runs(root, os.getppid())
    latest, current = attest(root)
    if latest["owner"] != data["owner"] or latest["supervisor_pid"] != data["supervisor_pid"]:
        raise Blocked("registered supervisor changed before fault")
    pid = validate_identity(data, current, index)
    payload = dict(owner=data["owner"], supervisor_pid=data["supervisor_pid"], api_pid=pid, signal=selected)
    receipt = expect(request(CONTROL, "POST", "/fault", payload, bearer=data["control_secret"], timeout=3), 202)
    if receipt != {"pid": pid, "signal": selected}:
        raise AssertionError("fault controller receipt does not match registered target")
    return pid


def wait_down(timeout=35):
    start = time.monotonic()
    while time.monotonic() - start < timeout:
        occupied = False
        for port in (18560, 18561, 18562):
            with socket.socket() as sock:
                sock.settimeout(.1)
                occupied |= sock.connect_ex(("127.0.0.1", port)) == 0
        if not occupied:
            return time.monotonic() - start
        time.sleep(.1)
    raise AssertionError("owned supervisor group did not stop within bounded window")


def exit_proof(data, pid, selected, after):
    path = Path(data["work"]) / "process-exits.jsonl"
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or stat.S_IMODE(info.st_mode) != 0o600 or info.st_size > LIMIT:
        raise AssertionError("private bounded process exit evidence missing")
    rows = [json.loads(line) for line in path.read_text().splitlines()]
    matched = [r for r in rows if r.get("pid") == pid and r.get("at", 0) >= after]
    if len(matched) != 1 or matched[0].get("forced_kill") is not False:
        raise AssertionError("registered process lacks one unforced exit record")
    if matched[0].get("returncode") != (0 if selected == "SIGTERM" else -9):
        raise AssertionError("registered process exit status does not match fault")


def emit(name, status, actual, duration=0, **fields):
    print(json.dumps(dict(name=name, status=status, actual=actual, duration=duration,
                         module="formal-v3-faults", type="recovery", evidence_mode="real_local",
                         **fields), ensure_ascii=False), flush=True)


def completed_messages(doc):
    messages = doc.get("messages", [])
    assistants = [m for m in messages if m.get("role") == "assistant"]
    if any(m.get("state") == "failed" for m in assistants):
        raise AssertionError("protocol Mock reply failed")
    return bool(assistants) and all(m.get("state") == "completed" and m.get("parts") for m in assistants)


def message_digest(doc):
    return hashlib.sha256(json.dumps(doc["messages"], sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def verify_committed(data, cookie, token, token_id, conv_id, digest):
    for base in BASES:
        expect(request(base, "GET", "/api/v1/auth/me", cookie=cookie), 200)
        tokens = expect(request(base, "GET", "/api/v1/api-tokens", cookie=cookie), 200)
        encoded = json.dumps(tokens)
        if token_id not in encoded or token in encoded or "secret_shown_once" in encoded:
            raise AssertionError("token ledger missing committed token or exposing secret")
        expect(request(base, "GET", "/api/v1/connectors", bearer=token), 200)
        doc = expect(request(base, "GET", "/api/v1/conversations/" + conv_id, cookie=cookie), 200)
        if not completed_messages(doc) or message_digest(doc) != digest:
            raise AssertionError("committed chat changed across instance/restart")


def slow_stream(data, cookie, index=0):
    base = BASES[index]
    capture_path = Path(data["work"]).parent / "captures.jsonl"
    capture_offset = capture_path.stat().st_size if capture_path.exists() else 0
    conv = expect(request(base, "POST", "/api/v1/conversations", {"text": "Synthetic fault stream", "context": "", "model": data["model_id"]}, cookie=cookie), 201)["id"]
    turn = expect(request(base, "POST", "/api/v1/conversations/" + conv + "/messages", {"text": "TESTLAB_FAULT_STREAM_HOLD", "model": data["model_id"]}, cookie=cookie), 202)
    conn = http.client.HTTPConnection("127.0.0.1", 18561 + index, timeout=5)
    try:
        conn.connect()
        conn.sock.setsockopt(socket.SOL_SOCKET, socket.SO_RCVBUF, 1024)
        conn.request("GET", "/api/v1/conversations/" + conv + "/events?reply_id=" + urllib.parse.quote(turn["reply_id"], safe=""), headers={"Cookie": "aiops_session=" + cookie})
        response = conn.getresponse()
        if response.status != 200 or response.getheader("Content-Type") != "text/event-stream":
            raise AssertionError("active SSE connection could not be opened")
        # Read one real persisted delta before becoming a non-reading consumer.
        start = time.monotonic()
        cursor = 0
        while time.monotonic() - start < 8:
            line = response.readline(65536)
            if not line:
                raise Blocked("provider completed before active slow-consumer window")
            if line.startswith(b"id:"):
                cursor = int(line[3:])
            if line.startswith(b"data:"):
                event = json.loads(line[5:])
                if event.get("type") == "message.completed":
                    raise Blocked("provider has no controlled long-stream fixture")
                if event.get("type") == "text.delta":
                    if cursor <= 0 or not event.get("message_id") or not event.get("delta"):
                        raise AssertionError("first delta lacks durable cursor/message/text")
                    break
        else:
            raise Blocked("controlled provider produced no first streamed delta")
        doc = expect(request(BASES[1 - index], "GET", "/api/v1/conversations/" + conv, cookie=cookie), 200)
        if not any(m.get("role") == "assistant" and m.get("state") == "streaming" for m in doc.get("messages", [])):
            raise Blocked("SSE is no longer active; cannot claim slow-consumer shutdown")
        active = dict(conversation_id=conv, reply_id=turn["reply_id"], assistant_id=event["message_id"],
                      cursor=cursor, prefix_delta=event["delta"], capture_offset=capture_offset)
        return conn, response, active
    except BaseException:
        conn.close()
        raise


def restart_owned(root, data):
    import backend
    restarted = backend.setup()
    current, _ = attest(root)
    if any(restarted.get(key) != data.get(key) or current.get(key) != data.get(key)
           for key in ("owner", "database_url", "source_tree_sha")):
        raise AssertionError("restart changed owned database or frozen source")
    return restarted


def fault_cycle(root, data, selected, index=0):
    after = time.time()
    pid = fire(root, data, selected, index)
    try:
        elapsed = wait_down()
        exit_proof(data, pid, selected, after)
    except Exception:
        # Preserve the original assertion even if recovery also fails. No
        # signal, unknown PID or different environment is used for cleanup.
        try:
            restart_owned(root, data)
        except Exception:
            emit("故障断言失败后的自有环境恢复", "failed", "owned runtime restoration failed; first assertion retained; inspect private supervisor evidence")
        raise
    return restart_owned(root, data), pid, elapsed


def replay(base, cookie, active, after=0, timeout=10):
    parsed = urllib.parse.urlsplit(base)
    if base not in BASES:
        raise Blocked("replay origin is outside owned API instances")
    conn = http.client.HTTPConnection(parsed.hostname, parsed.port, timeout=timeout)
    headers = {"Cookie": "aiops_session=" + cookie, "Last-Event-ID": str(after)}
    path = "/api/v1/conversations/" + active["conversation_id"] + "/events?reply_id=" + urllib.parse.quote(active["reply_id"], safe="")
    try:
        conn.request("GET", path, headers=headers)
        response = conn.getresponse()
        if response.status != 200 or response.getheader("Content-Type") != "text/event-stream":
            raise AssertionError("recovered SSE replay unavailable")
        return read_replay(response, after, time.monotonic() + timeout)
    finally:
        conn.close()


def read_replay(response, after, deadline):
    events, size, pending = [], 0, None
    cursor = after
    while time.monotonic() < deadline:
        line = response.readline(65537)
        if not line:
            return events
        size += len(line)
        if size > LIMIT or len(line) > 65536:
            raise AssertionError("replay exceeded bounded response")
        if line.startswith(b"id:"):
            pending = int(line[3:])
        if line.startswith(b"data:"):
            if pending is None or pending <= cursor:
                raise AssertionError("replay IDs are missing, duplicated or out of order")
            events.append(dict(seq=pending, event=json.loads(line[5:])))
            cursor, pending = pending, None
    raise AssertionError("terminal replay did not close within bounded window")


def check_recovered_message(doc, active):
    matches = [m for m in doc.get("messages", []) if m.get("id") == active["assistant_id"]]
    if len(matches) != 1 or matches[0].get("role") != "assistant":
        raise AssertionError("interrupted assistant message missing or duplicated")
    message = matches[0]
    if message.get("state") == "completed":
        raise AssertionError("interrupted model reply silently became success")
    if message.get("state") == "failed":
        if message.get("streaming") or not any(p.get("type") == "status" and p.get("text") == "Model reply failed" for p in message.get("parts", [])):
            raise AssertionError("failed reply lacks durable failure indication")
        return message
    if message.get("state") not in ("streaming", "queued"):
        raise AssertionError("unexpected interrupted reply state")
    return None


def check_replay(full, resumed, terminal_reconnect, active, message):
    terminals = [row for row in full if row["event"].get("type") == "message.completed"]
    if len(terminals) != 1 or full[-1] != terminals[0] or terminals[0]["event"].get("message_id") != active["assistant_id"]:
        raise AssertionError("one final terminal for interrupted assistant is required")
    if not any(row["seq"] == active["cursor"] and row["event"].get("type") == "text.delta" and row["event"].get("delta") == active["prefix_delta"] for row in full):
        raise AssertionError("first observed delta/cursor missing after restart")
    if resumed != [row for row in full if row["seq"] > active["cursor"]] or terminal_reconnect:
        raise AssertionError("Last-Event-ID lost/duplicated suffix or replayed completed terminal")
    failure = [row for row in resumed if row["event"].get("type") == "part.added" and row["event"].get("part", {}).get("type") == "status" and row["event"]["part"].get("text") == "Model reply failed"]
    if len(failure) != 1:
        raise AssertionError("terminal replay omitted or duplicated failure status")
    streamed = "".join(row["event"].get("delta", "") for row in full if row["event"].get("type") == "text.delta")
    persisted = "".join(part.get("markdown", "") for part in message.get("parts", []) if part.get("type") == "text")
    if not streamed or streamed != persisted:
        raise AssertionError("interrupted partial text differs from persisted SSE deltas")


def provider_calls(data, active):
    path = Path(data["work"]).parent / "captures.jsonl"
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or stat.S_IMODE(info.st_mode) != 0o600 or info.st_size < active["capture_offset"]:
        raise AssertionError("owned provider capture rotated or lacks private ownership evidence")
    with path.open("rb") as stream:
        stream.seek(active["capture_offset"])
        raw = stream.read(LIMIT + 1)
    if len(raw) > LIMIT:
        raise AssertionError("provider evidence exceeds bounded fault window")
    # An unrelated capture writer may be appending its final line. The held
    # request's started record is already closed before its first delta.
    rows = [json.loads(line) for line in raw.rsplit(b"\n", 1)[0].splitlines()]
    calls = [r for r in rows if r.get("kind") == "model_fault_stream" and r.get("payload", {}).get("phase") == "started"]
    if len(calls) != 1 or calls[0].get("owner") != data["owner"] or calls[0]["payload"].get("evidence_mode") != "protocol_mock":
        raise AssertionError("interrupted reply triggered duplicate/unverified provider execution")


def observe_reply(root, data, active):
    latest, _ = attest(root)
    if latest["owner"] != data["owner"] or latest["database_url"] != data["database_url"]:
        raise Blocked("reply observation environment changed")
    ids = [active["conversation_id"], active["reply_id"], active["assistant_id"]]
    if any(not re.fullmatch(r"[A-Za-z0-9_-]{1,128}", value) for value in ids):
        raise Blocked("generated reply IDs required for fixed read-only observation")
    conv, reply, assistant = ids
    query = f"""BEGIN READ ONLY;
SELECT json_build_object('reply_state',r.state,'message_state',m.state,
 'error',r.error,'finished',r.finished_at IS NOT NULL,
 'lease_cleared',r.lease_expires_at IS NULL,'active_cleared',c.active_reply_id IS NULL,
 'terminals',(SELECT count(*) FROM v3_conversation_events e
 WHERE e.reply_id=r.id AND e.conversation_id=c.id AND e.payload->>'type'='message.completed'),
 'failed_audits',(SELECT count(*) FROM management_audit a
 WHERE a.action='v3.conversation.reply' AND a.target=r.id AND a.outcome='failed'))
FROM v3_conversation_replies r JOIN v3_conversations c ON c.id=r.conversation_id
JOIN v3_conversation_messages m ON m.id=r.assistant_message_id AND m.conversation_id=c.id
WHERE r.id='{reply}' AND c.id='{conv}' AND m.id='{assistant}';
COMMIT;"""
    result = subprocess.run(["docker", "exec", "-i", data["postgres_container"], "psql", "-U", "testlab", "-d", "aiops_testlab_app", "-X", "-q", "-A", "-t", "-v", "ON_ERROR_STOP=1"],
                            input=query, text=True, capture_output=True, timeout=10)
    if result.returncode or len(result.stdout) > 8192:
        raise AssertionError("fixed read-only interrupted reply observation failed")
    rows = result.stdout.strip().splitlines()
    if len(rows) != 1:
        raise AssertionError("exact interrupted reply observation missing/duplicated")
    proof = json.loads(rows[0])
    if proof != dict(reply_state="failed", message_state="failed", error="reply interrupted after lease expiry", finished=True,
                     lease_cleared=True, active_cleared=True, terminals=1, failed_audits=1):
        raise AssertionError("reply/message/active lease/terminal/audit recovery are inconsistent")
    return proof


def verify_active_recovery(data, cookie, active, signal_at):
    # Current source contract: 30s reply lease, 10s recovery sweep, plus 20s
    # margin for process startup and bounded DB work. Never compress time.
    deadline = signal_at + 30 + 10 + 20
    message = None
    while time.monotonic() < deadline:
        documents = [expect(request(base, "GET", "/api/v1/conversations/" + active["conversation_id"], cookie=cookie, timeout=5), 200) for base in BASES]
        messages = [check_recovered_message(doc, active) for doc in documents]
        if all(messages):
            if messages[0] != messages[1]:
                raise AssertionError("instances disagree on durable interrupted terminal")
            message = messages[0]
            break
        time.sleep(.25)
    if message is None:
        raise AssertionError("active reply never reached failed state within lease+sweep+startup budget")
    full = replay(BASES[0], cookie, active)
    resumed = replay(BASES[1], cookie, active, active["cursor"])
    if not full:
        raise AssertionError("recovered reply has no persistent replay")
    terminal_reconnect = replay(BASES[0], cookie, active, full[-1]["seq"])
    check_replay(full, resumed, terminal_reconnect, active, message)
    provider_calls(data, active)
    return dict(reply_id=active["reply_id"], conversation_id=active["conversation_id"],
                assistant_state="failed", first_cursor=active["cursor"], terminal_cursor=full[-1]["seq"],
                partial_text_sha=hashlib.sha256("".join(row["event"].get("delta", "") for row in full if row["event"].get("type") == "text.delta").encode()).hexdigest(),
                provider_started_count=1, model_evidence="protocol_mock", elapsed=time.monotonic() - signal_at)


def execute(root, enabled):
    from env import _private
    with serial_window(root, enabled):
        data, _ = attest(root)  # Never rebuild/stop before isolation and serial checks.
        login = request(BASES[0], "POST", "/api/v1/auth/login", data["admin"])
        expect(login, 200)
        cookies = [v.split(";", 1)[0].split("=", 1)[1] for k, v in login[2] if k.lower() == "set-cookie" and v.startswith("aiops_session=")]
        if len(cookies) != 1:
            raise AssertionError("formal login did not issue exactly one session")
        cookie = cookies[0]
        token = expect(request(BASES[0], "POST", "/api/v1/api-tokens", {"name": "faults-" + uuid.uuid4().hex, "scopes": ["connectors:view"], "ttl": 3600}, cookie=cookie), 201)
        secret, token_id = token["secret_shown_once"], token["id"]
        conv_id = expect(request(BASES[0], "POST", "/api/v1/conversations", {"text": "Synthetic durable fault-test conversation", "context": "", "model": data["model_id"]}, cookie=cookie), 201)["id"]
        expect(request(BASES[0], "POST", "/api/v1/conversations/" + conv_id + "/messages", {"text": "Reply with the local synthetic fixture", "model": data["model_id"]}, cookie=cookie), 202)
        until = time.monotonic() + 25
        while True:
            doc = expect(request(BASES[1], "GET", "/api/v1/conversations/" + conv_id, cookie=cookie), 200)
            if completed_messages(doc):
                break
            if time.monotonic() >= until:
                raise AssertionError("committed protocol Mock conversation did not complete")
            time.sleep(.2)
        digest = message_digest(doc)
        verify_committed(data, cookie, secret, token_id, conv_id, digest)
        proof_path = Path(data["work"]) / ("faults-" + uuid.uuid4().hex + ".json")
        _private(proof_path, dict(owner=data["owner"], source_tree_sha=data["source_tree_sha"], conversation_id=conv_id, token_id=token_id, message_sha=digest, model_evidence="protocol_mock"))
        emit("已提交会话/Token/对话跨实例一致", "passed", "formal HTTP → real PG → second API; model=protocol_mock")
        stream, response, active = slow_stream(data, cookie)
        signal_at = time.monotonic()
        try:
            data, pid, elapsed = fault_cycle(root, data, "SIGTERM")
            emit("活动 SSE 慢消费者期间 SIGTERM 有界停机", "passed", f"registered pid={pid}; exit=0; no supervisor forced kill; owned group offline; model=protocol_mock", elapsed)
        finally:
            response.close()
            stream.close()
        verify_committed(data, cookie, secret, token_id, conv_id, digest)
        recovered_term = verify_active_recovery(data, cookie, active, signal_at)
        recovered_term["database"] = observe_reply(root, data, active)
        emit("SIGTERM 活动回复终态与跨实例断线重放", "passed", "durable failed/status part; exact Last-Event-ID suffix includes terminal; partial text preserved; provider started exactly once", recovered_term["elapsed"])
        emit("SIGTERM 后同环境重启及已提交结果恢复", "passed", "original session/token/completed chat survive on both APIs")
        stream, response, active = slow_stream(data, cookie, index=1)
        signal_at = time.monotonic()
        try:
            data, pid, elapsed = fault_cycle(root, data, "SIGKILL", index=1)
        finally:
            response.close()
            stream.close()
        recovered_kill = verify_active_recovery(data, cookie, active, signal_at)
        recovered_kill["database"] = observe_reply(root, data, active)
        emit("SIGKILL 活动回复租约恢复与终态重放", "passed", "dead owning API; real 30s lease expiry/10s sweep; durable failed, no model re-execution; complete and resumed SSE agree", recovered_kill["elapsed"])
        _private(proof_path, dict(owner=data["owner"], source_tree_sha=data["source_tree_sha"], conversation_id=conv_id, token_id=token_id, message_sha=digest,
                                 model_evidence="protocol_mock", sigterm_recovery=recovered_term, sigkill_recovery=recovered_kill))
        proof = private_json(proof_path)
        if proof["message_sha"] != digest or proof["owner"] != data["owner"] or proof["source_tree_sha"] != data["source_tree_sha"]:
            raise AssertionError("persistent evidence/source/owner changed across restart")
        verify_committed(data, cookie, secret, token_id, conv_id, digest)
        emit("SIGKILL 后重启及持久已提交结果恢复", "passed", f"registered pid={pid}; exit=-9; original session/token/chat re-read on both APIs; immutable proof restored", elapsed)
        expect(request(BASES[1], "DELETE", "/api/v1/api-tokens/" + token_id, cookie=cookie), 204)
        expect(request(BASES[0], "GET", "/api/v1/connectors", bearer=secret), 401)
        emit("故障恢复后的 Token 撤销一致性", "passed", "revoke on second API → 401 on first API; synthetic conversations retained as evidence")
        emit("真实供应商推理故障验收", "external_todo", "Provider is protocol_mock; paid/real model behavior remains external acceptance TODO",
             scope_limits=["This process test does not force an old worker's delta to arrive after a new generation is claimed",
                           "Does not drop the terminal DB commit response after a successful commit",
                           "Does not revoke initiating session/grants during the process outage",
                           "Does not saturate socket buffers or prove a surviving single instance stays available"],
             related_module_tests=["TestLiveLeaseIsNotRecoveredAndExpiredLeaseFencesWorker", "TestRecoverInterruptedRetainsOnlyAuthorizedSessionOutput", "TestSSECursorStaysWithOriginalReplyAfterNewTurn"],
             related_evidence_note="Module test names are cross-reference pointers; no new pass claim, see frozen PG/race run evidence")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--serial-window", action="store_true")
    args = parser.parse_args()
    try:
        execute(ROOT, args.serial_window)
        return 0
    except Blocked as error:
        emit("正式进程故障前置/场景", "blocked", str(error))
        return 2
    except Exception as error:
        # Do not include HTTP bodies, URLs, credentials or arbitrary dependency errors.
        emit("正式进程故障断言", "failed", type(error).__name__ + (": " + str(error) if isinstance(error, AssertionError) else "; inspect registered private evidence"))
        return 1


if __name__ == "__main__":
    sys.exit(main())
