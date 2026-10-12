import { useState } from "react";
import { Page } from "../../app/Shell";
import { API_MODE, api, useGet } from "../../api/client";
import type { SyncItem } from "../../api/types";
import { Badge, Btn, Card, Col, Muted, Row, Skeleton, Spacer, Strong } from "../../ui/core";
import { toast } from "../../ui/overlays";
import { useI18n } from "../../i18n";
import { useEnv } from "../../app/environment";
import { PageHead } from "../conversation/ConversationsPage";

/**
 * Config promotion goes through Git, never platform-to-platform. Non-production exports (runbooks, templates,
 * alert rules, agent versions) as merge requests; production lists merged changes and imports them after review.
 */
export default function ConfigSyncPage() {
  const { tx } = useI18n();
  const prod = useEnv((s) => s.instance?.tier === "production");
  const { data, refetch, error } = useGet<{ direction: "import" | "export"; items: SyncItem[] }>("/config-sync");
  const [busy, setBusy] = useState<string | null>(null);
  const importIt = async (it: SyncItem) => {
    setBusy(it.id);
    try {
      await api("POST", `/config-sync/${it.id}/import`);
      toast(it.kind === "agent_version" ? tx("已导入，先按灰度发布", "Imported; rolled out as a canary first") : tx("已导入并写入审计", "Imported and audited"));
      void refetch();
    } finally {
      setBusy(null);
    }
  };
  const statusBadge = (s: SyncItem["status"]) =>
    ({ pending: <Badge tone="warn">{tx("待审核", "To review")}</Badge>, imported: <Badge tone="ok">{tx("已导入", "Imported")}</Badge>, exported: <Badge tone="info">{tx("已导出 · 合并请求中", "Exported · in review")}</Badge>, merged: <Badge tone="ok">{tx("已合并", "Merged")}</Badge>, verifying: <Badge tone="info">{tx("核验中", "Verifying")}</Badge>, unverifiable: <Badge tone="warn">{tx("无法核验", "Unverifiable")}</Badge> })[s];
  return (
    <Page error={!data ? error : undefined} errorSource={tx("配置仓库", "configuration repository")} crumbs={[tx("集成中心", "Integrations"), tx("配置同步", "Config sync")]}>
      <div className="v3-content" style={{ maxWidth: 980 }}>
        <PageHead
          title={prod ? tx("配置同步 · 待导入", "Config sync · to import") : tx("配置同步 · 已导出", "Config sync · exported")}
          desc={
            prod
              ? tx("来自配置仓库（ops-config · main）的变更。低环境验证后以合并请求提交，这里审核差异后再导入；导入写入审计。", "Changes from the config repo (ops-config · main), submitted as merge requests after lower-environment validation. Review the diff here before importing; imports are audited.")
              : tx("在这里导出的 Runbook、模板、告警规则和 Agent 版本会以合并请求提交到配置仓库，由生产负责人审核。平台之间不直接连接。", "Runbooks, templates, alert rules and agent versions exported here go to the config repo as merge requests for production owners to review. The platforms never connect directly.")
          }
        />
        <Card style={{ overflow: "hidden" }}>
          {!data && <Skeleton h={160} />}
          {data?.items.map((it) => (
            <Row key={it.id} gap={12} style={{ padding: "12px 16px", borderBottom: "1px solid var(--border)" }}>
              <Badge tone="accent">{it.kind_label}</Badge>
              <Col gap={0} grow>
                <Strong size={13.5}>{it.title}</Strong>
                <Muted size={12}>
                  {it.mr} · {it.author} · {it.meta}
                </Muted>
              </Col>
              {statusBadge(it.status)}
              <Spacer />
              {API_MODE === "mock" && <Btn h={26} onClick={() => toast(tx("在 GitLab 中打开 {m} 的差异", "Opening the {m} diff in GitLab", { m: it.mr }))}>{tx("查看差异", "View diff")}</Btn>}
              {prod && it.status === "pending" && (
                <Btn h={26} kind="primary" loading={busy === it.id} onClick={() => importIt(it)}>
                  {tx("导入", "Import")}
                </Btn>
              )}
            </Row>
          ))}
        </Card>
      </div>
    </Page>
  );
}
