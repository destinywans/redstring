#!/usr/bin/env python3
"""Write the Redstring mark files: favicon and touch icon into web/public, masters into docs/brand.

The geometry is the same 64-grid bolt as web/src/v3/ui/BrandMark.tsx; change both together.
SVGs are plain paths and circles (no masks), so they render anywhere. PNGs are drawn by
scripts/brand-raster.swift and therefore need macOS with Swift.
"""
import math
import pathlib
import subprocess
import tempfile

ROOT = pathlib.Path(__file__).resolve().parent.parent
PUBLIC = ROOT / "web" / "public"
DOCS = ROOT / "docs" / "brand"
RASTER = ROOT / "scripts" / "brand-raster.swift"

# Two parallel 45° strokes joined by a short rising jog; clues at the stroke midpoints, the cause at the end.
S, A, B, E = (46, 5), (16, 35), (48, 27), (18, 57)
D1, D2 = (31, 20), (33, 42)
# Optical sizes: L from 48 px, M for 25–47 px, S for 24 px and below (heavier line, no gaps, bigger dots).
OPT = {
    "L": dict(W=3.6, gap=1.4, r=3.8, rr=4.4),
    "M": dict(W=4.2, gap=1.4, r=3.8, rr=4.4),
    "S": dict(W=5.2, gap=0.0, r=4.6, rr=5.3),
}
ON_LIGHT = dict(red="#E5484D", ink="#111113")
ON_DARK = dict(red="#F2555A", ink="#F2F2F3")


def n(v):
    return ("%.2f" % v).rstrip("0").rstrip(".")


def pt(p):
    return f"{n(p[0])} {n(p[1])}"


def along(p, q, at, d):
    """Point d units from `at`, measured along the direction p → q."""
    dx, dy = q[0] - p[0], q[1] - p[1]
    length = math.hypot(dx, dy)
    return at[0] + dx / length * d, at[1] + dy / length * d


def opt_for(px):
    return "S" if px <= 24 else "M" if px < 48 else "L"


def body(o, red, ink, cls=False):
    W, g, r, rr = o["W"], o["gap"], o["r"], o["rr"]
    stroke = 'class="l"' if cls else f'stroke="{red}"'
    fill_red = 'class="r"' if cls else f'fill="{red}"'
    fill_ink = 'class="i"' if cls else f'fill="{ink}"'
    common = f'fill="none" {stroke} stroke-width="{n(W)}" stroke-linejoin="miter" stroke-miterlimit="12"'
    if not g:
        line = f'<path d="M{pt(S)}L{pt(A)}L{pt(B)}L{pt(E)}" {common} stroke-linecap="round"/>'
    else:
        # The thread stops short of every dot so it reads as passing through it.
        d = (
            f"M{pt(S)}L{pt(along(S, A, D1, -(r + g)))}"
            f"M{pt(along(S, A, D1, r + g))}L{pt(A)}L{pt(B)}L{pt(along(B, E, D2, -(r + g)))}"
            f"M{pt(along(B, E, D2, r + g))}L{pt(along(B, E, E, -(rr + g)))}"
        )
        line = f'<path d="{d}" {common} stroke-linecap="butt"/><circle cx="{n(S[0])}" cy="{n(S[1])}" r="{n(W / 2)}" {fill_red}/>'
    dots = (
        f'<circle cx="{n(D1[0])}" cy="{n(D1[1])}" r="{n(r)}" {fill_ink}/>'
        f'<circle cx="{n(D2[0])}" cy="{n(D2[1])}" r="{n(r)}" {fill_ink}/>'
        f'<circle cx="{n(E[0])}" cy="{n(E[1])}" r="{n(rr)}" {fill_red}/>'
    )
    return line + dots


def svg(inner, size=64, label="Redstring"):
    return f'<svg xmlns="http://www.w3.org/2000/svg" width="{size}" height="{size}" viewBox="0 0 64 64" role="img" aria-label="{label}">{inner}</svg>\n'


def tile(px, rounded=True, scale=0.72):
    """Dark app tile; the mark inside picks its optical size from its rendered size."""
    o = OPT[opt_for(px * scale)]
    rx, hrx = (' rx="14.5"', ' rx="14"') if rounded else ("", "")
    defs = (
        '<defs><linearGradient id="bg" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="#26262C"/><stop offset="1" stop-color="#0E0E11"/></linearGradient>'
        '<linearGradient id="hi" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="#fff" stop-opacity=".22"/><stop offset=".5" stop-color="#fff" stop-opacity="0"/></linearGradient></defs>'
    )
    frame = f'<rect width="64" height="64"{rx} fill="url(#bg)"/><rect x=".5" y=".5" width="63" height="63"{hrx} fill="none" stroke="url(#hi)"/>'
    mark = f'<g transform="translate(32 32) scale({scale}) translate(-32 -32)">{body(o, **ON_DARK)}</g>'
    return svg(defs + frame + mark, px, "Redstring app icon")


def png(svg_text, px, dest):
    with tempfile.NamedTemporaryFile("w", suffix=".svg") as tmp:
        tmp.write(svg_text)
        tmp.flush()
        subprocess.run(["swift", str(RASTER), tmp.name, str(px), str(dest)], check=True, capture_output=True)


def main():
    PUBLIC.mkdir(parents=True, exist_ok=True)
    DOCS.mkdir(parents=True, exist_ok=True)
    # Favicon follows the browser's colour scheme; PNGs are fallbacks.
    style = "<style>.l{stroke:#E5484D}.r{fill:#E5484D}.i{fill:#111113}@media (prefers-color-scheme:dark){.l{stroke:#F2555A}.r{fill:#F2555A}.i{fill:#F2F2F3}}</style>"
    (PUBLIC / "favicon.svg").write_text(svg(style + body(OPT["S"], "", "", cls=True), 32))
    png(tile(32), 32, PUBLIC / "favicon-32.png")
    png(tile(180, rounded=False, scale=0.62), 180, PUBLIC / "apple-touch-icon.png")
    # Masters for README, slides and the GitHub social preview.
    (DOCS / "redstring-mark.svg").write_text(svg(body(OPT["L"], **ON_LIGHT)))
    (DOCS / "redstring-mark-on-dark.svg").write_text(svg(body(OPT["L"], **ON_DARK)))
    (DOCS / "redstring-mark-mono.svg").write_text(svg(body(OPT["L"], "currentColor", "currentColor")))
    (DOCS / "redstring-app-icon.svg").write_text(tile(512))
    png(tile(512), 512, DOCS / "redstring-app-icon-512.png")
    for path in sorted([*PUBLIC.glob("favicon*"), PUBLIC / "apple-touch-icon.png", *DOCS.glob("redstring-*")]):
        print(path.relative_to(ROOT))


if __name__ == "__main__":
    main()
