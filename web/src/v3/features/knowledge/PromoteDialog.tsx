import { useRef, useState } from "react";
import { ApiError, api, newIdempotencyKey } from "../../api/client";
import { Btn, Muted, Spacer } from "../../ui/core";
import { Dialog, toast } from "../../ui/overlays";
import { Callout } from "../../ui/forms";
import { useI18n } from "../../i18n";
import { useEnv } from "../../app/environment";
import { useConfigSyncError } from "../integrations/configSyncText";

/**
 * Non-production only: promote a validated item to production via a merge request on the config repo
 * (POST /promotions → { mr, path }). Pre-check refusals (422 with a reason code) and an unknown GitLab result (503;
 * the retry reuses the same merge request) are said in the dialog, which stays open to retry; success shows the merge
 * request and file path.
 */
export function PromoteButton({ kind, id, title }: { kind: "runbooks" | "templates" | "alert-rules" | "agents"; id: string; title: string }) {
  const { tx } = useI18n();
  const nonprod = useEnv((s) => s.instance?.tier === "nonprod");
  const errorText = useConfigSyncError();
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<{ text: string; retry: boolean } | null>(null);
  const [result, setResult] = useState<{ mr: string; path: string } | null>(null);
  // One key per opened dialog: a retry after an unknown result is the same request.
  const key = useRef("");
  if (!nonprod) return null;
  const openDialog = () => {
    setErr(null);
    setResult(null);
    key.current = newIdempotencyKey();
    setOpen(true);
  };
  const go = async () => {
    setBusy(true);
    setErr(null);
    try {
      const r = await api<{ mr: string; path: string }>("POST", "/promotions", { body: { kind, id }, idempotencyKey: key.current });
      setResult(r);
      toast(tx("已导出并发起合并请求 {m}，等待生产负责人审核", "Exported; merge request {m} awaits production review", { m: r.mr }));
    } catch (e) {
      // 503 (unknown result) and 409 promotion_in_progress can be retried; a 422 refusal needs the item changed first.
      const retry = e instanceof ApiError && (e.status === 503 || e.code === "promotion_in_progress");
      setErr({ text: errorText(e, "export"), retry });
    } finally {
      setBusy(false);
    }
  };
  const step = (n: number, text: React.ReactNode) => (
    <div className="v3-row" style={{ gap: 10, alignItems: "flex-start", fontSize: "var(--fs-sm)" }}>
      <span className="f-num" style={{ marginTop: 1 }}>{n}</span>
      <span>{text}</span>
    </div>
  );
  return (
    <>
      <Btn icon="upload" onClick={openDialog}>
        {tx("提升到生产", "Promote to production")}
      </Btn>
      <Dialog
        open={open}
        onClose={() => setOpen(false)}
        title={tx("提升到生产：{t}", "Promote to production: {t}", { t: title })}
        footer={
          result ? (
            <>
              <Spacer />
              <Btn kind="primary" onClick={() => setOpen(false)}>{tx("完成", "Done")}</Btn>
            </>
          ) : (
            <>
              <Muted size={12}>{tx("不会直接写入生产平台", "Nothing is written to the production platform directly")}</Muted>
              <Spacer />
              <Btn kind="ghost" onClick={() => setOpen(false)}>{tx("取消", "Cancel")}</Btn>
              <Btn kind="primary" loading={busy} onClick={go}>
                {err?.retry ? tx("重试", "Retry") : tx("导出并发起合并请求", "Export and open merge request")}
              </Btn>
            </>
          )
        }
      >
        <div className="v3-col" style={{ gap: 12 }}>
          {step(1, result ? <>{tx("已导出到配置仓库", "Exported to the config repo")} <code>{result.path}</code></> : tx("导出到配置仓库", "Export to the config repo"))}
          {step(2, result ? tx("已发起合并请求 {m}，由生产负责人审核", "Merge request {m} is open for production owners to review", { m: result.mr }) : tx("发起合并请求，由生产负责人审核", "Open a merge request for production owners to review"))}
          {step(3, tx("合并后，生产平台在「集成中心 › 配置同步」里看到这项变更，审核通过才生效", "Once merged, production sees it under Integrations › Config sync and applies it only after review"))}
          <Muted size={12}>{tx("低环境里的执行记录和证据不会带过去。", "Runs and evidence from lower environments are not carried over.")}</Muted>
          {err && (
            <Callout tone="warn" icon="alert">
              {err.text}
            </Callout>
          )}
          {result && (
            <Callout tone="ok" icon="check">
              {tx("已导出并发起合并请求 {m}。", "Exported; merge request {m} is open.", { m: result.mr })}
            </Callout>
          )}
        </div>
      </Dialog>
    </>
  );
}
