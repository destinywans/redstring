#!/usr/bin/env python3
"""Check that docs/api/v3-contract.md and the v3 frontend mock routes list the same endpoints.

Each table row in the contract holds one method and one path. Rows whose status says 前端模拟
(simulated in the frontend, e.g. the SSE stream) have no mock route and are skipped.
Exit code 1 when the two sides differ.
"""
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
DOC = ROOT / "docs/api/v3-contract.md"
MOCK = ROOT / "web/src/v3/mock"

ROW = re.compile(r"^\| (GET|POST|PUT|PATCH|DELETE) \| `([^`]+)` \|(.*)$")
MOCK_ROUTE = re.compile(r'mock\("(GET|POST|PUT|PATCH|DELETE)",\s*"([^"]+)"')


def norm(path: str) -> str:
    return re.sub(r"\{[^}]+\}|:[A-Za-z_]+", "{}", path)


def doc_routes() -> tuple[set[str], list[str]]:
    routes, problems = set(), []
    for n, line in enumerate(DOC.read_text().splitlines(), 1):
        if not line.startswith("| "):
            continue
        m = ROW.match(line)
        if not m:
            if re.match(r"^\| (GET|POST|PUT|PATCH|DELETE)\b", line):
                problems.append(f"{DOC.name}:{n}: row must hold exactly one method and one path")
            continue
        method, path, rest = m.groups()
        if "·" in path or rest.lstrip().startswith("`/"):
            problems.append(f"{DOC.name}:{n}: row must hold exactly one path")
        if "前端模拟" in rest:
            continue
        key = f"{method} {norm(path)}"
        if key in routes:
            problems.append(f"{DOC.name}:{n}: duplicate row {method} {path}")
        routes.add(key)
    return routes, problems


def mock_routes() -> set[str]:
    out = set()
    for f in MOCK.rglob("*.ts"):
        for method, path in MOCK_ROUTE.findall(f.read_text()):
            out.add(f"{method} {norm(path)}")
    return out


def main() -> int:
    doc, problems = doc_routes()
    mock = mock_routes()
    for p in problems:
        print(p)
    for r in sorted(mock - doc):
        print(f"in mock, missing from contract: {r}")
    for r in sorted(doc - mock):
        print(f"in contract, missing from mock: {r}")
    ok = not problems and doc == mock
    print(f"{'OK' if ok else 'MISMATCH'}: {len(doc)} contract rows, {len(mock)} mock routes")
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
