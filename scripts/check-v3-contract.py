#!/usr/bin/env python3
"""Check that docs/api/v3-contract.md and the v3 frontend mock routes list the same endpoints.

Each table row in the contract holds one method and one path. The SSE stream is
simulated directly by the frontend and has no mock-route declaration even when
its backend route is implemented.
Rows in the "v1.1 页面交互接口" section (status `v1.1·已实现` / `v1.1·待实现`) are
counted separately and need no mock yet; they must not repeat a v1 row. The mock may
also cover v1.1 rows and the auxiliary endpoints listed in v3-backend-aux.md (pages
such as /invite use them); any other mock route still fails the check.
Exit code 1 when the two sides differ.
"""
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
DOC = ROOT / "docs/api/v3-contract.md"
AUX = ROOT / "docs/api/v3-backend-aux.md"
MOCK = ROOT / "web/src/v3/mock"

ROW = re.compile(r"^\| (GET|POST|PUT|PATCH|DELETE) \| `([^`]+)` \|(.*)$")
MOCK_ROUTE = re.compile(r'mock\("(GET|POST|PUT|PATCH|DELETE)",\s*"([^"]+)"')
DIRECTLY_SIMULATED = {"GET /conversations/{}/events"}


def norm(path: str) -> str:
    return re.sub(r"\{[^}]+\}|:[A-Za-z_]+", "{}", path)


V11 = re.compile(r"v1\.1·(已实现|待实现)")


def doc_routes() -> tuple[set[str], dict[str, str], list[str]]:
    routes, v11, problems = set(), {}, []
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
        key = f"{method} {norm(path)}"
        if key in DIRECTLY_SIMULATED:
            continue
        if "前端模拟" in rest:
            continue
        st = V11.search(rest)
        if st:
            if key in v11:
                problems.append(f"{DOC.name}:{n}: duplicate v1.1 row {method} {path}")
            v11[key] = st.group(1)
            continue
        if key in routes:
            problems.append(f"{DOC.name}:{n}: duplicate row {method} {path}")
        routes.add(key)
    for key in v11.keys() & routes:
        problems.append(f"v1.1 row repeats a v1 row: {key}")
    return routes, v11, problems


def aux_routes() -> set[str]:
    out = set()
    for line in AUX.read_text().splitlines():
        m = ROW.match(line)
        if m:
            out.add(f"{m.group(1)} {norm(m.group(2).removeprefix('/api/v1'))}")
    return out


def mock_routes() -> set[str]:
    out = set()
    for f in MOCK.rglob("*.ts"):
        for method, path in MOCK_ROUTE.findall(f.read_text()):
            out.add(f"{method} {norm(path)}")
    return out


def main() -> int:
    doc, v11, problems = doc_routes()
    mock = mock_routes()
    allowed_extra = set(v11) | aux_routes()
    extra = mock - doc
    for p in problems:
        print(p)
    for r in sorted(extra - allowed_extra):
        print(f"in mock, missing from contract: {r}")
    for r in sorted(doc - mock):
        print(f"in contract, missing from mock: {r}")
    ok = not problems and not (extra - allowed_extra) and not (doc - mock)
    print(f"{'OK' if ok else 'MISMATCH'}: {len(doc)} contract rows, {len(mock & doc)} mocked"
          f" (+{len(extra & allowed_extra)} v1.1/aux mocks)")
    done = sum(1 for v in v11.values() if v == "已实现")
    print(f"v1.1: {len(v11)} rows outside the mock check ({done} implemented, {len(v11) - done} planned)")
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
