#!/usr/bin/env python3
"""Generate a reviewable mapping for every V3 contract operation.

The V3 contract remains authoritative for fields and implementation status.
This inventory only classifies whether an old URL can be adapted, a response
must aggregate sources, or the operation is new. Classification is planning
metadata, never proof that a V3 handler is usable.
"""

from __future__ import annotations

import argparse
from collections import Counter
from pathlib import Path
import re

ROOT = Path(__file__).resolve().parent.parent
CONTRACT = ROOT / "docs/api/v3-contract.md"
OUTPUT = ROOT / "docs/api/v3-backend-route-matrix.md"
ROW = re.compile(r"^\| (GET|POST|PUT|PATCH|DELETE) \| `([^`]+)` \|")
V3_ENTRY = "# V3_CONTRACT_ENTRY:"

# These views combine several authoritative records or external read sources.
# Other paths absent from the old route set are independent new operations.
AGGREGATES = {
    "GET /me/profile", "GET /members", "GET /roles/{id}",
    "GET /conversations", "GET /conversations/{id}",
    "GET /canvases", "GET /canvases/{id}",
    "GET /inbox", "GET /alert-groups", "GET /alerts/{id}",
    "GET /incidents", "GET /incidents/{id}",
    "GET /services", "GET /services/{id}", "GET /services/{id}/peek",
    "GET /services/{id}/resilience", "GET /topology",
    "GET /metrics/dashboard", "GET /traces", "GET /traces/{id}",
    "GET /logs", "GET /changes", "GET /service-mappings",
    "GET /coverage", "GET /agent-runs", "GET /agent-runs/{id}",
    "GET /agent-runs/{id}/trace", "GET /audit", "GET /audit/{id}",
    "GET /security", "GET /webhooks", "GET /ingest/push",
    "GET /knowledge-sources", "POST /knowledge/search",
}


def legacy_operations() -> set[str]:
    source = (ROOT / "scripts/build-openapi.py").read_text()
    if V3_ENTRY not in source:
        raise ValueError("V3 generator entry point was not found")
    namespace: dict[str, object] = {"__name__": "v3_route_matrix_legacy"}
    exec(compile(source.split(V3_ENTRY, 1)[0], "build-openapi.py", "exec"), namespace)
    paths = namespace["P"]
    return {f"{method.upper()} {path}" for path, methods in paths.items() for method in methods}


def contract_operations() -> list[tuple[str, str, str, str]]:
    section = ""
    operations: list[tuple[str, str, str, str]] = []
    seen: set[str] = set()
    for line in CONTRACT.read_text().splitlines():
        if line.startswith("## "):
            section = line[3:].split("（", 1)[0]
        match = ROW.match(line)
        if not match:
            continue
        method, path = match.groups()
        key = f"{method} {path}"
        if key in seen:
            raise ValueError(f"duplicate contract operation: {key}")
        seen.add(key)
        status = line.rsplit("|", 2)[1].strip()
        if status.startswith("v1.1·"):
            continue  # Page interaction rows keep a separate count from the frozen v1.
        operations.append((section, method, path, status))
    if len(operations) != 166:
        raise ValueError(f"expected 166 V3 operations including SSE, found {len(operations)}")
    return operations


def render() -> str:
    legacy = legacy_operations()
    operations = contract_operations()
    rows: list[tuple[str, str, str, str, str]] = []
    counts: Counter[str] = Counter()
    for section, method, path, status in operations:
        key = f"{method} {path}"
        if key in legacy:
            strategy = "旧路径适配"
        elif key in AGGREGATES:
            strategy = "聚合"
        else:
            strategy = "新增"
        counts[strategy] += 1
        rows.append((section, method, path, strategy, status))
    lines = [
        "# V3 后端接口映射",
        "",
        "由 `scripts/v3-route-matrix.py` 从 `v3-contract.md` 和 V3 接入前的 OpenAPI 生成段生成。",
        "字段定义仍以 `web/src/v3/api/types.ts` 为准；状态仍以契约主表为准。",
        "“旧路径适配”表示 URL 已存在，但必须替换为 V3 语义；“聚合”表示需要拼合多种真实记录；“新增”表示独立新能力。",
        "这些类别不表示接口已实现或已通过数据库联调。",
        "",
        f"共 {len(rows)} 项：旧路径适配 {counts['旧路径适配']}，聚合 {counts['聚合']}，新增 {counts['新增']}。",
        "",
        "| 分组 | 方法 | 路径 | 实施方式 | 契约状态 |",
        "| --- | --- | --- | --- | --- |",
    ]
    for section, method, path, strategy, status in rows:
        lines.append(f"| {section} | {method} | `{path}` | {strategy} | {status} |")
    return "\n".join(lines) + "\n"


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--check", action="store_true", help="fail if the checked-in matrix is stale")
    args = parser.parse_args()
    generated = render()
    if args.check:
        if not OUTPUT.exists() or OUTPUT.read_text() != generated:
            raise SystemExit(f"{OUTPUT} is stale; run python3 scripts/v3-route-matrix.py")
        print("V3 route matrix matches the current contract.")
    else:
        OUTPUT.write_text(generated)
        print(f"Wrote {OUTPUT}")
