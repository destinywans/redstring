import { useMemo } from "react";
import qrcode from "qrcode-generator";

/** A QR code drawn as SVG in the current text color, so it follows the theme. Generated locally; nothing is fetched. */
export function QrCode({ value, size = 156, label }: { value: string; size?: number; label: string }) {
  const { n, path } = useMemo(() => {
    const qr = qrcode(0, "M");
    qr.addData(value);
    qr.make();
    const count = qr.getModuleCount();
    let d = "";
    for (let r = 0; r < count; r++) for (let c = 0; c < count; c++) if (qr.isDark(r, c)) d += `M${c} ${r}h1v1h-1z`;
    return { n: count, path: d };
  }, [value]);
  const quiet = 2;
  return (
    <svg width={size} height={size} viewBox={`${-quiet} ${-quiet} ${n + quiet * 2} ${n + quiet * 2}`} role="img" aria-label={label} shapeRendering="crispEdges" style={{ background: "var(--panel)", borderRadius: 8, border: "1px solid var(--border)", flex: "none" }}>
      <path d={path} fill="var(--text)" />
    </svg>
  );
}
