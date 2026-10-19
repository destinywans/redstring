#!/usr/bin/env python3
"""Rename the Go module path everywhere it appears in tracked files.

Default: OLD below -> github.com/destinywans/redstring, the public repository,
so that `go install` and imports work from outside. The path is replaced in
go.mod, every Go import, and in docs and test tooling that spell out package
paths, including the regex-escaped spelling (github\\.com/...) that testlab
uses to match `go version -m` output. Only whole module paths are replaced
(the old path followed by "/" or a non-path character), and only files
tracked by git.

Dry run by default: prints the files and replacement counts.
--apply writes the files, re-runs gofmt on Go files the rename leaves
unformatted (import order can change when the path sorts differently; files
that were unformatted before are left as they were), then runs
`go build ./...` and `go vet ./...` and looks for any other spelling of the
old path that is left. Exit code 1 when a check fails.
"""
import argparse
import collections
import re
import subprocess
import sys
from pathlib import Path

# Split so this file never matches the search for the old path or rewrites itself.
OLD = "github.com/wans/" + "aiops-canvas"
NEW = "github.com/destinywans/redstring"


def run(cmd, root, **kw):
    return subprocess.run(cmd, cwd=root, text=True, **kw)


def gofmt_path(root):
    """The gofmt of the toolchain `go` selects (GOTOOLCHAIN may switch it), not whatever is on PATH."""
    goroot = run(["go", "env", "GOROOT"], root, check=True, capture_output=True).stdout.strip()
    return str(Path(goroot) / "bin" / "gofmt")


def gofmt_unformatted(gofmt, root, files):
    if not files:
        return set()
    out = run([gofmt, "-l", *files], root, check=True, capture_output=True).stdout
    return set(out.split())


def spellings(old, new):
    """(label, old, new) for the plain path and its regex-escaped form."""
    pairs = [("plain", old, new)]
    if "." in old:
        pairs.append(("regex-escaped", old.replace(".", "\\."), new.replace(".", "\\.")))
    return pairs


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--apply", action="store_true", help="write the changes and run the Go checks (default: dry run)")
    parser.add_argument("--from", dest="old", default=OLD, help="current module path (default: %(default)s)")
    parser.add_argument("--to", dest="new", default=NEW, help="new module path (default: %(default)s)")
    parser.add_argument("--skip-checks", action="store_true", help="with --apply, do not run go build / go vet")
    parser.add_argument("-v", "--verbose", action="store_true", help="list every file, not only a summary per type")
    args = parser.parse_args()

    root = Path(run(["git", "rev-parse", "--show-toplevel"], ".", check=True, capture_output=True).stdout.strip())
    # A module path ends at "/" or at a character that cannot continue a path.
    rules = [(label, re.compile(re.escape(old) + r"(?![A-Za-z0-9_.~-])"), new)
             for label, old, new in spellings(args.old, args.new)]
    files = set()
    for _, old, _ in spellings(args.old, args.new):
        listed = run(["git", "grep", "-l", "-z", "-I", "-F", old], root, capture_output=True)
        if listed.returncode not in (0, 1):
            sys.exit(listed.stderr)
        files.update(p for p in listed.stdout.split("\0") if p)

    plan = []
    by_spelling = collections.Counter()
    for rel in sorted(files):
        path = root / rel
        if path.is_symlink() or not path.is_file():
            continue
        text = path.read_text(encoding="utf-8")
        count = 0
        for label, pattern, new in rules:
            text, n = pattern.subn(new.replace("\\", "\\\\"), text)
            by_spelling[label] += n
            count += n
        if count:
            plan.append((rel, count, text))

    by_type = collections.Counter()
    files_by_type = collections.Counter()
    for rel, n, _ in plan:
        kind = "go.mod" if rel == "go.mod" else (Path(rel).suffix or Path(rel).name)
        by_type[kind] += n
        files_by_type[kind] += 1
        if args.verbose or kind != ".go":
            print(f"{rel}: {n}")
    total = sum(n for _, n, _ in plan)
    print(f"\n{'Replacing' if args.apply else 'Would replace'} {args.old} -> {args.new}: "
          f"{total} occurrences in {len(plan)} files")
    for kind, n in sorted(by_type.items(), key=lambda kv: -kv[1]):
        print(f"  {kind}: {n} in {files_by_type[kind]} files")
    print("  by spelling: " + ", ".join(f"{label} {by_spelling[label]}" for label, _, _ in rules))
    if not plan:
        return 0
    if not args.apply:
        print("Dry run: nothing written. Re-run with --apply to write the files and run go build / go vet.")
        return 0

    go_files = [rel for rel, _, _ in plan if rel.endswith(".go")]
    gofmt = gofmt_path(root)
    unformatted_before = gofmt_unformatted(gofmt, root, go_files)
    for rel, _, new_text in plan:
        (root / rel).write_text(new_text, encoding="utf-8")
    reformat = sorted(gofmt_unformatted(gofmt, root, go_files) - unformatted_before)
    if reformat:
        run([gofmt, "-w", *reformat], root, check=True)
    print(f"gofmt: rewrote {len(reformat)} files whose import order changed"
          + (f"; left {len(unformatted_before)} already-unformatted files as they were" if unformatted_before else ""))

    status = 0
    if not args.skip_checks:
        for cmd in (["go", "build", "./..."], ["go", "vet", "./..."]):
            print("$ " + " ".join(cmd), flush=True)
            if run(cmd, root).returncode != 0:
                print(f"FAILED: {' '.join(cmd)}", file=sys.stderr)
                status = 1
    # Any other spelling of the old owner/name pair, e.g. with "/" escaped.
    owner_name = args.old.split("/", 1)[1] if "/" in args.old else args.old
    loose = re.compile(r"(?<![A-Za-z0-9_-])" + r"\\?/".join(re.escape(p) for p in owner_name.split("/")))
    tracked = run(["git", "ls-files", "-z"], root, check=True, capture_output=True).stdout.split("\0")
    leftover = []
    for rel in filter(None, tracked):
        path = root / rel
        if path.is_symlink() or not path.is_file():
            continue
        try:
            text = path.read_text(encoding="utf-8")
        except UnicodeDecodeError:
            continue
        for number, line in enumerate(text.splitlines(), 1):
            if loose.search(line):
                leftover.append(f"  {rel}:{number}: {line.strip()[:160]}")
    if leftover:
        print("Possible spellings of the old path still present (check by hand):", file=sys.stderr)
        print("\n".join(leftover), file=sys.stderr)
        status = 1
    if status == 0:
        print(("go build and go vet passed; " if not args.skip_checks else "")
              + "no spelling of the old path is left in tracked files.")
    return status


if __name__ == "__main__":
    sys.exit(main())
