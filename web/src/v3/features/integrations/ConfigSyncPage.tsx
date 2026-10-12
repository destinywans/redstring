import { useState } from "react";
import { Page } from "../../app/Shell";
import { API_MODE, ApiError, api, useGet } from "../../api/client";
import type { SyncItem } from "../../api/types";
import { Badge, Btn, Card, Empty, Skeleton } from "../../ui/core";
import { toast } from "../../ui/overlays";
import { confirmAction } from "../../ui/demo";
import { PageUnavailable } from "../../ui/unknown";
import { useI18n } from "../../i18n";
import { useEnv } from "../../app/environment";
import { PageHead } from "../conversation/ConversationsPage";
import { useConfigSyncError } from "./configSyncText";
import "./integrations.css";

/**
 * Config promotion goes through Git, never platform-to-platform. Non-production exports (runbooks, templates,
 * alert rules, agent versions) as merge requests; production lists merged changes and imports them after review.
 * Production reports a merged, verified change as `merged` (docs/api/v3-config-sync.md), so `merged` and the older
 * `pending` both offer the import.
 */
export default function ConfigSyncPage() {
  const { tx } = useI18n();
  const prod = useEnv((s) => s.instance?.tier === "production");
  const { data, refetch, error } = useGet<{ direction: "import" | "export"; items: SyncItem[] }>("/config-sync");
  const [busy, setBusy] = useState<string | null>(null);
  const [done, setDone] = useState<Record<string, SyncItem>>({});
  const errorText = useConfigSyncError();
  const importable = (it: SyncItem) => prod && (it.status === "merged" || it.status === "pending");
  const IMPORT_EFFECT: Record<SyncItem["kind"], string> = {
    runbook: tx("导入为生产应用的待审核草稿，正文为空。", "It is imported as a production draft to review, with an empty body."),
    alert_rule: tx("导入为生产配置记录，不会应用到 Prometheus。", "It is imported as a production configuration record and isn't applied to Prometheus."),
    agent_version: tx("导入为目录版本；新 Agent 默认暂停、L0，已有 Agent 不改变发布状态。", "It is imported as a catalog version; a new Agent starts paused at L0, an existing one keeps its rollout."),
    template: tx("导入为生产画布模板。", "It is imported as a production canvas template."),
    // PA-100: a just-merged MR GitLab is still preparing; nothing was read from it yet.
    unknown: tx("合并请求还在核验，暂不能导入。", "The merge request is still being verified and can't be imported yet."),
  };
  const importIt = async (it: SyncItem) => {
    const ok = await confirmAction({
      title: tx("导入 {t}？", "Import {t}?", { t: it.title }),
      desc: `${IMPORT_EFFECT[it.kind]}${tx("导入前服务端会再核对合并请求、签名和审批；导入写入审计。", " Before importing, the server checks the merge request, signature and approvals again; the import is audited.")}`,
      confirm: tx("导入", "Import"),
    });
    if (!ok) return;
    setBusy(it.id);
    try {
      const r = await api<SyncItem>("POST", `/config-sync/${encodeURIComponent(it.id)}/import`);
      setDone((d) => ({ ...d, [it.id]: r }));
      toast(it.kind === "agent_version" ? tx("已导入，Agent 发布状态不变", "Imported; the Agent's rollout is unchanged") : tx("已导入并写入审计", "Imported and audited"));
      void refetch();
    } catch (e) {
      if (e instanceof ApiError && e.code === "promotion_conflict") void refetch();
      toast(errorText(e, "import"), { tone: "danger" });
    } finally {
      setBusy(null);
    }
  };
  const statusBadge = (s: SyncItem["status"]) =>
    ({
      pending: <Badge tone="warn">{tx("待审核", "To review")}</Badge>,
      imported: <Badge tone="ok">{tx("已导入", "Imported")}</Badge>,
      exported: <Badge tone="info">{tx("已导出 · 合并请求中", "Exported · in review")}</Badge>,
      merged: prod ? <Badge tone="warn">{tx("已合并 · 待导入", "Merged · to import")}</Badge> : <Badge tone="ok">{tx("已合并", "Merged")}</Badge>,
      // PA-100: GitLab is still preparing a just-merged MR; nothing was read from it and it cannot be imported yet.
      verifying: <Badge tone="info">{tx("核验中", "Verifying")}</Badge>,
      // PA-103: this one candidate could not be verified (GitLab unavailable or a mismatch); the rest of the list stands.
      unverifiable: <Badge tone="warn">{tx("无法核验", "Unverifiable")}</Badge>,
    })[s];
  // Production needs a platform administrator with global workspace.manage to read the list (D44 wording elsewhere).
  const forbidden = error instanceof ApiError && error.status === 403 && !data;
  return (
    <Page error={!data && !forbidden ? error : undefined} errorSource={tx("配置仓库", "configuration repository")} crumbs={[tx("集成中心", "Integrations"), tx("配置同步", "Config sync")]}>
      {forbidden ? (
        <PageUnavailable
          icon="lock"
          configure={false}
          title={prod ? tx("没有权限查看待导入的配置", "You can't view the configuration to import") : tx("没有权限查看导出的配置", "You can't view the exported configuration")}
          desc={prod ? tx("查看和导入已合并的配置需要平台管理员身份和全局 workspace.manage 权限。", "Viewing and importing merged configuration needs a platform administrator with global workspace.manage.") : tx("需要管理员在当前环境授予导出权限。", "An admin needs to grant export access in this environment.")}
        />
      ) : (
        <div className="v3-content" style={{ maxWidth: 980 }}>
          <PageHead
            title={prod ? tx("配置同步 · 待导入", "Config sync · to import") : tx("配置同步 · 已导出", "Config sync · exported")}
            desc={
              prod
                ? tx("来自配置仓库 main 分支的变更。低环境验证后以合并请求提交，这里审核差异后再导入；导入写入审计。", "Changes from the config repository's main branch, submitted as merge requests after lower-environment validation. Review the diff here before importing; imports are audited.")
                : tx("在这里导出的 Runbook、模板、告警规则和 Agent 版本会以合并请求提交到配置仓库，由生产负责人审核。平台之间不直接连接。", "Runbooks, templates, alert rules and agent versions exported here go to the config repo as merge requests for production owners to review. The platforms never connect directly.")
            }
          />
          <Card style={{ overflow: "hidden" }}>
            {!data && <Skeleton h={160} />}
            {data && data.items.length === 0 && (
              <Empty
                icon="inbox"
                title={prod ? tx("还没有待导入的配置", "Nothing to import yet") : tx("还没有导出的配置", "Nothing exported yet")}
                desc={prod ? tx("低环境导出、在配置仓库合并后，会出现在这里等你审核导入。", "Once something is exported from a lower environment and merged in the config repository, it shows up here for you to review and import.") : tx("在 Runbook 等页面用“提升到生产”导出后，会出现在这里。", "Items you export with “Promote to production” (e.g. from a Runbook) show up here.")}
              />
            )}
            {data?.items.map((raw) => {
              const it = done[raw.id] ?? raw;
              // Verifying / unverifiable rows say why on their second line (PA-100, PA-103); they never offer the import.
              const why =
                it.status === "unverifiable"
                  ? it.reason === "untrusted"
                    ? tx("合并请求核验未通过", "The merge request didn't pass verification")
                    : tx("GitLab 暂时不可用，刷新重试", "GitLab is unavailable right now; refresh to retry")
                  : it.status === "verifying"
                    ? tx("GitLab 还在处理刚合并的请求，稍后刷新", "GitLab is still processing the just-merged request; refresh shortly")
                    : null;
              const meta = [it.mr, why, it.author, it.meta].filter(Boolean).join(" · ");
              return (
                <div key={it.id} className="cs-row">
                  <Badge tone="accent">{it.kind_label}</Badge>
                  <div className="cs-main">
                    <b className="ellipsis" title={it.title} style={{ fontSize: 13.5, fontWeight: 600 }}>
                      {it.title}
                    </b>
                    {/* Long source versions (sha256:…) are cut; the full line is in the title. */}
                    <span className="muted ellipsis" title={meta} style={{ fontSize: 12 }}>
                      {meta}
                    </span>
                  </div>
                  <span className="cs-status">{statusBadge(it.status)}</span>
                  <span className="cs-actions">
                    {API_MODE === "mock" && <Btn h={26} onClick={() => toast(tx("在 GitLab 中打开 {m} 的差异", "Opening the {m} diff in GitLab", { m: it.mr }))}>{tx("查看差异", "View diff")}</Btn>}
                    {importable(it) && (
                      <Btn h={26} kind="primary" loading={busy === it.id} onClick={() => void importIt(it)}>
                        {tx("导入", "Import")}
                      </Btn>
                    )}
                  </span>
                </div>
              );
            })}
          </Card>
        </div>
      )}
    </Page>
  );
}
