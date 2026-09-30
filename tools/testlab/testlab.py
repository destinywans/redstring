#!/usr/bin/env python3
"""Local backend testlab CLI. All network targets are explicit isolated fixtures."""
from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import signal
import sys
import uuid

from store import Store, render_report, scrub


def main(argv=None):
    parser = argparse.ArgumentParser(description="AIOps 独立后端测试实验室")
    parser.add_argument("--root", default=str(Path(__file__).resolve().parents[2]))
    sub = parser.add_subparsers(dest="action", required=True)
    sub.add_parser("doctor")
    prep = sub.add_parser("prepare")
    prep.add_argument("--run-id")
    run = sub.add_parser("run")
    run.add_argument("--suite", required=True)
    run.add_argument("--manifest")
    serve = sub.add_parser("serve")
    serve.add_argument("--port", type=int, default=8787)
    report = sub.add_parser("report")
    report.add_argument("--run-id", required=True)
    report.add_argument("--format", choices=("json", "html", "md", "all"), default="all")
    clean = sub.add_parser("cleanup")
    clean.add_argument("--run-id", required=True)
    args = parser.parse_args(argv)
    root = Path(args.root).resolve()
    if args.action in ("doctor", "prepare", "cleanup"):
        import env
        if args.action == "doctor":
            checks = env.doctor(root)
            print(json.dumps(checks, ensure_ascii=False, indent=2))
            return 0 if checks and all(check.get("status") == "passed" for check in checks) else 2
        elif args.action == "prepare":
            from prepare import prepare_all
            data = prepare_all(root, args.run_id or "testlab-" + uuid.uuid4().hex[:12])
            state = root / ".local/testlab/environment.json"
            if not state.exists():
                state.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
                state.write_text(json.dumps(data, indent=2))
                os.chmod(state, 0o600)
            print(json.dumps({key: value for key, value in data.items() if key != "env"}, ensure_ascii=False, indent=2))
        else:
            print(json.dumps(env.cleanup(root, args.run_id), ensure_ascii=False, indent=2))
        return 0
    if args.action == "serve":
        from server import make_server
        server = make_server(root, args.port)
        print(f"测试看板：http://127.0.0.1:{server.server_port}", flush=True)
        try:
            server.serve_forever()
        except KeyboardInterrupt:
            pass
        finally:
            server.server_close()
        return 0
    if args.action == "report":
        store = Store(root)
        detail = store.detail(args.run_id)
        formats = ("json", "html", "md") if args.format == "all" else (args.format,)
        target = store.data / "reports" / args.run_id
        target.mkdir(parents=True, exist_ok=True, mode=0o700)
        for fmt in formats:
            output = target / ("report." + fmt)
            output.write_text(render_report(detail, fmt))
            os.chmod(output, 0o600)
            print(output)
        return 0
    if args.action == "run":
        from runner import Runner
        prepared_path = root / ".local/testlab/environment.json"
        prepared = json.loads(prepared_path.read_text()).get("env", {}) if prepared_path.exists() else {}
        runner = Runner(root, args.manifest, prepared)
        def stop(sig, frame):
            runner.request_stop()
        signal.signal(signal.SIGTERM, stop)
        signal.signal(signal.SIGINT, stop)
        detail = runner.run(args.suite)
        print(json.dumps({"run_id": detail["run"]["id"], "status": detail["run"]["status"], "summary": detail["summary"]}, ensure_ascii=False), flush=True)
        return 0 if detail["run"]["status"] == "passed" else 2
    return 1


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (OSError, ValueError, KeyError, RuntimeError) as exc:
        print("testlab: " + scrub(str(exc)), file=sys.stderr)
        raise SystemExit(2)
