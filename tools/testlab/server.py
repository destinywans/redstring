"""Loopback-only read-only dashboard and resumable persisted event stream."""
from __future__ import annotations

import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import re
import threading
import time
from urllib.parse import parse_qs, unquote, urlsplit

from store import Store, render_report


class LabHTTPServer(ThreadingHTTPServer):
    daemon_threads = True
    # Rebind after a restart while prior SSE sockets are in TIME_WAIT. An
    # actively listening process still fails bind; SO_REUSEPORT is not enabled.
    allow_reuse_address = True

    def __init__(self, *args, **kwargs):
        self.stream_condition = threading.Condition()
        self.active_streams = 0
        super().__init__(*args, **kwargs)

    def server_close(self):
        self.closing = True
        super().server_close()
        # The event loops must release their SQLite readers before the caller
        # tears down a temporary ledger or replaces this dashboard instance.
        with self.stream_condition:
            self.stream_condition.wait_for(lambda: self.active_streams == 0, timeout=6)


def make_server(root, port=8787):
    root = Path(root).resolve()
    store = Store(root)
    static = root / "tools/testlab/static"
    overview_cache = {}
    overview_lock = threading.Lock()

    def overview_snapshot():
        # Reuse an unchanged persisted snapshot. A new event, source change or
        # ledger/plan change invalidates it; downloads always compute afresh.
        from overview import get_overview
        from runner import source_fingerprint, git_value
        with overview_lock:
            with store.connect() as connection:
                event_id = connection.execute("SELECT coalesce(max(id),0) FROM events").fetchone()[0]
            ledgers = [root / "tools/testlab" / name for name in ("acceptance-plan.json", "gaps.json", "defects.json")]
            ledgers += [root / "docs/api/v3-contract.md", root / "internal/apicontract/openapi.json"]
            key = (event_id, source_fingerprint(root), git_value(root, "rev-parse", "HEAD"), git_value(root, "branch", "--show-current"), *(p.stat().st_mtime_ns if p.exists() else None for p in ledgers))
            if key != overview_cache.get("key"):
                overview_cache.update(key=key, data=get_overview(root, store))
            return overview_cache["data"]

    class Handler(BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def log_message(self, format, *args):
            pass

        def send(self, status, body=b"", mime="application/json; charset=utf-8", download_name=None):
            if isinstance(body, str):
                body = body.encode("utf-8")
            self.send_response(status)
            self.send_header("Content-Type", mime)
            self.send_header("Content-Length", str(len(body)))
            self.send_header("Cache-Control", "no-store")
            if download_name:
                self.send_header("Content-Disposition", f'attachment; filename="{download_name}"')
            self.send_header("X-Content-Type-Options", "nosniff")
            self.send_header("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'")
            self.end_headers()
            if self.command != "HEAD":
                self.wfile.write(body)

        def do_GET(self):
            # Reject DNS rebinding and cross-origin browser reads even on loopback.
            accepted_hosts = {f"127.0.0.1:{self.server.server_port}", f"localhost:{self.server.server_port}"}
            origin = self.headers.get("Origin")
            if self.headers.get("Host") not in accepted_hosts or (origin and origin not in {"http://" + h for h in accepted_hosts}):
                return self.send(403, '{"error":"localhost origin required"}')
            parsed = urlsplit(self.path)
            path = unquote(parsed.path)
            try:
                if path == "/api/runs":
                    store.reconcile_dead_runs()
                    return self.send(200, json.dumps({"items": store.runs()}, ensure_ascii=False))
                if path == "/api/overview" or re.fullmatch(r"/api/overview/report\.(json|html|md)", path):
                    from overview import get_overview, render_overview_report
                    store.reconcile_dead_runs()
                    detail = overview_snapshot() if path == "/api/overview" else get_overview(root, store)
                    if path == "/api/overview":
                        return self.send(200, json.dumps(detail, ensure_ascii=False))
                    fmt = path.rsplit(".", 1)[-1]
                    mime = {"json": "application/json; charset=utf-8", "html": "text/html; charset=utf-8", "md": "text/markdown; charset=utf-8"}[fmt]
                    return self.send(200, render_overview_report(detail, fmt), mime, f"testlab-overview.{fmt}")
                match = re.fullmatch(r"/api/runs/([0-9a-f-]{36})(?:/report\.(json|html|md))?", path)
                if match:
                    detail = store.detail(match.group(1))
                    fmt = match.group(2)
                    if fmt:
                        mime = {"json": "application/json; charset=utf-8", "html": "text/html; charset=utf-8", "md": "text/markdown; charset=utf-8"}[fmt]
                        return self.send(200, render_report(detail, fmt), mime, f"testlab-{match.group(1)}.{fmt}")
                    return self.send(200, json.dumps(detail, ensure_ascii=False))
                if path in ("/api/events", "/api/overview/events"):
                    params = parse_qs(parsed.query)
                    run_id = params.get("run_id", [""])[0] if path == "/api/events" else None
                    if run_id is not None:
                        store.detail(run_id)
                    after = int(self.headers.get("Last-Event-ID", params.get("after", ["0"])[0]))
                    if after < 0:
                        raise ValueError()
                    return self.stream(run_id, after)
                if path.startswith("/api/evidence/"):
                    data, mime = store.read_evidence(path.removeprefix("/api/evidence/"))
                    return self.send(200, data, mime)
                files = {"/": ("index.html", "text/html; charset=utf-8"), "/static/app.js": ("app.js", "text/javascript; charset=utf-8"), "/static/styles.css": ("styles.css", "text/css; charset=utf-8")}
                if path in files:
                    filename, mime = files[path]
                    asset = static / filename
                    if not asset.is_file() or asset.is_symlink():
                        raise KeyError(path)
                    return self.send(200, asset.read_bytes(), mime)
                return self.send(404, '{"error":"not found"}')
            except (KeyError, FileNotFoundError):
                return self.send(404, '{"error":"not found"}')
            except ValueError:
                return self.send(400, '{"error":"invalid request"}')
            except (BrokenPipeError, ConnectionResetError, TimeoutError):
                return

        def stream(self, run_id, after):
            with self.server.stream_condition:
                self.server.active_streams += 1
            try:
                self.stream_events(run_id, after)
            finally:
                with self.server.stream_condition:
                    self.server.active_streams -= 1
                    self.server.stream_condition.notify_all()

        def stream_events(self, run_id, after):
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("Cache-Control", "no-cache")
            self.send_header("Connection", "close")
            self.send_header("X-Accel-Buffering", "no")
            self.end_headers()
            self.close_connection = True
            self.connection.settimeout(5)
            deadline = time.monotonic() + 30
            while time.monotonic() < deadline and not getattr(self.server, "closing", False):
                events = store.events(run_id, after)
                for event in events:
                    payload = f"id: {event['id']}\ndata: {json.dumps(event, ensure_ascii=False)}\n\n"
                    self.wfile.write(payload.encode("utf-8"))
                    after = event["id"]
                if not events:
                    self.wfile.write(b": heartbeat\n\n")
                self.wfile.flush()
                time.sleep(.5)

        def do_HEAD(self):
            if urlsplit(self.path).path in ("/api/events", "/api/overview/events"):
                return self.send(405, '{"error":"event stream requires GET"}')
            self.do_GET()

        def reject_write(self):
            self.close_connection = True
            self.send(405, '{"error":"read-only dashboard"}')

        do_POST = do_PUT = do_PATCH = do_DELETE = do_OPTIONS = reject_write

    return LabHTTPServer(("127.0.0.1", port), Handler)
