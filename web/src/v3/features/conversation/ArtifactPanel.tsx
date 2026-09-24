import { useState } from "react";
import type { Artifact } from "../../api/types";
import { Avatar, Badge, Btn, Card, Chip, Icon, IconBtn, Muted, Row, SearchBox, Spacer, Strong, Textarea, cx, type IconName } from "../../ui/core";
import { DataTable } from "../../ui/data";
import { toast } from "../../ui/overlays";
import { Markdown } from "../../ui/Markdown";
import { Lines, C, CS } from "../../charts";
import { DiffView } from "./Parts";
import { useI18n } from "../../i18n";

export const ARTIFACT_ICON: Record<Artifact["kind"], IconName> = { report: "file", chart: "activity", table: "grid", diff: "code" };

function ReportView({ a }: { a: Extract<Artifact["content"], { kind: "report" }>; meta: Artifact }) {
  const { tx } = useI18n();
  const [editing, setEditing] = useState(false);
  const [text, setText] = useState(a.markdown);
  return (
    <div className="art-report anim-fade">
      <Row gap={6}>
        <Badge mono>v3</Badge>
        <Muted size={11.5}>{tx("AI 修改于 14:26 · 基于你的 1 条批注", "AI revised at 14:26 · based on your note")}</Muted>
        <Spacer />
        <Chip icon="layers">{tx("对比 v2", "Compare v2")}</Chip>
        <Btn h={26} icon={editing ? "check" : "edit"} kind={editing ? "primary" : "secondary"} onClick={() => { setEditing((e) => !e); if (editing) toast(tx("已保存为 v4", "Saved as v4")); }}>
          {editing ? tx("保存", "Save") : tx("编辑", "Edit")}
        </Btn>
      </Row>
      {a.editing_note && (
        <div className="art-editing">
          <span className="v3-spinner spin" />
          {a.editing_note}
        </div>
      )}
      {editing ? <Textarea value={text} onChange={(e) => setText(e.target.value)} style={{ minHeight: 420, fontFamily: "var(--mono)", fontSize: 12.5 }} /> : <Markdown text={text} />}
      {a.annotations.map((n) => (
        <div key={n.text} className="art-note">
          <Avatar name={n.author} size={20} accent />
          <span>
            {tx("批注：", "Note: ")}
            {n.text}
          </span>
        </div>
      ))}
    </div>
  );
}

function ChartCompare({ a }: { a: Extract<Artifact["content"], { kind: "chart" }> }) {
  const { tx } = useI18n();
  return (
    <div className="v3-col anim-fade" style={{ gap: 14, padding: "20px 24px" }}>
      <Row gap={8}>
        <Strong>{tx("连接等待 P95 与 5xx 比例 · 联动", "Connection wait P95 vs 5xx · linked")}</Strong>
        <Spacer />
        <Chip icon="clock" caret>
          14:00 – 14:35
        </Chip>
      </Row>
      {a.panels.map((p, i) => (
        <Card key={p.title} style={{ padding: "12px 14px" }}>
          <Row gap={6}>
            <Muted>{p.title}</Muted>
            <Spacer />
            <b className="tnum" style={{ color: "var(--danger)" }}>
              {p.peak}
            </b>
          </Row>
          <Lines series={[{ name: p.series.name, values: p.series.values, color: i ? C[6] : C[2], fill: i ? CS[6] : CS[2] }]} height={120} max={i ? 10 : 520} threshold={p.threshold} markers={a.markers} labels={a.labels} format={(v) => `${Math.round(v * 10) / 10} ${p.unit}`} />
        </Card>
      ))}
      <Muted size={11.5}>{tx("两个面板共享时间轴；悬停查看同一时刻的数值。", "Both panels share a time axis; hover to read values at the same moment.")}</Muted>
    </div>
  );
}

function CsvView({ a, title }: { a: Extract<Artifact["content"], { kind: "table" }>; title: string }) {
  const { tx } = useI18n();
  const [q, setQ] = useState("");
  const rows = a.rows.filter((r) => !q || r.join(" ").toLowerCase().includes(q.toLowerCase()));
  const num = (v: string | number) => parseFloat(String(v)) || 0;
  return (
    <div className="v3-col anim-fade" style={{ gap: 12, padding: "20px 24px" }}>
      <Row gap={8}>
        <Strong>{title}</Strong>
        <Badge>{tx("{n} 行", "{n} rows", { n: a.rows.length })}</Badge>
        <Spacer />
        <SearchBox placeholder={tx("筛选", "Filter")} value={q} onChange={setQ} width={160} />
        <Btn icon="download" onClick={() => toast(tx("已导出 CSV", "CSV exported"))}>
          {tx("导出", "Export")}
        </Btn>
      </Row>
      <Card style={{ overflow: "hidden" }}>
        <DataTable<(string | number)[]>
          rows={rows}
          rowKey={(r) => String(r[0])}
          rowHeight={36}
          columns={a.columns.map((c, i) => ({
            key: String(i),
            header: c,
            sort: (x, y) => num(x[i]) - num(y[i]) || String(x[i]).localeCompare(String(y[i])),
            render: (r) => <span className={i === 0 ? "mono" : "tnum"} style={{ color: i === 1 && num(r[i]) > 1 ? "var(--danger)" : undefined }}>{r[i]}</span>,
          }))}
        />
      </Card>
    </div>
  );
}

/** Right-hand artifact panel: tabs per artifact, view/edit, download, share, pin to canvas, pop-out, full screen. */
export function ArtifactPanel({ artifacts, active, onSelect, onClose }: { artifacts: Artifact[]; active: string; onSelect: (id: string) => void; onClose: () => void }) {
  const { tx } = useI18n();
  const [wide, setWide] = useState(false);
  const a = artifacts.find((x) => x.id === active) ?? artifacts[0];
  if (!a) return null;
  return (
    <aside className={cx("artifact-panel", wide && "wide")} aria-label={tx("产物面板", "Artifact panel")}>
      <div className="art-tabs" role="tablist">
        {artifacts.map((x) => (
          <button key={x.id} type="button" role="tab" aria-selected={x.id === a.id} className={x.id === a.id ? "on" : ""} onClick={() => onSelect(x.id)}>
            <Icon name={ARTIFACT_ICON[x.kind]} size={13} />
            <span className="ellipsis" style={{ maxWidth: 120 }}>{x.title}</span>
          </button>
        ))}
        <Spacer />
        <IconBtn icon="download" label={tx("下载", "Download")} size={26} onClick={() => toast(tx("已开始下载 {n}", "Downloading {n}", { n: a.title }))} />
        <IconBtn icon="send" label={tx("分享到飞书", "Share to Feishu")} size={26} onClick={() => toast(tx("已分享到飞书 #sre-oncall（记入审计）", "Shared to Feishu #sre-oncall (audited)"))} />
        <IconBtn icon="pin" label={tx("钉到 Canvas", "Pin to canvas")} size={26} onClick={() => toast(tx("已钉到 Canvas INV-231", "Pinned to canvas INV-231"))} />
        <IconBtn icon={wide ? "minimize" : "fit"} label={wide ? tx("退出全屏", "Exit full screen") : tx("全屏", "Full screen")} size={26} onClick={() => setWide((w) => !w)} />
        <IconBtn icon="x" label={tx("关闭产物面板", "Close panel")} size={26} onClick={onClose} />
      </div>
      <div className="art-body" key={a.id}>
        {a.content.kind === "report" && <ReportView a={a.content} meta={a} />}
        {a.content.kind === "chart" && <ChartCompare a={a.content} />}
        {a.content.kind === "table" && <CsvView a={a.content} title={a.title} />}
        {a.content.kind === "diff" && (
          <div className="v3-col anim-fade" style={{ gap: 12, padding: "20px 24px" }}>
            <DiffView {...a.content} />
            <Row gap={6}>
              <Btn kind="soft" icon="shield" to="/review/PRP-88">
                {tx("生成回滚提案", "Create rollback proposal")}
              </Btn>
              <Btn kind="ghost" icon="external" onClick={() => window.open("https://gitlab.example.internal", "_blank", "noopener")}>
                {tx("在 GitLab 中打开", "Open in GitLab")}
              </Btn>
            </Row>
          </div>
        )}
      </div>
    </aside>
  );
}
