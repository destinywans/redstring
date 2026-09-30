"""Loopback-only read-only dashboard and resumable persisted event stream."""
from __future__ import annotations

import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import re
import time
from urllib.parse import parse_qs, unquote, urlsplit

from store import Store, render_report


class LabHTTPServer(ThreadingHTTPServer):
    daemon_threads = True
    # Rebind after a restart while prior SSE sockets are in TIME_WAIT. An
    # actively listening process still fails bind; SO_REUSEPORT is not enabled.
    allow_reuse_address = True

    def server_close(self):
        self.closing = True
        super().server_close()


def make_server(root, port=8787):
    root = Path(root).resolve()
    store = Store(root)
    static = root / "tools/testlab/static"

    class Handler(BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def log_message(self, format, *args):
            pass

        def send(self, status, body=b"", mime="application/json; charset=utf-8"):
            if isinstance(body, str):
                body = body.encode("utf-8")
            self.send_response(status)
            self.send_header("Content-Type", mime)
            self.send_header("Content-Length", str(len(body)))
            self.send_header("Cache-Control", "no-store")
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
                match = re.fullmatch(r"/api/runs/([0-9a-f-]{36})(?:/report\.(json|html|md))?", path)
                if match:
                    detail = store.detail(match.group(1))
                    fmt = match.group(2)
                    if fmt:
                        mime = {"json": "application/json; charset=utf-8", "html": "text/html; charset=utf-8", "md": "text/markdown; charset=utf-8"}[fmt]
                        return self.send(200, render_report(detail, fmt), mime)
                    return self.send(200, json.dumps(detail, ensure_ascii=False))
                if path == "/api/events":
                    params = parse_qs(parsed.query)
                    run_id = params.get("run_id", [""])[0]
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
            if urlsplit(self.path).path == "/api/events":
                return self.send(405, '{"error":"event stream requires GET"}')
            self.do_GET()

        def reject_write(self):
            self.close_connection = True
            self.send(405, '{"error":"read-only dashboard"}')

        do_POST = do_PUT = do_PATCH = do_DELETE = do_OPTIONS = reject_write

    return LabHTTPServer(("127.0.0.1", port), Handler)
