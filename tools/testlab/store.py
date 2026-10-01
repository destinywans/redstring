"""Durable, append-preserving result ledger for the isolated backend test lab."""
from __future__ import annotations

import contextlib
import datetime as dt
import html
import json
import os
from pathlib import Path
import re
import sqlite3
import uuid

STATUSES = ("queued", "running", "passed", "failed", "skipped", "blocked", "not_implemented", "external_todo", "interrupted")
MAX_LOG_BYTES = 1024 * 1024
UUID_RE = re.compile(r"^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$")


def now():
    return dt.datetime.now(dt.timezone.utc).isoformat()


def scrub(value, secrets=()):
    """Redact before persistence, including credentials in URLs and structured logs."""
    value = str(value)
    for secret in sorted((str(s) for s in secrets if s and len(str(s)) >= 4), key=len, reverse=True):
        value = value.replace(secret, "[REDACTED]")
    value = re.sub(r"(?i)(\b[a-z][a-z0-9+.-]{0,31}://[^\s/@:]+:)[^\s/@]+(@)", r"\1[REDACTED]\2", value)
    value = re.sub(r"(?i)(\b(?:bearer|basic)\s+)[A-Za-z0-9+/=_\-.]+", r"\1[REDACTED]", value)
    value = re.sub(r"(?im)(^\s*(?:cookie|set-cookie)\s*:\s*)[^\r\n]+", r"\1[REDACTED]", value)
    value = re.sub(r'''(?i)(["']?(?:password|passwd|secret|token|api[_-]?key|authorization|cookie|set-cookie)["']?\s*[:=]\s*)("[^"]*"|'[^']*'|[^\s,;}]+)''', r"\1[REDACTED]", value)
    value = re.sub(r"(?i)([?&](?:access_token|token|api_key|password)=)[^&#\s]+", r"\1[REDACTED]", value)
    return value


def bounded(value, limit=MAX_LOG_BYTES):
    raw = str(value).encode("utf-8", "replace")
    if len(raw) <= limit:
        return raw.decode("utf-8")
    return raw[:limit].decode("utf-8", "ignore") + "\n[日志达到大小上限，后续内容未保存]"


def clean_data(value):
    if isinstance(value, str):
        return bounded(scrub(value))
    if isinstance(value, list):
        return [clean_data(item) for item in value]
    if isinstance(value, dict):
        return {key: "[REDACTED]" if re.fullmatch(r"(?i)(password|passwd|secret|token|api[_-]?key|authorization|cookie|set-cookie)", str(key)) and isinstance(item, str) else clean_data(item) for key, item in value.items()}
    return value


class Store:
    def __init__(self, root):
        self.root = Path(root).resolve()
        self.data = self.root / ".local" / "testlab"
        self.data.mkdir(parents=True, exist_ok=True, mode=0o700)
        self.evidence_dir = self.data / "evidence"
        self.evidence_dir.mkdir(exist_ok=True, mode=0o700)
        self.database = self.data / "state.sqlite3"
        with self.connect() as conn:
            conn.executescript("""
            PRAGMA journal_mode=WAL;
            CREATE TABLE IF NOT EXISTS runs(id TEXT PRIMARY KEY, data TEXT NOT NULL);
            CREATE TABLE IF NOT EXISTS suites(id TEXT PRIMARY KEY, run_id TEXT NOT NULL, data TEXT NOT NULL);
            CREATE TABLE IF NOT EXISTS cases(id TEXT PRIMARY KEY, run_id TEXT NOT NULL, suite_id TEXT NOT NULL, data TEXT NOT NULL);
            CREATE TABLE IF NOT EXISTS attempts(id TEXT PRIMARY KEY, case_id TEXT NOT NULL, data TEXT NOT NULL);
            CREATE TABLE IF NOT EXISTS events(id INTEGER PRIMARY KEY AUTOINCREMENT, run_id TEXT NOT NULL, type TEXT NOT NULL, created_at TEXT NOT NULL, data TEXT NOT NULL);
            CREATE INDEX IF NOT EXISTS events_run ON events(run_id,id);
            CREATE TABLE IF NOT EXISTS evidence(id TEXT PRIMARY KEY, run_id TEXT NOT NULL, filename TEXT NOT NULL, mime TEXT NOT NULL, size INTEGER NOT NULL);
            CREATE TABLE IF NOT EXISTS records(id TEXT PRIMARY KEY, run_id TEXT NOT NULL, kind TEXT NOT NULL, data TEXT NOT NULL);
            """)
        os.chmod(self.database, 0o600)

    @contextlib.contextmanager
    def connect(self):
        conn = sqlite3.connect(self.database, timeout=30)
        conn.row_factory = sqlite3.Row
        try:
            with conn:
                yield conn
        finally:
            conn.close()

    def emit(self, run_id, kind, data):
        with self.connect() as conn:
            cur = conn.execute("INSERT INTO events(run_id,type,created_at,data) VALUES(?,?,?,?)", (run_id, kind, now(), json.dumps(clean_data(data), ensure_ascii=False)))
            return cur.lastrowid

    def create_run(self, sha="unknown", branch="unknown", **fields):
        ident = str(uuid.uuid4())
        data = dict(id=ident, sha=sha, branch=branch, status="queued", started_at=now(), ended_at=None, stage="准备", heartbeat=now(), pid=os.getpid(), **clean_data(fields))
        with self.connect() as conn:
            conn.execute("INSERT INTO runs VALUES(?,?)", (ident, json.dumps(data, ensure_ascii=False)))
        self.emit(ident, "run", data)
        return data

    def update(self, table, ident, **fields):
        if table not in ("runs", "suites", "cases", "attempts"):
            raise ValueError("invalid ledger table")
        with self.connect() as conn:
            # Acquire the writer lock before reading the JSON snapshot. The
            # connection's 30s busy timeout bounds contention with heartbeat.
            conn.execute("BEGIN IMMEDIATE")
            row = conn.execute(f"SELECT data FROM {table} WHERE id=?", (ident,)).fetchone()
            if not row:
                raise KeyError(ident)
            data = json.loads(row[0])
            if table == "attempts" and data.get("status") not in ("queued", "running"):
                raise ValueError("completed attempt is immutable")
            data.update(clean_data(fields))
            conn.execute(f"UPDATE {table} SET data=? WHERE id=?", (json.dumps(data, ensure_ascii=False), ident))
        if table != "attempts":
            self.emit(ident if table == "runs" else data["run_id"], table.rstrip("s"), data)
        return data

    def add_suite(self, run_id, spec):
        ident = str(uuid.uuid4())
        data = dict(clean_data(spec), id=ident, suite_key=spec["id"], run_id=run_id, status="queued", started_at=None, ended_at=None)
        with self.connect() as conn:
            conn.execute("INSERT INTO suites VALUES(?,?,?)", (ident, run_id, json.dumps(data, ensure_ascii=False)))
        return data

    def add_case(self, run_id, suite_id, **fields):
        ident = str(uuid.uuid4())
        fields = clean_data(fields)
        data = dict(id=ident, run_id=run_id, suite_id=suite_id, name=fields.pop("name", ""), module=fields.pop("module", ""), type=fields.pop("type", "functional"), status="queued", evidence_mode=fields.pop("evidence_mode", "source"), expected=fields.pop("expected", "断言通过"), actual="", duration=0, gap_ids=fields.pop("gap_ids", []), **fields)
        with self.connect() as conn:
            conn.execute("INSERT INTO cases VALUES(?,?,?,?)", (ident, run_id, suite_id, json.dumps(data, ensure_ascii=False)))
        self.emit(run_id, "case", data)
        return data

    def start_attempt(self, case):
        ident = str(uuid.uuid4())
        data = dict(id=ident, case_id=case["id"], run_id=case["run_id"], status="running", started_at=now(), ended_at=None, evidence_ids=[])
        with self.connect() as conn:
            conn.execute("INSERT INTO attempts VALUES(?,?,?)", (ident, case["id"], json.dumps(data)))
        self.update("cases", case["id"], status="running")
        return data

    def finish_attempt(self, case, attempt, status, actual="", duration=0, evidence_ids=(), secrets=()):
        if status not in STATUSES or status in ("queued", "running"):
            raise ValueError("invalid terminal status")
        actual = bounded(scrub(actual, secrets), 8192)
        data = self.update("attempts", attempt["id"], status=status, ended_at=now(), actual=actual, duration=duration, evidence_ids=list(evidence_ids))
        self.update("cases", case["id"], status=status, actual=actual, duration=duration)
        return data

    def add_record(self, run_id, kind, data):
        if kind not in ("interfaces", "defects", "gaps", "metrics", "environment"):
            raise ValueError("invalid record kind")
        ident = str(uuid.uuid4())
        data = clean_data(dict(data))
        data.setdefault("id", ident)
        with self.connect() as conn:
            conn.execute("INSERT INTO records VALUES(?,?,?,?)", (ident, run_id, kind, json.dumps(data, ensure_ascii=False)))
        self.emit(run_id, kind, data)
        return data

    def write_evidence(self, run_id, content, mime="text/plain; charset=utf-8", secrets=()):
        ident = str(uuid.uuid4())
        content = bounded(scrub(content, secrets))
        path = self.evidence_dir / (ident + ".txt")
        with path.open("x", encoding="utf-8") as out:
            out.write(content)
        os.chmod(path, 0o600)
        with self.connect() as conn:
            conn.execute("INSERT INTO evidence VALUES(?,?,?,?,?)", (ident, run_id, path.name, mime, path.stat().st_size))
        return ident

    def read_evidence(self, ident):
        if not UUID_RE.fullmatch(ident):
            raise KeyError(ident)
        with self.connect() as conn:
            row = conn.execute("SELECT * FROM evidence WHERE id=?", (ident,)).fetchone()
        if not row or row["filename"] != ident + ".txt":
            raise KeyError(ident)
        path = self.evidence_dir / row["filename"]
        if path.is_symlink() or path.resolve().parent != self.evidence_dir.resolve():
            raise KeyError(ident)
        fd = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
        with os.fdopen(fd, "rb") as src:
            return src.read(MAX_LOG_BYTES + 256), row["mime"]

    def runs(self):
        with self.connect() as conn:
            return [json.loads(row[0]) for row in conn.execute("SELECT data FROM runs ORDER BY rowid DESC")]

    def events(self, run_id, after=0, limit=200):
        with self.connect() as conn:
            if run_id is None:
                rows = conn.execute("SELECT * FROM events WHERE id>? ORDER BY id LIMIT ?", (max(0, after), min(1000, limit))).fetchall()
            else:
                rows = conn.execute("SELECT * FROM events WHERE run_id=? AND id>? ORDER BY id LIMIT ?", (run_id, max(0, after), min(1000, limit))).fetchall()
        return [dict(id=r["id"], run_id=r["run_id"], type=r["type"], created_at=r["created_at"], data=json.loads(r["data"])) for r in rows]

    def recent_events(self, run_id, limit=200):
        with self.connect() as conn:
            rows = conn.execute("SELECT * FROM events WHERE run_id=? ORDER BY id DESC LIMIT ?", (run_id, min(1000, max(0, limit)))).fetchall()
        return [dict(id=r["id"], run_id=r["run_id"], type=r["type"], created_at=r["created_at"], data=json.loads(r["data"])) for r in reversed(rows)]

    def detail(self, run_id):
        with self.connect() as conn:
            row = conn.execute("SELECT data FROM runs WHERE id=?", (run_id,)).fetchone()
            if not row:
                raise KeyError(run_id)
            result = dict(run=json.loads(row[0]))
            for table in ("suites", "cases"):
                result[table] = [json.loads(r[0]) for r in conn.execute(f"SELECT data FROM {table} WHERE run_id=? ORDER BY rowid", (run_id,))]
            attempts = {c["id"]: [] for c in result["cases"]}
            for row in conn.execute("SELECT a.data,a.case_id FROM attempts a JOIN cases c ON c.id=a.case_id WHERE c.run_id=? ORDER BY a.rowid", (run_id,)):
                attempts[row[1]].append(json.loads(row[0]))
            for case in result["cases"]:
                case["attempts"] = attempts[case["id"]]
            for kind in ("interfaces", "defects", "gaps", "metrics", "environment"):
                result[kind] = [json.loads(r[0]) for r in conn.execute("SELECT data FROM records WHERE run_id=? AND kind=? ORDER BY rowid", (run_id, kind))]
        result["summary"] = {key: sum(c["status"] == key for c in result["cases"]) for key in STATUSES}
        result["summary"]["total"] = len(result["cases"])
        result["summary"]["execution_completed"] = sum(c["status"] not in ("queued", "running") for c in result["cases"])
        result["events"] = self.recent_events(run_id, limit=200)
        return result

    def interrupt(self, run_id, reason="测试进程中断"):
        detail = self.detail(run_id)
        for case in detail["cases"]:
            if case["status"] in ("queued", "running"):
                for attempt in case["attempts"]:
                    if attempt["status"] == "running":
                        self.finish_attempt(case, attempt, "interrupted", reason)
                self.update("cases", case["id"], status="interrupted", actual=reason)
        for suite in detail["suites"]:
            if suite["status"] in ("queued", "running"):
                self.update("suites", suite["id"], status="interrupted", ended_at=now())
        self.update("runs", run_id, status="interrupted", ended_at=now(), stage=reason, heartbeat=now())

    def reconcile_dead_runs(self):
        for run in self.runs():
            if run["status"] != "running":
                continue
            try:
                os.kill(int(run.get("pid", 0)), 0)
            except (ProcessLookupError, ValueError):
                self.interrupt(run["id"], "运行进程已退出，未产生最终结果")
            except PermissionError:
                pass


def render_report(detail, format):
    if format == "json":
        return json.dumps(detail, ensure_ascii=False, indent=2)
    run = detail["run"]
    lines = ["# AIOps 后端测试报告", "", f"运行：{run['id']}", f"源码：{run['sha']} · {run['branch']}",
             f"源码指纹：{run.get('source_tree_sha', '未记录')}", f"结束源码指纹：{run.get('source_tree_sha_end', '未记录')}",
             f"测试清单指纹：{run.get('manifest_sha', '未记录')}", f"起止：{run.get('started_at')} → {run.get('ended_at') or '仍运行'}",
             f"状态：{run['status']}", "", "执行结束、单次通过与完整验收分别判断；外部待办、缺实现、跳过、阻塞均不计为通过。", "", "## 用例", ""]
    for case in detail["cases"]:
        lines += [f"- [{case['status']}] {case['module']} / {case['name']} ({case['evidence_mode']})", f"  预期：{case['expected']}", f"  实际：{case['actual']}"]
        for attempt in case["attempts"]:
            lines.append(f"  尝试 {attempt['id']}: {attempt['status']}，证据：{', '.join(attempt['evidence_ids'])}")
    for kind, title in (("defects", "缺陷"), ("gaps", "缺口"), ("metrics", "性能"), ("interfaces", "接口矩阵"), ("environment", "环境")):
        lines += ["", f"## {title}", ""]
        lines += ["- " + json.dumps(item, ensure_ascii=False) for item in detail[kind]]
    text = "\n".join(lines)
    if format == "md":
        return text
    if format == "html":
        return '<!doctype html><html lang="zh-CN"><meta charset="utf-8"><title>AIOps 测试报告</title><style>body{font:15px/1.6 system-ui;max-width:1100px;margin:40px auto;padding:20px}pre{white-space:pre-wrap;overflow-wrap:anywhere}</style><body><pre>' + html.escape(text) + "</pre></body></html>"
    raise ValueError("unsupported report format")
