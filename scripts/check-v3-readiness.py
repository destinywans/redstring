#!/usr/bin/env python3
"""Check that the version-bound readiness list covers precisely the mock V3 routes."""

from __future__ import annotations

import hashlib
import importlib.util
from pathlib import Path
import re

ROOT = Path(__file__).resolve().parent.parent
CONTRACT = ROOT / "docs/api/v3-contract.md"
TYPES = ROOT / "web/src/v3/api/types.ts"
READINESS = ROOT / "docs/api/v3-route-readiness.md"
MATRIX = ROOT / "scripts/v3-route-matrix.py"
ROW = re.compile(r"^\| ([^|]+) \| (GET|POST|PUT|PATCH|DELETE) \| `([^`]+)` \| ([^|]+) \| ([^|]+) \| ([^|]+) \|$")
CONTRACT_HASH = re.compile(r"`docs/api/v3-contract\.md` SHA-256 `([0-9a-f]{64})`")
TYPES_HASH = re.compile(r"`web/src/v3/api/types\.ts` SHA-256 `([0-9a-f]{64})`")
TOTALS = re.compile(r"主契约 (\d+) 行中 `implemented` (\d+) 行；下表覆盖全部 (\d+) 条 `mock`")
TAGS = {"真实来源可闭合", "需生产者或外部适配", "冻结前端限制", "待评估"}


def main() -> int:
    spec = importlib.util.spec_from_file_location("v3_route_matrix", MATRIX)
    assert spec and spec.loader
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    operations = module.contract_operations()
    problems = []
    sse = ("GET", "/conversations/{id}/events")
    for group, method, path, status in operations:
        valid = {"implemented", "mock（前端模拟）"} if (method, path) == sse else {"implemented", "mock"}
        if status not in valid:
            problems.append(f"invalid contract status for {method} {path}: {status}")
    expected = [(group, method, path) for group, method, path, status in operations if status in {"mock", "mock（前端模拟）"}]
    implemented = sum(status == "implemented" for _, _, _, status in operations)
    document = READINESS.read_text()
    contract_match = CONTRACT_HASH.search(document)
    types_match = TYPES_HASH.search(document)
    totals_match = TOTALS.search(document)
    if not contract_match or contract_match.group(1) != hashlib.sha256(CONTRACT.read_bytes()).hexdigest():
        problems.append("contract SHA-256 changed; review the list and update its basis")
    if not types_match or types_match.group(1) != hashlib.sha256(TYPES.read_bytes()).hexdigest():
        problems.append("types.ts SHA-256 changed; review frontend DTOs and update the list basis")
    totals = (len(operations), implemented, len(expected))
    if not totals_match or tuple(map(int, totals_match.groups())) != totals:
        problems.append(f"documented totals differ from contract: {totals[0]}/{totals[1]}/{totals[2]}")

    found = []
    seen = set()
    for number, line in enumerate(document.splitlines(), 1):
        if not re.match(r"^\| [^|]+ \| (?:GET|POST|PUT|PATCH|DELETE) \|", line):
            continue
        row = ROW.fullmatch(line)
        if not row:
            problems.append(f"readiness:{number}: malformed operation row")
            continue
        group, method, path, labels, evidence, dependency = (part.strip() for part in row.groups())
        key = (group, method, path)
        if key in seen:
            problems.append(f"readiness:{number}: duplicate {method} {path}")
        seen.add(key)
        found.append(key)
        label_set = set(labels.split("、"))
        if not label_set or not label_set <= TAGS or ("待评估" in label_set and len(label_set) > 1):
            problems.append(f"readiness:{number}: invalid readiness tags")
        if not evidence or not dependency:
            problems.append(f"readiness:{number}: evidence and next dependency are required")

    if found != expected:
        missing = set(expected) - set(found)
        extra = set(found) - set(expected)
        for item in sorted(missing):
            problems.append(f"missing mock route: {item[1]} {item[2]}")
        for item in sorted(extra):
            problems.append(f"not a mock route: {item[1]} {item[2]}")
        if not missing and not extra:
            problems.append("route order differs from contract")
    for problem in problems:
        print(problem)
    print(f"{'MISMATCH' if problems else 'OK'}: {len(found)} readiness rows, {len(expected)} mock contract rows")
    return 1 if problems else 0


if __name__ == "__main__":
    raise SystemExit(main())
