import { useEffect, useState } from "react";
import { Page } from "../../app/Shell";
import { useGet, useSend } from "../../api/client";
import type { SecurityView } from "../../api/types";
import { Badge, Brand, Btn, Check, Chip, Col, Divider, Field, Icon, IconBtn, Input, Muted, Panel, Radio, Row, Skeleton, State, Strong, Toggle } from "../../ui/core";
import { toast, MenuButton } from "../../ui/overlays";
import { useI18n } from "../../i18n";
import "./settings.css";
import { ChipSelect, openExternal } from "../../ui/demo";

export default function SecurityPage() {
  const { tx } = useI18n();
  const { data } = useGet<SecurityView>("/security");
  const [s, setS] = useState<SecurityView | null>(null);
  const save = useSend<SecurityView>("PUT", "/security", ["/security"]);
  useEffect(() => {
    if (data) setS(structuredClone(data));
  }, [data]);
  const dirty = !!data && !!s && JSON.stringify(data) !== JSON.stringify(s);
  const up = (patch: Partial<SecurityView>) => setS((x) => (x ? { ...x, ...patch } : x));
  return (
    <Page
      ai={false}
      crumbs={[tx("设置", "Settings"), tx("安全策略", "Security policy")]}
      actions={
        <Btn kind="primary" icon="check" disabled={!dirty} loading={save.isPending} onClick={() => s && save.mutate(s, { onSuccess: () => toast(tx("安全策略已保存并写入审计", "Security policy saved and audited")) })}>
          {tx("保存", "Save")}
        </Btn>
      }
    >
      {!s ? (
        <div className="v3-content">
          <Skeleton h={400} />
        </div>
      ) : (
        <div className="v3-content anim-fade" style={{ padding: "18px 24px" }}>
          <div className="sec-grid stagger">
            <Panel title={tx("单点登录", "Single sign-on")}>
              <ul className="set-list">
                {s.sso.map((p) => (
                  <li key={p.name}>
                    <Brand name={p.brand} size={30} />
                    <Col gap={0} grow>
                      <Row gap={6}>
                        <Strong>{p.name}</Strong>
                        {p.default && <Badge tone="accent">{tx("默认", "Default")}</Badge>}
                      </Row>
                      <Muted size={11.5}>{p.sub}</Muted>
                    </Col>
                    <State tone={p.enabled ? "ok" : "neutral"}>{p.enabled ? tx("已启用", "Enabled") : tx("未启用", "Off")}</State>
                    <MenuButton
                      trigger={({ onClick, ref }) => (
                        <span ref={ref}>
                          <IconBtn icon="more" label={tx("{n} 设置", "{n} settings", { n: p.name })} size={26} onClick={onClick} />
                        </span>
                      )}
                      items={[
                        { label: p.enabled ? tx("停用", "Disable") : tx("启用", "Enable"), icon: p.enabled ? "x" : "check", onSelect: () => up({ sso: s.sso.map((x) => (x.name === p.name ? { ...x, enabled: !x.enabled } : x)) }) },
                        { label: tx("设为默认登录方式", "Make default"), icon: "star", onSelect: () => up({ sso: s.sso.map((x) => ({ ...x, default: x.name === p.name })) }) },
                        { label: tx("测试登录", "Test sign-in"), icon: "play", onSelect: () => openExternal(p.name, "https://sso.example.internal/test") },
                      ]}
                    />
                  </li>
                ))}
              </ul>
              <Row gap={10} style={{ paddingTop: 10 }}>
                <Toggle on={s.scim} onChange={(v) => up({ scim: v })} label={tx("SCIM 自动开通", "SCIM provisioning")} />
                <Col gap={0}>
                  <Strong>{tx("SCIM 自动开通与停用", "SCIM provisioning & deprovisioning")}</Strong>
                  <Muted size={11.5}>{tx("离职人员在身份源停用后，平台账号 5 分钟内同步停用", "Accounts are disabled within 5 minutes of being disabled at the identity provider")}</Muted>
                </Col>
              </Row>
            </Panel>
            <Panel title={tx("两步验证", "Two-factor")}>
              <Col gap={10}>
                <Radio name="mfa" checked={s.mfa_policy === "all"} onChange={() => up({ mfa_policy: "all" })} label={tx("所有成员必须启用", "Required for everyone")} sub={tx("新成员有 7 天宽限期", "New members get 7 days of grace")} />
                <Radio name="mfa" checked={s.mfa_policy === "privileged"} onChange={() => up({ mfa_policy: "privileged" })} label={tx("仅管理员和审批人必须启用", "Admins and approvers only")} />
                <Radio name="mfa" checked={s.mfa_policy === "none"} onChange={() => up({ mfa_policy: "none" })} label={tx("不强制", "Not required")} sub={tx("不推荐", "Not recommended")} />
                <Divider />
                <Muted size={11.5}>{tx("允许的方式", "Allowed methods")}</Muted>
                <Row gap={18} align="flex-start" wrap>
                  {s.mfa_methods.map((m, i) => (
                    <Check key={m.id} checked={m.enabled} label={m.label} sub={m.note} onChange={(v) => up({ mfa_methods: s.mfa_methods.map((x, j) => (j === i ? { ...x, enabled: v } : x)) })} />
                  ))}
                </Row>
                <Row gap={10}>
                  <Toggle on={s.approval_passkey} onChange={(v) => up({ approval_passkey: v })} label={tx("审批需要通行密钥", "Passkey for approvals")} />
                  <span style={{ fontSize: "var(--fs-sm)" }}>{tx("批准生产写操作时，必须用通行密钥再次验证", "Approving production writes requires a passkey re-check")}</span>
                </Row>
              </Col>
            </Panel>
            <Panel title={tx("会话与访问", "Sessions & access")}>
              <div className="form-grid">
                <Field label={tx("空闲超时", "Idle timeout")}>
                  <ChipSelect label="" showLabel={false} value={s.session.idle} onChange={(v) => up({ session: { ...s.session, idle: v as string } })} options={[...new Set([s.session.idle, tx("30 分钟", "30 min"), tx("1 小时", "1 h"), tx("4 小时", "4 h")])]} />
                </Field>
                <Field label={tx("最长会话", "Max session")}>
                  <ChipSelect label="" showLabel={false} value={s.session.max} onChange={(v) => up({ session: { ...s.session, max: v as string } })} options={[...new Set([s.session.max, tx("8 小时", "8 h"), tx("12 小时", "12 h"), tx("7 天", "7 days")])]} />
                </Field>
                <Field label={tx("IP 白名单", "IP allow-list")} hint={tx("办公网与 VPN", "Office network and VPN")}>
                  <Input mono value={s.session.ip_allow} onChange={(e) => up({ session: { ...s.session, ip_allow: e.target.value } })} />
                </Field>
                <Field label={tx("新设备登录", "New-device sign-in")}>
                  <Row gap={8}>
                    <Toggle on={s.session.new_device_notice} onChange={(v) => up({ session: { ...s.session, new_device_notice: v } })} label={tx("新设备登录通知", "New-device notice")} />
                    <Muted size={11.5}>{tx("通过飞书与邮件提醒本人", "Notify the user via Feishu and email")}</Muted>
                  </Row>
                </Field>
              </div>
            </Panel>
            <Panel title={tx("凭据与密钥", "Credentials & secrets")}>
              <Col gap={10}>
                <Row gap={10}>
                  <Brand name="vault" size={30} />
                  <Col gap={0} grow>
                    <Strong>{s.vault.name}</Strong>
                    <Muted size={11.5}>{s.vault.sub}</Muted>
                  </Col>
                  <State tone="ok">{tx("已连接", "Connected")}</State>
                </Row>
                <Row gap={8}>
                  <Icon name="shield" size={14} color="var(--ok)" />
                  <span style={{ fontSize: "var(--fs-sm)" }}>{tx("连接器只保存凭据引用，平台不存明文", "Connectors store credential references only — no plaintext")}</span>
                </Row>
                <Row gap={8}>
                  <Icon name="shield" size={14} color="var(--ok)" />
                  <span style={{ fontSize: "var(--fs-sm)" }}>{tx("写操作凭据只由执行器持有，Agent 不可见", "Write credentials live only with the executor; agents never see them")}</span>
                  <Badge tone="accent">{tx("强制", "Enforced")}</Badge>
                </Row>
                <div className="form-grid">
                  <Field label={tx("API 令牌最长有效期", "Max API token lifetime")}>
                    <ChipSelect label="" showLabel={false} value={s.token_ttl} onChange={(v) => up({ token_ttl: v as string })} options={[...new Set([s.token_ttl, tx("30 天", "30 days"), tx("90 天", "90 days"), tx("1 年", "1 year")])]} />
                  </Field>
                  <Field label={tx("密码策略", "Password policy")}>
                    <ChipSelect label="" showLabel={false} value={s.password_policy} onChange={(v) => up({ password_policy: v as string })} options={[...new Set([s.password_policy, tx("至少 12 位 · 含三类字符", "12+ chars · 3 character classes"), tx("至少 16 位 · 含四类字符", "16+ chars · 4 character classes")])]} />
                  </Field>
                </div>
              </Col>
            </Panel>
          </div>
        </div>
      )}
    </Page>
  );
}
