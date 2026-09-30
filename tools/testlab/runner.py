"""Fixed-manifest subprocess runner; never imports the project's business .env."""
from __future__ import annotations

import json
import hashlib
import ast
import glob
import os
from pathlib import Path
import re
import selectors
import signal
import subprocess
import threading
import time

from store import Store, STATUSES, MAX_LOG_BYTES, bounded, now, scrub

ENV_ALLOWLIST = {"PATH", "HOME", "USER", "LOGNAME", "TMPDIR", "TEMP", "TMP", "LANG", "LC_ALL", "LC_CTYPE", "GOPATH", "GOCACHE", "GOMODCACHE", "GOPROXY", "GOSUMDB", "GOPRIVATE", "GONOSUMDB", "GONOPROXY", "GOTOOLCHAIN", "GOROOT", "CGO_ENABLED", "CC", "CXX", "SDKROOT"}
PLACEHOLDER = re.compile(r"\$\{([A-Z][A-Z0-9_]*)\}")
MAX_PROTOCOL_FRAME = 1024 * 1024


def command_output(proc, deadline, stop, terminate):
    """Yield bounded complete lines; EOF never cancels a process deadline."""
    selector = selectors.DefaultSelector()
    buffers = {}
    discarded = set()
    pipes = (proc.stdout, proc.stderr)
    try:
        for source, pipe in zip(("stdout", "stderr"), pipes):
            os.set_blocking(pipe.fileno(), False)
            selector.register(pipe, selectors.EVENT_READ, source)
            buffers[source] = bytearray()
        while selector.get_map() or proc.poll() is None:
            if stop.is_set() or time.monotonic() >= deadline:
                terminate()
                yield "stop" if stop.is_set() else "timeout", ""
                return
            ready = selector.select(min(.2, max(0, deadline - time.monotonic())))
            if not selector.get_map() and not ready:
                stop.wait(min(.05, max(0, deadline - time.monotonic())))
            for key, _ in ready:
                source, pipe = key.data, key.fileobj
                try:
                    chunk = os.read(pipe.fileno(), 65536)
                except BlockingIOError:
                    continue
                if not chunk:
                    if buffers[source] and source not in discarded:
                        try:
                            yield source, buffers[source].decode("utf-8")
                        except UnicodeDecodeError:
                            yield "invalid" if source == "stdout" else "stderr", "[invalid UTF-8 output]\n"
                    selector.unregister(pipe)
                    pipe.close()
                    continue
                parts = chunk.split(b"\n")
                for index, part in enumerate(parts):
                    complete = index < len(parts) - 1
                    if source not in discarded:
                        if len(buffers[source]) + len(part) > MAX_PROTOCOL_FRAME:
                            buffers[source].clear()
                            discarded.add(source)
                            yield "oversize" if source == "stdout" else "stderr", "[output frame exceeds 1 MiB]\n"
                        else:
                            buffers[source].extend(part)
                    if complete:
                        if source not in discarded:
                            try:
                                yield source, buffers[source].decode("utf-8") + "\n"
                            except UnicodeDecodeError:
                                yield "invalid" if source == "stdout" else "stderr", "[invalid UTF-8 output]\n"
                        buffers[source].clear()
                        discarded.discard(source)
    finally:
        selector.close()
        for pipe in pipes:
            if not pipe.closed:
                pipe.close()


def command_env(explicit, prepared):
    env = {key: value for key, value in os.environ.items() if key in ENV_ALLOWLIST}
    for key, value in explicit.items():
        if not re.fullmatch(r"[A-Z][A-Z0-9_]*", key):
            raise ValueError("invalid explicit environment key")
        def replace(match):
            name = match.group(1)
            if name not in prepared:
                raise ValueError("环境准备项缺失：" + name)
            return str(prepared[name])
        env[key] = PLACEHOLDER.sub(replace, str(value))
    return env


def git_value(root, *args):
    try:
        return subprocess.check_output(["git", "-C", str(root), *args], stderr=subprocess.DEVNULL, text=True).strip()
    except (subprocess.SubprocessError, OSError):
        return "unknown"


def source_fingerprint(root):
    """Hash actual backend inputs, including uncommitted code, without reading secrets."""
    try:
        paths = subprocess.check_output(["git", "-C", str(root), "ls-files", "--cached", "--others", "--exclude-standard", "-z"], stderr=subprocess.DEVNULL).decode().split("\0")
    except (OSError, subprocess.SubprocessError):
        return "unknown"
    root=Path(root).resolve()
    selected={name for name in paths if name and (name in ("go.mod","go.sum") or name.endswith('.go') and not name.startswith('web/') or name.startswith('migrations/') or name=='internal/apicontract/openapi.json')}
    try:
        for name in tuple(selected):
            source=root/name
            if not name.endswith('.go') or not source.is_file() or source.is_symlink():continue
            for directive in re.finditer(r'^\s*//go:embed\s+([^\r\n]+)',source.read_text(),re.MULTILINE):
                rest=directive.group(1).strip()
                while rest:
                    token=re.match(r'"(?:[^"\\]|\\.)*"|`[^`]*`|[^\s]+',rest)
                    if not token:raise ValueError('invalid Go embed directive')
                    raw=token.group(0);rest=rest[token.end():]
                    if rest and not rest[0].isspace():raise ValueError('invalid Go embed token separator')
                    rest=rest.strip()
                    pattern=ast.literal_eval(raw) if raw.startswith('"') else raw[1:-1] if raw.startswith('`') else raw
                    include_hidden=pattern.startswith('all:');pattern=pattern[4:] if include_hidden else pattern
                    if not pattern or pattern.startswith('/') or any(p in ('','..','.') for p in pattern.split('/')) or '\\' in pattern:
                        raise ValueError('Go embed pattern escapes package')
                    matches=glob.glob(pattern,root_dir=source.parent,recursive=False,include_hidden=include_hidden)
                    if not matches:raise ValueError('Go embed pattern has no inputs')
                    for match in matches:
                        path=source.parent/match
                        if path.is_symlink():raise ValueError('Go embed input is a symlink')
                        files=path.rglob('*') if path.is_dir() else [path]
                        for item in files:
                            if item.is_symlink():raise ValueError('Go embed input is a symlink')
                            if not item.is_file():continue
                            if path.is_dir() and not include_hidden and any(p.startswith(('.','_')) for p in item.relative_to(path).parts):continue
                            relative=item.relative_to(root)
                            if any(p in ('.env','.local','.git') for p in relative.parts):raise ValueError('Go embed input is private state')
                            selected.add(relative.as_posix())
        digest=hashlib.sha256()
        for name in sorted(selected):
            path=root/name
            if path.is_symlink():raise ValueError('backend source input is a symlink')
            if not path.is_file():continue
            digest.update(name.encode()+b'\0');digest.update(path.read_bytes())
        return digest.hexdigest()
    except (OSError,ValueError,SyntaxError):
        return 'unknown'


class Runner:
    def __init__(self, root, manifest=None, prepared=None):
        self.root = Path(root).resolve()
        self.store = Store(self.root)
        self.manifest_path = Path(manifest) if manifest else self.root / "tools/testlab/suites.json"
        self.manifest = json.loads(self.manifest_path.read_text())
        self.prepared = prepared or {}
        self.stop = threading.Event()
        self.process = None
        self.run_id = None

    def request_stop(self):
        self.stop.set()
        self.terminate_process()

    def terminate_process(self):
        proc = self.process
        if not proc or proc.poll() is not None:
            return
        try:
            if os.name == "posix":
                os.killpg(proc.pid, signal.SIGTERM)
            else:
                proc.terminate()
            proc.wait(timeout=3)
        except subprocess.TimeoutExpired:
            if os.name == "posix":
                os.killpg(proc.pid, signal.SIGKILL)
            else:
                proc.kill()
            proc.wait(timeout=3)
        except ProcessLookupError:
            pass

    def run(self, suite_name):
        suites = self.manifest.get("suites", [])
        selected = suites if suite_name == "full" else [s for s in suites if s["id"] in suite_name.split(",")]
        if not selected:
            raise ValueError("没有匹配的测试组：" + suite_name)
        if suite_name != "full" and set(suite_name.split(",")) != {s["id"] for s in selected}:
            raise ValueError("包含未知测试组")
        self.store.reconcile_dead_runs()
        source_start = source_fingerprint(self.root)
        run = self.store.create_run(git_value(self.root, "rev-parse", "HEAD"), git_value(self.root, "branch", "--show-current"), selected_suite=suite_name, source_tree_sha=source_start, manifest_sha=hashlib.sha256(self.manifest_path.read_bytes()).hexdigest())
        self.run_id = run["id"]
        print(json.dumps({"run_id": self.run_id, "status": "running"}), flush=True)
        self.store.update("runs", self.run_id, status="running", stage="初始化")
        self.store.add_record(self.run_id, "environment", {"kind": "source", "dirty": bool(git_value(self.root, "status", "--porcelain")), "prepared_keys": sorted(self.prepared), "manifest": str(self.manifest_path.relative_to(self.root)) if self.manifest_path.is_relative_to(self.root) else self.manifest_path.name})
        try:
            from env import public_snapshot
            self.store.add_record(self.run_id,'environment',public_snapshot(self.root))
        except (OSError,ValueError,RuntimeError) as error:
            self.store.add_record(self.run_id,'environment',{'kind':'testlab','status':'unverified','reason':scrub(str(error))})
        if (self.root/'internal/apicontract/openapi.json').exists():
            from inventory import interfaces,attach_defects
            for row in interfaces(self.root):self.store.add_record(self.run_id,'interfaces',row)
            attach_defects(self.store,self.run_id)
        gaps = self.root / "tools/testlab/gaps.json"
        if gaps.exists():
            parsed = json.loads(gaps.read_text())
            for gap in parsed if isinstance(parsed, list) else parsed.get("gaps", []):
                self.store.add_record(self.run_id, "gaps", gap)
        scheduled = [(spec, self.store.add_suite(self.run_id, spec)) for spec in selected]
        done = threading.Event()
        heartbeat = threading.Thread(target=self.heartbeat, args=(done,), daemon=True)
        heartbeat.start()
        try:
            for spec, suite in scheduled:
                if self.stop.is_set():
                    break
                self.execute_suite(spec, suite)
            if self.stop.is_set():
                self.store.interrupt(self.run_id, "用户终止或运行进程中断")
            else:
                source_end = source_fingerprint(self.root)
                if source_start == "unknown" or source_end == "unknown" or source_start != source_end:
                    suite = self.store.add_suite(self.run_id, {"id": "source-audit", "name": "源码一致性审计"})
                    unavailable = source_start == "unknown" or source_end == "unknown"
                    case, attempt = self.new_case({"id": "source-audit", "type": "audit"}, suite, "源码指纹不可用" if unavailable else "运行期间源码发生变化", "runner")
                    self.store.finish_attempt(case, attempt, "blocked", "无法证明源码一致性；修复指纹采集后需重新验收" if unavailable else "测试结束源码与开始时不同；保留结果供调查，冻结源码后需重新验收")
                    self.store.update("suites", suite["id"], status="blocked", ended_at=now())
                self.store.update("runs", self.run_id, source_tree_sha_end=source_end)
                detail = self.store.detail(self.run_id)
                counts = detail["summary"]
                status = "failed" if counts["failed"] else "blocked" if any(counts[key] for key in ("skipped", "blocked", "not_implemented", "external_todo", "queued", "running", "interrupted")) else "passed"
                self.store.update("runs", self.run_id, status=status, ended_at=now(), stage="执行结束", heartbeat=now())
        except BaseException as exc:
            self.terminate_process()
            self.store.interrupt(self.run_id, "运行器中断：" + scrub(str(exc), self.prepared.values()))
            raise
        finally:
            done.set()
            heartbeat.join(timeout=2)
        return self.store.detail(self.run_id)

    def heartbeat(self, done):
        while not done.wait(3):
            self.store.update("runs", self.run_id, heartbeat=now())

    def execute_suite(self, spec, suite):
        self.store.update("runs", self.run_id, stage=spec.get("name", spec["id"]))
        self.store.update("suites", suite["id"], status="running", started_at=now())
        if not spec.get("commands"):
            status = spec.get("status", "blocked")
            if status not in ("external_todo", "not_implemented", "blocked", "skipped"):
                status = "blocked"
            case, attempt = self.new_case(spec, suite, spec.get("name", spec["id"]), spec["id"])
            self.store.finish_attempt(case, attempt, status, spec.get("reason", "没有可执行测试命令"))
            self.store.update("suites", suite["id"], status=status, ended_at=now())
            return
        for interface in spec.get("interfaces", []):
            self.store.add_record(self.run_id, "interfaces", interface)
        for command in spec.get("commands", []):
            if self.stop.is_set():
                break
            self.execute_command(spec, suite, command)
        detail = self.store.detail(self.run_id)
        cases = [c for c in detail["cases"] if c["suite_id"] == suite["id"]]
        status = "interrupted" if self.stop.is_set() else "failed" if any(c["status"] == "failed" for c in cases) else "blocked" if not cases or any(c["status"] != "passed" for c in cases) else "passed"
        self.store.update("suites", suite["id"], status=status, ended_at=now())

    def execute_command(self, spec, suite, command):
        argv = command.get("argv")
        if not isinstance(argv, list) or not argv or any(not isinstance(a, str) for a in argv):
            return self.blocked_command(spec, suite, "命令必须为固定 argv 列表")
        cwd = (self.root / command.get("cwd", ".")).resolve()
        if not cwd.is_relative_to(self.root):
            return self.blocked_command(spec, suite, "命令工作目录不在仓库内")
        try:
            explicit = dict(spec.get("env", {}), **command.get("env", {}))
            env = command_env(explicit, self.prepared)
            if argv[0] == "go":
                env.setdefault("GOCACHE", str(self.root / ".local/go-cache"))
        except ValueError as exc:
            return self.blocked_command(spec, suite, str(exc))
        secrets = list(self.prepared.values()) + [v for k, v in env.items() if any(word in k for word in ("TOKEN", "SECRET", "PASSWORD", "DSN", "DATABASE_URL"))]
        parser = command.get("parser", "command")
        if parser not in ("command", "go_json", "testlab_json"):
            return self.blocked_command(spec, suite, "未知结果解析器")
        started = time.monotonic()
        logs = []
        log_size = 0
        cases = {}
        saw_terminal = False
        timeout = float(command.get("timeout_seconds", 600))
        evidence_text = "命令：" + scrub(json.dumps(argv, ensure_ascii=False), secrets) + "\n"
        shell_case = None
        if parser == "command":
            shell_case = self.new_case(spec, suite, command.get("name", " ".join(argv)), argv[0])
            cases[("command", "")] = shell_case
        try:
            self.process = subprocess.Popen(argv, cwd=cwd, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=(os.name == "posix"))
        except OSError as exc:
            if shell_case:
                self.store.finish_attempt(*shell_case, "blocked", "无法启动命令：" + str(exc))
                return
            return self.blocked_command(spec, suite, "无法启动命令：" + str(exc))
        timed_out = False
        malformed = False
        for source, line in command_output(self.process, started + timeout, self.stop, self.terminate_process):
            if source in ("timeout", "stop"):
                timed_out = source == "timeout"
                continue
            clean = scrub(line, secrets)
            if log_size < MAX_LOG_BYTES:
                chunk = clean[:MAX_LOG_BYTES - log_size]
                logs.append(chunk)
                log_size += len(chunk.encode("utf-8"))
            if source in ("oversize", "invalid"):
                malformed = parser != "command" or malformed
                continue
            if source == "stderr":
                continue  # Diagnostics remain evidence, never protocol results.
            if parser in ("go_json", "testlab_json"):
                try:
                    item = json.loads(line)
                except json.JSONDecodeError:
                    if parser == "testlab_json":
                        malformed = True
                    continue  # go tool diagnostics are retained in command evidence
                if not isinstance(item, dict):
                    malformed = True
                    continue
                if parser == "go_json":
                    package, name = item.get("Package", ""), item.get("Test", "")
                    action = item.get("Action")
                    if not name:
                        if action == "fail":
                            case, attempt = self.new_case(spec, suite, "包构建/初始化/清理", package)
                            self.store.finish_attempt(case, attempt, "failed", clean, item.get("Elapsed", 0), secrets=secrets)
                            saw_terminal = True
                        elif action == "skip":
                            case, attempt = self.new_case(spec, suite, "包无可执行测试", package)
                            self.store.finish_attempt(case, attempt, "skipped", clean, secrets=secrets)
                            saw_terminal = True
                        continue
                    key = (package, name)
                    if key not in cases:
                        cases[key] = self.new_case(spec, suite, name, package)
                    if action == "run":
                        self.store.update("runs", self.run_id, current_test=scrub(name, secrets), current_module=package, last_progress=now())
                    case, attempt = cases[key]
                    if action == "output":
                        case["output"] = bounded(case.get("output", "") + clean, 8192)
                    elif action in ("pass", "fail", "skip"):
                        status = dict(pass_="passed", fail="failed", skip="skipped")["pass_" if action == "pass" else action]
                        if attempt.get("status") == "running":
                            self.store.finish_attempt(case, attempt, status, case.get("output", clean), item.get("Elapsed", 0), secrets=secrets)
                            attempt["status"] = status
                            saw_terminal = True
                else:
                    status = item.get("status")
                    if status == "running":
                        actual = item.get("actual")
                        timing = actual if isinstance(actual, dict) else {}
                        progress = {"suite_id": suite["id"], "name": scrub(item.get("name", ""), secrets), "actual": scrub(item.get("actual", ""), secrets), "elapsed": item.get("elapsed", timing.get("elapsed_seconds")), "planned_seconds": item.get("planned_seconds", timing.get("planned_seconds"))}
                        self.store.update("runs", self.run_id, current_test=progress["name"], elapsed=progress["elapsed"], planned_seconds=progress["planned_seconds"], last_progress=now())
                        self.store.emit(self.run_id, "progress", progress)
                        continue
                    if status not in STATUSES or status in ("queued", "running"):
                        malformed = True
                        continue
                    key = (item.get("module", ""), item.get("id", item.get("name", "")))
                    if key in cases:
                        malformed = True  # each line represents one immutable attempt
                        continue
                    fields = {key: item[key] for key in ("type", "evidence_mode", "expected", "gap_ids") if key in item}
                    case, attempt = self.new_case(spec, suite, item.get("name", "未命名测试"), item.get("module", spec["id"]), **fields)
                    cases[key] = (case, attempt)
                    evidence = self.store.write_evidence(self.run_id, clean, secrets=secrets)
                    self.store.finish_attempt(case, attempt, status, item.get("actual", ""), item.get("duration", 0), [evidence], secrets=secrets)
                    attempt["status"] = status
                    saw_terminal = True
                    for field, kind in (("interface", "interfaces"), ("metrics", "metrics"), ("defect", "defects")):
                        if isinstance(item.get(field), dict):
                            def redact_values(value):
                                if isinstance(value, str):
                                    return scrub(value, secrets)
                                if isinstance(value, dict):
                                    return {key: redact_values(v) for key, v in value.items()}
                                if isinstance(value, list):
                                    return [redact_values(v) for v in value]
                                return value
                            self.store.add_record(self.run_id, kind, redact_values(item[field]))
        exit_code = self.process.wait(timeout=3)
        self.process = None
        duration = time.monotonic() - started
        if log_size >= MAX_LOG_BYTES:
            logs.append("\n[日志达到大小上限，后续内容未保存]\n")
        evidence = self.store.write_evidence(self.run_id, evidence_text + "".join(logs), secrets=secrets)
        for case, attempt in cases.values():
            if attempt.get("status") == "running":
                status = "interrupted" if self.stop.is_set() else "failed" if timed_out or exit_code else "blocked" if parser != "command" else "passed"
                self.store.finish_attempt(case, attempt, status, "命令超时" if timed_out else "缺少最终测试结果" if parser != "command" else "".join(logs), duration, [evidence], secrets=secrets)
            elif parser == "go_json":
                # Attempts are immutable: link aggregate command evidence separately.
                self.store.add_record(self.run_id, "environment", {"kind": "command_evidence", "case_id": case["id"], "attempt_id": attempt["id"], "evidence_id": evidence})
        if parser != "command" and (not saw_terminal or malformed or timed_out or exit_code != 0) and not self.stop.is_set():
            case, attempt = self.new_case(spec, suite, command.get("name", "命令结果审计"), "runner")
            self.store.finish_attempt(case, attempt, "failed" if exit_code or timed_out or malformed else "blocked", f"exit={exit_code}; timeout={timed_out}; malformed={malformed}; terminal_results={saw_terminal}", duration, [evidence])

    def new_case(self, spec, suite, name, module, **fields):
        fields.setdefault("type", spec.get("type", "functional"))
        fields.setdefault("evidence_mode", spec.get("evidence_mode", "source"))
        fields.setdefault("gap_ids", spec.get("gap_ids", []))
        case = self.store.add_case(self.run_id, suite["id"], name=name, module=module, **fields)
        return case, self.store.start_attempt(case)

    def blocked_command(self, spec, suite, reason):
        case, attempt = self.new_case(spec, suite, "运行前置条件", "runner")
        self.store.finish_attempt(case, attempt, "blocked", reason)
