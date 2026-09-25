import { useEffect, useState } from "react";
import { Page } from "../../app/Shell";
import { useGet, useSend } from "../../api/client";
import { Badge, Btn, Chip, Field, Input, Muted, Panel, Row, Segmented, Skeleton, Strong } from "../../ui/core";
import { toast } from "../../ui/overlays";
import { useI18n } from "../../i18n";
import { SettingsHead } from "./common";
import "./settings.css";

type Workspace = { name: string; slug: string; timezone: string; locale: string; week_start: "monday" | "sunday"; retention_days: number; data_region: string; brand: { name: string; logo: string | null; pending: boolean } };

/** Workspace › General: name, defaults for new members, retention. Product name and logo are still undecided. */
export default function WorkspacePage() {
  const { tx } = useI18n();
  const { data } = useGet<Workspace>("/workspace");
  const [w, setW] = useState<Workspace | null>(null);
  const save = useSend<Workspace>("PUT", "/workspace", ["/workspace"]);
  useEffect(() => {
    if (data) setW(structuredClone(data));
  }, [data]);
  const dirty = !!data && !!w && JSON.stringify(data) !== JSON.stringify(w);
  const up = (patch: Partial<Workspace>) => setW((x) => (x ? { ...x, ...patch } : x));
  return (
    <Page
      ai={false}
      bodyClass="set-narrow"
      crumbs={[tx("设置", "Settings"), tx("工作区通用", "Workspace")]}
      actions={
        <Btn kind="primary" icon="check" disabled={!dirty} loading={save.isPending} onClick={() => w && save.mutate(w, { onSuccess: () => toast(tx("工作区设置已保存并写入审计", "Workspace settings saved and audited")) })}>
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
                <span className="logo-slot">A</span>
                <span className="v3-col grow" style={{ gap: 0 }}>
                  <Strong>{w.brand.name}</Strong>
                  <Muted size={11.5}>{tx("产品名称和 Logo 确定后在这里替换，侧栏和浏览器标签同步更新。", "Replace the product name and logo here once decided; the sidebar and browser tab follow.")}</Muted>
                </span>
                <Btn h={28} icon="upload" disabled>{tx("上传 Logo", "Upload logo")}</Btn>
              </Row>
            </Panel>
            <Panel title={tx("新成员默认值", "Defaults for new members")}>
              <div className="form-grid">
                <Field label={tx("时区", "Time zone")}>
                  <Chip caret>{w.timezone}</Chip>
                </Field>
                <Field label={tx("语言", "Language")}>
                  <Segmented label={tx("语言", "Language")} value={w.locale} onChange={(v) => up({ locale: v })} options={[{ value: "zh-CN", label: "中文" }, { value: "en-US", label: "English" }]} />
                </Field>
                <Field label={tx("每周开始于", "Week starts on")}>
                  <Segmented label={tx("每周开始于", "Week starts on")} value={w.week_start} onChange={(v) => up({ week_start: v })} options={[{ value: "monday", label: tx("周一", "Monday") }, { value: "sunday", label: tx("周日", "Sunday") }]} />
                </Field>
              </div>
            </Panel>
            <Panel title={tx("数据", "Data")}>
              <div className="form-grid">
                <Field label={tx("对话与画布保留", "Keep conversations and canvases")} hint={tx("审计日志的保留期在「安全策略」中单独设置", "Audit log retention is set separately in Security policy")}>
                  <Input width={140} value={String(w.retention_days)} onChange={(e) => up({ retention_days: Number(e.target.value.replace(/\D/g, "")) || 0 })} right={<Muted>{tx("天", "days")}</Muted>} />
                </Field>
                <Field label={tx("数据位置", "Data location")}>
                  <Chip icon="lock">{w.data_region}</Chip>
                </Field>
              </div>
            </Panel>
          </>
        )}
      </div>
    </Page>
  );
}
