#!/usr/bin/env python3
"""Replace links into the author's private Linear workspace with plain text.

The links are dead for anyone outside the workspace, so before the repository
goes public every one becomes a plain issue ID or plain text:

  [WAN-12](<base>/issue/WAN-12/slug)        -> WAN-12
  [WAN-12 notes](<base>/issue/WAN-12)       -> WAN-12 notes   (text already names it)
  [需求记录](<base>/issue/WAN-12)            -> 需求记录（WAN-12）
  [Linear 项目](<base>/project/name-1a2b)    -> Linear 项目
  <a href="<base>/issue/WAN-12">记录 ↗</a>  -> <span>记录（WAN-12）</span>
  <base>/issue/WAN-12/slug                  -> WAN-12
  <base>/project/name-0123456789ab          -> name

<base> is https://linear.app/<workspace>. Public Linear pages such as
linear.app/changelog are left alone. Only files tracked by git are read, and
the git history is not touched.

Dry run by default: prints the files and the number of changes in each.
--apply writes the files. Exit code 1 when a link matched the workspace but
fit none of the rules; such links are listed and left unchanged.
"""
import argparse
import collections
import re
import subprocess
import sys
from pathlib import Path

ISSUE_ID = r"[A-Za-z][A-Za-z0-9]*-\d+"
# Bare URLs end at whitespace or punctuation; \w includes CJK, so a Chinese
# slug stays part of the URL and a following "。" or "：" does not.
BARE_SLUG = r"[\w%~-]+"


def has_cjk(text):
    return any("　" <= ch <= "鿿" or "＀" <= ch <= "￯" for ch in text)


def name_from_slug(slug):
    """project/document slugs end in a 12-hex id: "redstring-3c9fce50a53b" -> "redstring"."""
    return re.sub(r"-[0-9a-f]{12}$", "", slug)


class Scrubber:
    def __init__(self, workspace):
        base = r"https?://linear\.app/" + re.escape(workspace) + "/"
        self.any_url = re.compile(base + r"[^\s()<>\"'`\]]*")
        self.issue_url = re.compile(base + r"issue/(?P<id>" + ISSUE_ID + r")(?:/[^\s()\"<>]*)?")
        self.other_url = re.compile(base + r"(?P<kind>project|document)/(?P<slug>[^/\s()\"<>]+)(?:/[^\s()\"<>]*)?")
        self.md_link = re.compile(
            r"\[(?P<text>[^\[\]\n]*)\]\((?P<url>" + base + r"[^\s()]*)(?:\s+\"[^\"\n]*\")?\)"
        )
        self.html_link = re.compile(
            r"<a\s[^>\n]*?href=\"(?P<url>" + base + r"[^\"]*)\"[^>\n]*>(?P<text>[^<\n]*)</a>"
        )
        self.autolink = re.compile(r"<(?P<url>" + base + r"[^\s<>]*)>")
        # Not right after "](" or href=": those are links the rules above could
        # not parse (brackets in the text, say); they are reported, not mangled.
        bare = r"(?<!\]\()(?<!href=\")" + base
        self.bare_issue = re.compile(bare + r"issue/(?P<id>" + ISSUE_ID + r")(?:/" + BARE_SLUG + r")*/?")
        self.bare_other = re.compile(bare + r"(?P<kind>project|document)/(?P<slug>" + BARE_SLUG + r")(?:/" + BARE_SLUG + r")*/?")

    def classify(self, url):
        """Return (kind, issue_id_or_slug) for a full link URL, or None."""
        m = self.issue_url.fullmatch(url)
        if m:
            return "issue", m.group("id")
        m = self.other_url.fullmatch(url)
        if m:
            return m.group("kind"), m.group("slug")
        return None

    @staticmethod
    def labelled(text, kind, ref):
        text = text.strip()
        if kind != "issue":
            return text or name_from_slug(ref)
        if not text or text == ref:
            return ref
        if ref in text:
            return text
        return f"{text}（{ref}）" if has_cjk(text) else f"{text} ({ref})"

    def scrub(self, content):
        """Return (new_content, changes, unhandled_urls)."""
        changes = []

        def link_sub(make):
            def sub(m):
                hit = self.classify(m.group("url"))
                if not hit:
                    return m.group(0)
                kind, ref = hit
                new = make(m, kind, ref)
                changes.append((kind, m.group(0), new))
                return new
            return sub

        def bare_sub(make, kind_of):
            def sub(m):
                new = make(m)
                changes.append((kind_of(m), m.group(0), new))
                return new
            return sub

        content = self.md_link.sub(
            link_sub(lambda m, kind, ref: self.labelled(m.group("text"), kind, ref)), content
        )
        content = self.html_link.sub(
            link_sub(
                lambda m, kind, ref: "<span>"
                + self.labelled(re.sub(r"\s*↗$", "", m.group("text")), kind, ref)
                + "</span>"
            ),
            content,
        )
        content = self.autolink.sub(
            link_sub(lambda m, kind, ref: ref if kind == "issue" else name_from_slug(ref)), content
        )

        content = self.bare_issue.sub(bare_sub(lambda m: m.group("id"), lambda m: "issue"), content)
        content = self.bare_other.sub(
            bare_sub(lambda m: name_from_slug(m.group("slug")), lambda m: m.group("kind")), content
        )
        return content, changes, [m.group(0) for m in self.any_url.finditer(content)]


def tracked_files(root):
    out = subprocess.run(
        ["git", "-C", str(root), "ls-files", "-z"], check=True, capture_output=True
    ).stdout
    return [p for p in out.decode("utf-8").split("\0") if p]


def excluded(path, excludes):
    return any(path == e or path.startswith(e.rstrip("/") + "/") for e in excludes)


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--apply", action="store_true", help="write the changes (default: dry run)")
    parser.add_argument("--workspace", default="wansss", help="Linear workspace URL key (default: %(default)s)")
    parser.add_argument("--exclude", action="append", default=[], metavar="PATH",
                        help="skip a file or directory, relative to the repository root; repeatable")
    parser.add_argument("-v", "--verbose", action="store_true", help="print every replacement")
    args = parser.parse_args()

    root = Path(
        subprocess.run(["git", "rev-parse", "--show-toplevel"], check=True, capture_output=True, text=True).stdout.strip()
    )
    scrubber = Scrubber(args.workspace)
    needle = ("linear.app/" + args.workspace + "/").encode()
    totals = collections.Counter()
    files_changed = 0
    problems = []

    for rel in tracked_files(root):
        if excluded(rel, args.exclude):
            continue
        path = root / rel
        if path.is_symlink() or not path.is_file():
            continue
        raw = path.read_bytes()
        if needle not in raw:
            continue
        try:
            content = raw.decode("utf-8")
        except UnicodeDecodeError:
            problems.append((rel, "not UTF-8 text; left unchanged"))
            continue
        new, changes, unhandled = scrubber.scrub(content)
        for url in unhandled:
            problems.append((rel, url))
        if not changes:
            continue
        files_changed += 1
        kinds = collections.Counter(kind for kind, _, _ in changes)
        totals.update(kinds)
        detail = ", ".join(f"{kinds[k]} {k}" for k in ("issue", "project", "document") if kinds[k])
        print(f"{rel}: {len(changes)} ({detail})")
        if args.verbose:
            for _, old, repl in changes:
                print(f"    {old}\n      -> {repl}")
        if args.apply:
            path.write_text(new, encoding="utf-8")

    total = sum(totals.values())
    summary = ", ".join(f"{totals[k]} {k}" for k in ("issue", "project", "document"))
    verb = "Changed" if args.apply else "Would change"
    print(f"\n{verb} {total} links in {files_changed} files ({summary}).")
    if not args.apply and total:
        print("Dry run: nothing written. Re-run with --apply to write the files.")
    if problems:
        print(f"\n{len(problems)} links left unchanged because no rule fits:", file=sys.stderr)
        for rel, what in problems:
            print(f"  {rel}: {what}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
