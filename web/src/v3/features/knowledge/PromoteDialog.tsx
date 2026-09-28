import { useState } from "react";
import { api } from "../../api/client";
import { Btn, Muted, Spacer } from "../../ui/core";
import { Dialog, toast } from "../../ui/overlays";
import { useI18n } from "../../i18n";
import { useEnv } from "../../app/environment";

/** Non-production only: promote a validated item to production via a merge request on the config repo. */
export function PromoteButton({ kind, id, title }: { kind: "runbooks" | "templates" | "alert-rules" | "agents"; id: string; title: string }) {
  const { tx } = useI18n();
  const nonprod = useEnv((s) => s.instance?.tier === "nonprod");
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  if (!nonprod) return null;
  const path = `ops-config/${kind}/${id}.yaml`;
  const go = async () => {
    setBusy(true);
    try {
      const r = await api<{ mr: string; path: string }>("POST", "/promotions", { body: { kind, id } });
      toast(tx("已导出并发起合并请求 {m}，等待生产负责人审核", "Exported; merge request {m} awaits production review", { m: r.mr }));
      setOpen(false);
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
      <Btn icon="upload" onClick={() => setOpen(true)}>
        {tx("提升到生产", "Promote to production")}
      </Btn>
      <Dialog
        open={open}
        onClose={() => setOpen(false)}
        title={tx("提升到生产：{t}", "Promote to production: {t}", { t: title })}
        footer={
          <>
            <Muted size={12}>{tx("不会直接写入生产平台", "Nothing is written to the production platform directly")}</Muted>
            <Spacer />
            <Btn kind="ghost" onClick={() => setOpen(false)}>{tx("取消", "Cancel")}</Btn>
            <Btn kind="primary" loading={busy} onClick={go}>{tx("导出并发起合并请求", "Export and open merge request")}</Btn>
          </>
        }
      >
        <div className="v3-col" style={{ gap: 12 }}>
          {step(1, <>{tx("导出到配置仓库", "Export to the config repo")} <code>{path}</code></>)}
          {step(2, tx("发起合并请求，由生产负责人审核", "Open a merge request for production owners to review"))}
          {step(3, tx("合并后，生产平台在「集成中心 › 配置同步」里看到这项变更，审核通过才生效", "Once merged, production sees it under Integrations › Config sync and applies it only after review"))}
          <Muted size={12}>{tx("低环境里的执行记录和证据不会带过去。", "Runs and evidence from lower environments are not carried over.")}</Muted>
        </div>
      </Dialog>
    </>
  );
}
