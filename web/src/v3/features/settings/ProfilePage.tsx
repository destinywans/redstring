import { useState } from "react";
import { Page } from "../../app/Shell";
import { usePreferences } from "../../app/preferences";
import { useGet, useSend } from "../../api/client";
import type { NotifyChannel } from "../../api/types";
import { Avatar, Badge, Brand, Btn, Chip, Col, Field, Input, Muted, Panel, Row, Section, Segmented, Skeleton, Spacer, State, Strong, TextLink, Tile, Toggle, type IconName } from "../../ui/core";
import { toast } from "../../ui/overlays";
import { useI18n } from "../../i18n";
import { SettingsHead, useProfileForm } from "./common";
import "./settings.css";

const CHANNELS: NotifyChannel[] = ["feishu", "email", "sms", "phone"];

function SaveBtn({ dirty, pending, onSave }: { dirty: boolean; pending: boolean; onSave: () => void }) {
  const { tx } = useI18n();
  return (
    <Btn kind="primary" icon="check" disabled={!dirty} loading={pending} onClick={onSave}>
      {tx("保存", "Save")}
    </Btn>
  );
}

export default function ProfilePage() {
  const { tx } = useI18n();
  const locale = usePreferences((s) => s.locale);
  const { form, setForm, save, dirty } = useProfileForm();
  const set = (k: "name" | "display_name" | "title", v: string) => setForm((f) => (f ? { ...f, [k]: v } : f));
  return (
    <Page ai={false} bodyClass="set-narrow" crumbs={[tx("设置", "Settings"), tx("个人资料", "Profile")]} actions={<SaveBtn dirty={dirty} pending={save.isPending} onSave={() => form && save.mutate(form, { onSuccess: () => toast(tx("个人资料已保存", "Profile saved")) })} />}>
      <div className="v3-content anim-fade">
        <SettingsHead title={tx("个人资料", "Profile")} desc={tx("你在工作区里的名字、头像和联系方式。", "Your name, photo and contact details in this workspace.")} />
        {!form ? (
          <Skeleton h={400} />
        ) : (
          <>
            <Panel title={tx("基本信息", "Basics")}>
              <Col gap={16}>
                <Row gap={16}>
                  <Avatar name={form.name} size={64} accent />
                  <Col gap={6}>
                    <Row gap={6}>
                      <Btn h={28} icon="upload">{tx("上传新头像", "Upload photo")}</Btn>
                      <Btn h={28} kind="ghost">{tx("移除", "Remove")}</Btn>
                    </Row>
                    <Muted size={11.5}>{tx("PNG 或 JPG，至少 256 × 256，不超过 2 MB", "PNG or JPG, at least 256 × 256, up to 2 MB")}</Muted>
                  </Col>
                </Row>
                <div className="form-grid">
                  <Field label={tx("姓名", "Name")}>
                    <Input value={form.name} onChange={(e) => set("name", e.target.value)} />
                  </Field>
                  <Field label={tx("显示名", "Display name")}>
                    <Input value={form.display_name} onChange={(e) => set("display_name", e.target.value)} />
                  </Field>
                  <Field label={tx("部门", "Department")}>
                    <Chip caret>{form.department}</Chip>
                  </Field>
                  <Field label={tx("职位", "Title")}>
                    <Input value={form.title} onChange={(e) => set("title", e.target.value)} />
                  </Field>
                  <Field label={tx("语言", "Language")} hint={<TextLink to="/settings/preferences">{tx("在偏好设置中切换", "Change in Preferences")}</TextLink>}>
                    <Chip icon="globe">{locale === "zh-CN" ? "简体中文" : "English"}</Chip>
                  </Field>
                  <Field label={tx("时区", "Time zone")}>
                    <Chip caret>{form.timezone}</Chip>
                  </Field>
                </div>
              </Col>
            </Panel>
            <Panel title={tx("联系方式", "Contacts")}>
              <ul className="set-list">
                {form.contacts.map((c) => (
                  <li key={c.kind}>
                    {c.kind === "feishu" ? <Brand name="feishu" size={30} /> : <Tile icon={(c.kind === "email" ? "message" : "phone") as IconName} size={30} />}
                    <Col gap={0} grow>
                      <Muted>{c.label}</Muted>
                      <span style={{ fontSize: "var(--fs-body)", fontWeight: 500 }}>{c.value}</span>
                    </Col>
                    {c.note ? <Badge tone="accent">{c.note}</Badge> : c.verified && <State tone="ok">{tx("已确认", "Verified")}</State>}
                    <Btn h={28}>{c.kind === "feishu" ? tx("解绑", "Unlink") : tx("修改", "Change")}</Btn>
                  </li>
                ))}
              </ul>
            </Panel>
          </>
        )}
      </div>
    </Page>
  );
}

export function NotificationsPage() {
  const { tx } = useI18n();
  const { form, setForm, save, dirty } = useProfileForm();
  const { data: extra } = useGet<{ quiet: { enabled: boolean; from: string; to: string }; digest: "off" | "daily" | "weekly"; subscriptions: { id: string; label: string; sub: string; on: boolean }[] }>("/me/notification-settings");
  const [quiet, setQuiet] = useState<boolean | null>(null);
  const [digest, setDigest] = useState<"off" | "daily" | "weekly" | null>(null);
  const [subs, setSubs] = useState<Record<string, boolean>>({});
  const saveExtra = useSend("PUT", "/me/notification-settings");
  const chName = (c: NotifyChannel) => ({ feishu: tx("飞书", "Feishu"), email: tx("邮件", "Email"), sms: tx("短信", "SMS"), phone: tx("电话", "Phone") })[c];
  const extraDirty = quiet !== null || digest !== null || Object.keys(subs).length > 0;
  const onSave = () => {
    if (form && dirty) save.mutate(form);
    if (extraDirty) saveExtra.mutate({ quiet: quiet ?? extra?.quiet.enabled, digest: digest ?? extra?.digest, subscriptions: subs });
    setQuiet(null);
    setDigest(null);
    setSubs({});
    toast(tx("通知设置已保存", "Notification settings saved"));
  };
  return (
    <Page ai={false} bodyClass="set-narrow" crumbs={[tx("设置", "Settings"), tx("通知", "Notifications")]} actions={<SaveBtn dirty={dirty || extraDirty} pending={save.isPending} onSave={onSave} />}>
      <div className="v3-content anim-fade">
        <SettingsHead title={tx("通知", "Notifications")} desc={tx("决定哪些事情、通过哪个渠道找到你。值班期间以值班规则为准。", "Choose what reaches you and through which channel. On-call rules take over while you're on call.")} />
        {!form ? (
          <Skeleton h={300} />
        ) : (
          <Panel title={tx("按事件选择渠道", "Channels by event")} right={<Muted size={11.5}>{tx("值班期间会覆盖为值班规则", "On-call rules override these while on call")}</Muted>}>
            <table className="v3-table notif">
              <thead>
                <tr>
                  <th style={{ width: "36%" }}>{tx("事件", "Event")}</th>
                  {CHANNELS.map((c) => (
                    <th key={c} style={{ textAlign: "center" }}>{chName(c)}</th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {form.notifications.map((n, i) => (
                  <tr key={n.event} style={{ height: 34 }}>
                    <td>{n.label}</td>
                    {CHANNELS.map((c) => (
                      <td key={c} style={{ textAlign: "center" }}>
                        <input
                          type="checkbox"
                          aria-label={`${n.label} · ${chName(c)}`}
                          checked={n.channels[c]}
                          onChange={(e) =>
                            setForm((f) => {
                              if (!f) return f;
                              const notifications = f.notifications.map((x, j) => (j === i ? { ...x, channels: { ...x.channels, [c]: e.target.checked } } : x));
                              return { ...f, notifications };
                            })
                          }
                        />
                      </td>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
          </Panel>
        )}
        <Panel title={tx("免打扰", "Do not disturb")}>
          <div className="v3-col" style={{ gap: 12 }}>
            <Row gap={10}>
              <Toggle on={quiet ?? extra?.quiet.enabled ?? false} onChange={setQuiet} label={tx("夜间免打扰", "Quiet hours")} />
              <span className="v3-col grow" style={{ gap: 0 }}>
                <Strong>{tx("夜间免打扰 {f}–{t}", "Quiet hours {f}–{t}", { f: extra?.quiet.from ?? "22:00", t: extra?.quiet.to ?? "08:00" })}</Strong>
                <Muted size={11.5}>{tx("只保留 P1 电话与值班升级；其余次日汇总", "Only P1 calls and on-call escalations; the rest go to the next-day digest")}</Muted>
              </span>
            </Row>
            <Row gap={10}>
              <span className="v3-col grow" style={{ gap: 0 }}>
                <Strong>{tx("汇总", "Digest")}</Strong>
                <Muted size={11.5}>{tx("未读的低优先级通知按周期合并发送", "Unread low-priority notifications, batched")}</Muted>
              </span>
              <Segmented label={tx("汇总", "Digest")} value={digest ?? extra?.digest ?? "daily"} onChange={setDigest} options={[{ value: "off", label: tx("关闭", "Off") }, { value: "daily", label: tx("每天", "Daily") }, { value: "weekly", label: tx("每周", "Weekly") }]} />
            </Row>
          </div>
        </Panel>
        <Panel title={tx("订阅", "Subscriptions")}>
          <ul className="set-list">
            {extra?.subscriptions.map((s) => (
              <li key={s.id}>
                <Col gap={0} grow>
                  <Strong>{s.label}</Strong>
                  <Muted size={11.5}>{s.sub}</Muted>
                </Col>
                <Toggle on={subs[s.id] ?? s.on} onChange={(v) => setSubs({ ...subs, [s.id]: v })} label={s.label} />
              </li>
            ))}
          </ul>
        </Panel>
      </div>
    </Page>
  );
}

export function AccountSecurityPage() {
  const { tx } = useI18n();
  const { form } = useProfileForm();
  const [ended, setEnded] = useState<Set<string>>(new Set());
  const endSession = useSend<string>("DELETE", (id) => `/me/sessions/${id}`);
  return (
    <Page ai={false} bodyClass="set-narrow" crumbs={[tx("设置", "Settings"), tx("账号安全", "Account security")]}>
      <div className="v3-content anim-fade">
        <SettingsHead title={tx("账号安全", "Account security")} desc={tx("你自己的登录方式、两步验证、设备与访问令牌。工作区的统一策略在「安全策略」。", "Your own sign-in, two-factor, devices and tokens. Workspace-wide rules live in Security policy.")} />
        {!form ? (
          <Skeleton h={300} />
        ) : (
        <Panel title={tx("登录与两步验证", "Sign-in and two-factor")}>
              <Col gap={8}>
                <Row gap={10} className="set-sep">
                  <Col gap={0} grow>
                    <Strong>{tx("密码", "Password")}</Strong>
                    <Muted size={11.5}>{form.security.password_changed}</Muted>
                  </Col>
                  <Btn h={28}>{tx("修改密码", "Change password")}</Btn>
                </Row>
                <Row gap={8}>
                  <Strong>{tx("两步验证", "Two-factor")}</Strong>
                  <State tone="ok">{tx("已启用", "On")}</State>
                  <Spacer />
                </Row>
                <Muted size={11.5}>{form.security.org_rule}</Muted>
                <ul className="set-list">
                  {form.security.factors.map((f) => (
                    <li key={f.kind}>
                      <Tile icon={f.kind === "passkey" ? "shield" : f.kind === "totp" ? "clock" : "file"} tone={f.kind === "passkey" ? "accent" : "neutral"} size={30} />
                      <Col gap={0} grow>
                        <Strong>{f.name}</Strong>
                        <Muted size={11.5}>{f.sub}</Muted>
                      </Col>
                      <Btn h={26} kind={f.kind === "passkey" ? "secondary" : "ghost"}>
                        {f.kind === "passkey" ? tx("添加", "Add") : f.kind === "totp" ? tx("重新绑定", "Re-link") : tx("查看", "View")}
                      </Btn>
                    </li>
                  ))}
                </ul>
                <Section
                  title={tx("登录设备与会话", "Devices & sessions")}
                  right={
                    <TextLink icon={null} onClick={() => { setEnded(new Set(form.security.sessions.filter((s) => !s.current).map((s) => s.id))); toast(tx("已退出其他所有会话", "Signed out of all other sessions")); }}>
                      {tx("退出其他所有会话", "Sign out others")}
                    </TextLink>
                  }
                >
                  <ul className="set-list">
                    {form.security.sessions
                      .filter((s) => !ended.has(s.id))
                      .map((s) => (
                        <li key={s.id}>
                          <Tile icon={s.device.includes("iPhone") ? "phone" : "monitor"} size={28} />
                          <Col gap={0} grow>
                            <Strong>{s.device}</Strong>
                            <Muted size={11.5}>{s.sub}</Muted>
                          </Col>
                          {s.current ? (
                            <Badge tone="ok">{tx("当前会话", "This session")}</Badge>
                          ) : (
                            <Btn h={26} kind="ghost" onClick={() => { endSession.mutate(s.id); setEnded((e) => new Set(e).add(s.id)); toast(tx("已退出 {d}", "Signed out {d}", { d: s.device })); }}>
                              {tx("退出", "Sign out")}
                            </Btn>
                          )}
                        </li>
                      ))}
                  </ul>
                </Section>
                <Row gap={10}>
                  <Col gap={0} grow>
                    <Strong>{tx("个人访问令牌", "Personal access tokens")}</Strong>
                    <Muted size={11.5}>{form.security.tokens}</Muted>
                  </Col>
                  <Btn h={28} to="/integrations/webhooks">{tx("管理", "Manage")}</Btn>
                </Row>
              </Col>
            </Panel>
        )}
      </div>
    </Page>
  );
}
