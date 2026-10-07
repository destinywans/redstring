import { useState } from "react";
import { Page } from "../../app/Shell";
import { useGet, useSend } from "../../api/client";
import type { Member, MembersView, OrgNode, Role } from "../../api/types";
import { AiAvatar, Avatar, Badge, Brand, Btn, Card, Chip, Col, Field, Icon, Input, Mono, Muted, Pills, Row, SearchBox, Section, Spacer, State, cx } from "../../ui/core";
import { DataTable } from "../../ui/data";
import { Dialog, toast } from "../../ui/overlays";
import { useI18n } from "../../i18n";
import "./settings.css";
import { ChipSelect, useFacet, openForm } from "../../ui/demo";

export default function MembersPage() {
  const { tx } = useI18n();
  const [filter, setFilter] = useState<"all" | "admin" | "no_mfa" | "disabled">("all");
  const [q, setQ] = useState("");
  const [node, setNode] = useState("plat");
  const [invite, setInvite] = useState(false);
  const [email, setEmail] = useState("");
  const [checked, setChecked] = useState<Set<string>>(new Set());
  const { data } = useGet<MembersView>("/members", { filter, q });
  const { data: roleList } = useGet<{ items: Role[] }>("/roles");
  const [inviteRole, setInviteRole] = useState("readonly");
  const fRole = useFacet(data?.items, (m) => m.roles);
  const fSrc = useFacet(data?.items, (m) => m.source);
  const sync = useSend<void, { added: number; updated: number }>("POST", "/members/sync", ["/members"]);
  const remind = useSend<void, { sent: number }>("POST", "/members/mfa-reminders");
  const send = useSend<{ email: string }>("POST", "/members/invitations", ["/members"]);
  const mfa = (m: Member["mfa"]) =>
    m === "passkey" ? (
      <Badge tone="ok">
        <Icon name="shield" size={11} />
        {tx("通行密钥", "Passkey")}
      </Badge>
    ) : m === "totp" ? (
      <Badge tone="ok">{tx("验证器", "Authenticator")}</Badge>
    ) : m === "none" ? (
      <Badge tone="warn">{tx("未启用", "Off")}</Badge>
    ) : (
      <Muted>—</Muted>
    );
  const src = (s: Member["source"]) =>
    s === "feishu" ? (
      <Row gap={4}>
        <Brand name="feishu" size={18} />
        <Muted size={11.5}>{tx("飞书", "Feishu")}</Muted>
      </Row>
    ) : (
      <Muted>{{ ldap: "LDAP", system: tx("系统", "System"), invite: tx("邀请", "Invite") }[s]}</Muted>
    );
  const Tree = ({ items }: { items: OrgNode[] }) => (
    <ul className="org-tree">
      {items.map((n) => (
        <li key={n.id}>
          <button type="button" className={cx(node === n.id && "on")} style={{ paddingLeft: 8 + n.depth * 16 }} onClick={() => setNode(n.id)}>
            {n.kind === "org" && n.depth < 2 ? <Icon name="down" size={12} /> : <span style={{ width: 12 }} />}
            <Icon name={n.kind === "group" ? "shield" : n.kind === "team" ? "user" : "grid"} size={14} />
            <span className="ellipsis grow">{n.name}</span>
            <Muted size={11.5}>{n.count}</Muted>
          </button>
        </li>
      ))}
    </ul>
  );
  return (
    <Page
      flush
      ai={false}
      crumbs={[tx("设置", "Settings"), tx("成员与团队", "Members & teams")]}
      actions={
        <>
          <Btn icon="rotate" loading={sync.isPending} onClick={() => sync.mutate(undefined, { onSuccess: (r) => toast(tx("已从飞书同步：新增 {a}，更新 {u}", "Synced from Feishu: {a} added, {u} updated", { a: r.added, u: r.updated })) })}>
            {tx("从飞书同步", "Sync from Feishu")}
          </Btn>
          <Btn kind="primary" icon="plus" onClick={() => setInvite(true)}>
            {tx("邀请成员", "Invite")}
          </Btn>
        </>
      }
    >
      <div className="v3-split">
        <div className="side" style={{ width: 260, padding: 12, display: "flex", flexDirection: "column", gap: 12 }}>
          <Section
            title={tx("组织架构", "Organization")}
            right={
              <Row gap={4}>
                <Brand name="feishu" size={18} />
                <Muted size={11}>{tx("飞书同步", "Feishu sync")}</Muted>
              </Row>
            }
          >
            {data && <Tree items={data.org} />}
          </Section>
          <Section title={tx("用户组", "Groups")}>{data && <Tree items={data.groups} />}</Section>
        </div>
        <div className="main">
          <div className="v3-content" style={{ padding: "16px 20px" }}>
            <Row gap={8}>
              <Pills
                value={filter}
                onChange={setFilter}
                options={[
                  { value: "all", label: tx("全部", "All"), count: data?.counts.all },
                  { value: "admin", label: tx("管理员", "Admins"), count: data?.counts.admin },
                  { value: "no_mfa", label: tx("未启用两步验证", "No 2FA"), count: data?.counts.no_mfa },
                  { value: "disabled", label: tx("已停用", "Disabled"), count: data?.counts.disabled },
                ]}
              />
              <SearchBox placeholder={tx("姓名、邮箱或手机号", "Name, email or phone")} value={q} onChange={setQ} width={240} />
              <ChipSelect label={tx("角色", "Role")} {...fRole.props} />
              <ChipSelect label={tx("来源", "Source")} {...fSrc.props} options={fSrc.props.options.map((o) => ({ ...o, label: ({ feishu: tx("飞书", "Feishu"), ldap: "LDAP", system: tx("系统", "System"), invite: tx("邀请", "Invitation") } as Record<string, string>)[o.value] ?? o.value }))} />
            </Row>
            {data && !remind.isSuccess && (
              <div className="warn-bar anim-rise">
                <Icon name="alert" size={14} />
                <span className="grow">{tx("{n} 名成员还没有启用两步验证，宽限期还剩 {d} 天。", "{n} members haven't enabled 2FA; {d} days of grace left.", { n: data.counts.no_mfa, d: data.mfa_grace_days })}</span>
                <Btn h={26} loading={remind.isPending} onClick={() => remind.mutate(undefined, { onSuccess: (r) => toast(tx("已通过飞书提醒 {n} 人", "Reminded {n} people on Feishu", { n: r.sent })) })}>
                  {tx("提醒他们", "Remind them")}
                </Btn>
              </div>
            )}
            {checked.size > 0 && (
              <div className="bulk-bar anim-rise">
                <b>{tx("已选 {n} 人", "{n} selected", { n: checked.size })}</b>
                <Spacer />
                <Btn
                  h={26}
                  icon="shield"
                  onClick={async () => {
                    const v = await openForm({
                      title: tx("为 {n} 人分配角色", "Assign a role to {n} people", { n: checked.size }),
                      fields: [{ key: "role", label: tx("角色", "Role"), type: "select", options: (roleList?.items ?? []).filter((r) => r.kind !== "machine").map((r) => ({ value: r.name, label: r.name, hint: tx("{n} 人", "{n} people", { n: r.members }) })) }],
                      submit: tx("分配", "Assign"),
                    });
                    if (v) {
                      toast(tx("已为 {n} 人分配「{r}」，写入审计", "Assigned “{r}” to {n} people; audited", { n: checked.size, r: v.role }));
                      setChecked(new Set());
                    }
                  }}
                >
                  {tx("分配角色", "Assign role")}
                </Btn>
                <Btn h={26} kind="danger" onClick={() => { const n = checked.size; setChecked(new Set()); toast(tx("已停用 {n} 名成员", "Disabled {n} members", { n }), { undo: () => undefined }); }}>
                  {tx("停用", "Disable")}
                </Btn>
              </div>
            )}
            <Card style={{ overflow: "hidden" }}>
              <DataTable<Member>
                rows={data?.items.filter((m) => fRole.test(m) && fSrc.test(m))}
                loading={!data}
                rowKey={(m) => m.id}
                rowHeight={50}
                selectable
                checked={checked}
                onCheck={setChecked}
                columns={[
                  {
                    key: "name",
                    header: tx("成员", "Member"),
                    width: "22%",
                    render: (m) => (
                      <Row gap={10}>
                        {m.agent ? <AiAvatar size={26} /> : <Avatar name={m.name} size={26} accent={m.id === "u1"} />}
                        <Col gap={0} style={{ minWidth: 0 }}>
                          <span className="ellipsis" style={{ fontWeight: 500 }}>{m.name}</span>
                          <span className="ellipsis muted" style={{ fontSize: "var(--fs-sub)" }}>{m.email}</span>
                        </Col>
                      </Row>
                    ),
                  },
                  { key: "dept", header: tx("部门", "Department"), render: (m) => <Muted style={{ whiteSpace: "nowrap" }}>{m.department}</Muted> },
                  {
                    key: "roles",
                    header: tx("角色", "Roles"),
                    width: "20%",
                    render: (m) => (
                      <Row gap={4} wrap>
                        {m.roles.map((r, i) => (
                          <Badge key={r} tone={i === 0 && (m.agent || m.id === "u1") ? "accent" : "neutral"}>
                            {r}
                          </Badge>
                        ))}
                      </Row>
                    ),
                  },
                  { key: "phone", header: tx("手机", "Phone"), render: (m) => <Mono color="var(--muted)" size={11.5}>{m.phone}</Mono> },
                  { key: "mfa", header: tx("两步验证", "2FA"), render: (m) => mfa(m.mfa) },
                  { key: "source", header: tx("来源", "Source"), render: (m) => <span style={{ whiteSpace: "nowrap" }}>{src(m.source)}</span> },
                  { key: "status", header: tx("状态", "Status"), render: (m) => <State tone={m.status === "active" ? "ok" : "neutral"}>{m.status === "active" ? tx("活跃", "Active") : tx("已停用", "Disabled")}</State> },
                  { key: "last", header: tx("最近登录", "Last sign-in"), render: (m) => <Muted style={{ whiteSpace: "nowrap" }}>{m.last_login}</Muted> },
                ]}
              />
            </Card>
          </div>
        </div>
      </div>
      <Dialog
        open={invite}
        onClose={() => setInvite(false)}
        title={tx("邀请成员", "Invite member")}
        footer={
          <>
            <Btn kind="ghost" onClick={() => setInvite(false)}>{tx("取消", "Cancel")}</Btn>
            <Btn kind="primary" disabled={!email.includes("@")} loading={send.isPending} onClick={() => send.mutate({ email, role: inviteRole } as never, { onSuccess: () => { setInvite(false); setEmail(""); toast(tx("邀请已发送", "Invitation sent")); } })}>
              {tx("发送邀请", "Send invite")}
            </Btn>
          </>
        }
      >
        <Col gap={12}>
          <Field label={tx("邮箱", "Email")}>
            <Input value={email} onChange={(e) => setEmail(e.target.value)} placeholder="name@example.com" autoFocus />
          </Field>
          <Field label={tx("角色", "Role")} hint={tx("外部成员默认只读，需要单独授权应用范围", "External members start read-only and need app scopes granted")}>
            <ChipSelect label="" showLabel={false} value={inviteRole} onChange={(v) => setInviteRole(v as string)} options={[{ value: "readonly", label: tx("只读", "Read-only") }, ...(roleList?.items ?? []).filter((r) => r.kind === "business").map((r) => ({ value: r.id, label: r.name }))]} />
          </Field>
        </Col>
      </Dialog>
    </Page>
  );
}
