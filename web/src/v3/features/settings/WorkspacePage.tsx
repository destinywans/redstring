import { useEffect, useState } from "react";
import { Page } from "../../app/Shell";
import { ApiError, api, useGetWithEtag } from "../../api/client";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Badge, Btn, Chip, Field, Input, Muted, Panel, Row, Segmented, Skeleton, Strong, Icon } from "../../ui/core";
import { BrandMark } from "../../ui/BrandMark";
import { toast } from "../../ui/overlays";
import { useI18n } from "../../i18n";
import { SettingsHead } from "./common";
import { API_MODE } from "../../api/client";
import { useEnv } from "../../app/environment";
import "./settings.css";
import { ChipSelect } from "../../ui/demo";

type Workspace = { name: string; slug: string; timezone: string; locale: string; week_start: "monday" | "sunday"; retention_days: number; data_region: string; brand: { name: string; logo: string | null; pending: boolean } };

/** Workspace › General: name, defaults for new members, retention. Product name and logo are still undecided. */
export default function WorkspacePage() {
  const { tx } = useI18n();
  // Saved against the version it was read at: If-Match with the GET's strong ETag (PA-158).
  const { data: read } = useGetWithEtag<Workspace>("/workspace");
  const data = read?.data;
  const [w, setW] = useState<Workspace | null>(null);
  const client = useQueryClient();
  const save = useMutation<unknown, ApiError, Workspace>({
    mutationFn: (body) => api("PUT", "/workspace", { body, headers: read?.etag ? { "If-Match": read.etag } : undefined }),
    onSettled: () => client.invalidateQueries({ predicate: (q) => q.queryKey[0] === "/workspace" }),
  });
  useEffect(() => {
    if (data) setW(structuredClone(data));
  }, [data]);
  const dirty = !!data && !!w && JSON.stringify(data) !== JSON.stringify(w);
  const up = (patch: Partial<Workspace>) => setW((x) => (x ? { ...x, ...patch } : x));
  const tier = useEnv((s) => s.instance?.tier);
  const domain = useEnv((s) => s.instance?.domain ?? "—");
  return (
    <Page
      ai={false}
      bodyClass="set-narrow"
      crumbs={[tx("设置", "Settings"), tx("工作区通用", "Workspace")]}
      actions={
        <Btn kind="primary" icon="check" disabled={!dirty} loading={save.isPending} onClick={() => w && save.mutate(w, { onSuccess: () => toast(tx("工作区设置已保存并写入审计", "Workspace settings saved and audited")), onError: (e) => toast(e.status === 409 || e.status === 412 ? tx("工作区设置刚被其他人修改，已重新读取，请核对后再保存", "Someone just changed the workspace; reloaded — check and save again") : e.message, { tone: "danger" }) })}>
          {tx("保存", "Save")}
        </Btn>
      }
    >
      <div className="v3-content anim-fade">
        <SettingsHead title={tx("工作区通用", "Workspace")} desc={tx("对工作区所有成员生效，只有管理员可以修改。", "Applies to everyone in the workspace; only admins can change it.")} />
        {!w ? (
          <Skeleton h={320} />
        ) : (
          <>
            <Panel title={tx("基本信息", "Basics")}>
              <div className="form-grid">
                <Field label={tx("工作区名称", "Workspace name")}>
                  <Input value={w.name} onChange={(e) => up({ name: e.target.value })} />
                </Field>
                <Field label={tx("标识", "URL slug")} hint={tx("用于链接和 API，修改后旧链接会失效", "Used in links and the API; old links stop working if changed")}>
                  <Input mono value={w.slug} onChange={(e) => up({ slug: e.target.value.toLowerCase().replace(/[^a-z0-9-]/g, "") })} />
                </Field>
              </div>
            </Panel>
            <Panel title={tx("品牌", "Branding")} right={w.brand.pending && <Badge tone="warn">{tx("名称与 Logo 待定", "Name and logo pending")}</Badge>}>
              <Row gap={12}>
                <span className="logo-slot">{w.brand.logo ? <img src={w.brand.logo} alt="" /> : <BrandMark size={28} label={w.brand.name} />}</span>
                <span className="v3-col grow" style={{ gap: 0 }}>
                  <Strong>{w.brand.name}</Strong>
                  <Muted size={11.5}>{tx("当前的产品名称和 Logo，暂不支持按工作区自定义。", "The current product name and logo; per-workspace branding isn't supported yet.")}</Muted>
                </span>
                <Btn h={28} icon="upload" disabled title={tx("暂不支持自定义 Logo", "Custom logos aren't supported yet")}>{tx("上传 Logo", "Upload logo")}</Btn>
              </Row>
            </Panel>
            <Panel title={tx("新成员默认值", "Defaults for new members")}>
              <div className="form-grid">
                <Field label={tx("时区", "Time zone")}>
                  <ChipSelect label="" showLabel={false} value={w.timezone} onChange={(v) => up({ timezone: v as string })} options={[...new Set([w.timezone, "Asia/Shanghai (UTC+8)", "Asia/Singapore (UTC+8)", "Europe/London (UTC+0)"])]} />
                </Field>
                <Field label={tx("语言", "Language")}>
                  <Segmented label={tx("语言", "Language")} value={w.locale} onChange={(v) => up({ locale: v })} options={[{ value: "zh-CN", label: "中文" }, { value: "en-US", label: "English" }]} />
                </Field>
                <Field label={tx("每周开始于", "Week starts on")}>
                  <Segmented label={tx("每周开始于", "Week starts on")} value={w.week_start} onChange={(v) => up({ week_start: v })} options={[{ value: "monday", label: tx("周一", "Monday") }, { value: "sunday", label: tx("周日", "Sunday") }]} />
                </Field>
              </div>
            </Panel>
            <Panel title={tx("实例", "Instance")}>
              <div className="form-grid">
                <Field label={tx("实例类型", "Instance type")} hint={tx("生产单独部署一套；开发、测试、预发和需求验证环境共用一套。部署时确定，界面上不能切换。", "Production runs on its own; development, test, staging and feature environments share one. Set at deploy time, not switchable in the UI.")}>
                  <Badge tone={tier === "production" ? "prod" : "neutral"}>
                    <Icon name="lock" size={11} />
                    {tier === "production" ? tx("生产实例", "Production instance") : tx("非生产实例", "Non-production instance")}
                  </Badge>
                </Field>
                <Field label={tx("域名", "Domain")}>
                  <Badge mono>{domain}</Badge>
                </Field>
              </div>
              {API_MODE === "mock" && (
                <Row gap={10} style={{ marginTop: 12 }}>
                  <Muted size={12}>{tx("演示：以哪种实例形态显示", "Demo: show the UI as")}</Muted>
                  <Segmented
                    label={tx("演示实例形态", "Demo instance")}
                    value={tier ?? "production"}
                    onChange={(v) => {
                      try {
                        localStorage.setItem("aiops-v3-demo-instance", v);
                        localStorage.removeItem("aiops-v3-env");
                      } catch {
                        /* demo only */
                      }
                      location.reload();
                    }}
                    options={[{ value: "production", label: tx("生产实例", "Production") }, { value: "nonprod", label: tx("非生产实例", "Non-production") }]}
                  />
                </Row>
              )}
            </Panel>
            <Panel title={tx("数据", "Data")}>
              <div className="form-grid">
                <Field label={tx("对话与画布保留", "Keep conversations and canvases")} hint={tx("审计日志的保留期在「安全策略」中单独设置", "Audit log retention is set separately in Security policy")}>
                  <Input width={140} value={String(w.retention_days)} onChange={(e) => up({ retention_days: Number(e.target.value.replace(/\D/g, "")) || 0 })} right={<Muted>{tx("天", "days")}</Muted>} />
                </Field>
                <Field label={tx("数据位置", "Data location")}>
                  <Badge>
                    <Icon name="lock" size={11} />
                    {w.data_region}
                  </Badge>
                </Field>
              </div>
            </Panel>
          </>
        )}
      </div>
    </Page>
  );
}
