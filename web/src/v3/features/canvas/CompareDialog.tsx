import type { CanvasNode } from "../../api/types";
import { Badge, Btn, Col, Icon, IconBtn, KV, Row, Spacer, Strong, Tile } from "../../ui/core";
import { useI18n } from "../../i18n";

/** Side-by-side comparison of two canvas nodes, with a merge shortcut. */
export function CompareDialog({ a, b, onClose, onMerge }: { a?: CanvasNode; b?: CanvasNode; onClose: () => void; onMerge: () => void }) {
  const { tx } = useI18n();
  if (!a || !b) return null;
  const col = (n: CanvasNode, icon: "database" | "layers", concl: string) => (
    <Col gap={8} style={{ padding: "12px 14px" }}>
      <Row gap={8}>
        <Tile icon={icon} tone="accent" size={22} />
        <Strong>
          {n.title} {n.context.version}
        </Strong>
      </Row>
      <KV k={tx("结论", "Conclusion")} kw={48}>
        {concl}
      </KV>
      <KV k={tx("证据", "Evidence")} kw={48}>
        {(n.evidence_refs ?? [tx("配置差异", "Config diff")]).join("、")}
      </KV>
      <KV k={tx("置信度", "Confidence")} kw={48}>
        <Badge tone="ok">{tx("高", "High")}</Badge>
      </KV>
      <KV k={tx("上下文", "Context")} kw={48}>
        {tx("{m} 条消息 · {t}k", "{m} messages · {t}k", { m: n.context.messages, t: (n.context.tokens / 1000).toFixed(1) })}
      </KV>
    </Col>
  );
  return (
    <div className="cv-compare anim-pop" role="dialog" aria-label={tx("节点对比", "Compare nodes")} onPointerDown={(e) => e.stopPropagation()}>
      <Row gap={8} style={{ padding: "10px 14px", borderBottom: "1px solid var(--border)" }}>
        <Icon name="layers" size={15} color="var(--accent-text)" />
        <Strong size={13}>{tx("对比两个节点", "Compare two nodes")}</Strong>
        <Spacer />
        <IconBtn icon="x" label={tx("关闭对比", "Close")} onClick={onClose} />
      </Row>
      <div className="cmp-grid">
        {col(a, "database", tx("连接池被打满", "Pool saturated"))}
        {col(b, "layers", tx("生产 50 / 预发 200", "Prod 50 / staging 200"))}
      </div>
      <Row gap={8} style={{ padding: "10px 14px", borderTop: "1px solid var(--border)", background: "var(--sunken)" }}>
        <Icon name="check" size={14} color="var(--ok)" />
        <span style={{ fontSize: "var(--fs-sub)", flexGrow: 1 }}>{tx("两个结论互相印证，没有冲突", "The conclusions agree — no conflict")}</span>
        <Btn kind="primary" icon="merge" h={26} onClick={onMerge}>
          {tx("合并到主线", "Merge to main")}
        </Btn>
      </Row>
    </div>
  );
}
