import type { ReactNode } from "react";
import { useGet } from "../../api/client";
import type { PersonPeek, ServicePeek } from "../../api/types";
import { Avatar, Badge, Btn, Dot, Env, KV, Muted, Row, Skeleton, Strong, Tier, Tile } from "../../ui/core";
import { HoverPeek, toast } from "../../ui/overlays";
import { useI18n } from "../../i18n";
import { openExternal, openForm } from "../../ui/demo";

function ServiceCard({ id }: { id: string }) {
  const { tx } = useI18n();
  const { data } = useGet<ServicePeek>(`/services/${id}/peek`);
  if (!data)
    return (
      <>
        <Skeleton h={26} />
        <Skeleton h={48} />
      </>
    );
  return (
    <>
      <Row gap={8}>
        <Tile icon="box" size={26} />
        <div className="v3-col grow" style={{ gap: 0 }}>
          <Row gap={6}>
            <Strong size={13}>{data.name}</Strong>
            <Tier tier={data.tier} />
          </Row>
          <Muted size={11}>
            {data.team} · <Env env={data.env} />
          </Muted>
        </div>
        <Badge tone={data.status}>
          <Dot tone={data.status} size={6} />
          {data.status === "danger" ? tx("异常", "Degraded") : data.status === "warn" ? tx("告警", "Alerting") : tx("正常", "Healthy")}
        </Badge>
      </Row>
      <div style={{ display: "grid", gridTemplateColumns: "repeat(3, minmax(0, 1fr))", gap: 6 }}>
        {data.kpis.map((k) => (
          <div key={k.k} style={{ padding: "6px 8px", borderRadius: 7, background: "var(--sunken)" }}>
            <Muted size={10.5}>{k.k}</Muted>
            <b className="tnum" style={{ display: "block", fontSize: "var(--fs-lg)", fontWeight: 600, color: k.tone ? `var(--${k.tone})` : undefined }}>
              {k.v}
            </b>
          </div>
        ))}
      </div>
      <KV k={tx("负责人", "Owner")} kw={56}>
        {data.owner}
      </KV>
      <KV k={tx("值班", "On call")} kw={56}>
        {data.oncall}
      </KV>
      <KV k={tx("最近变更", "Last change")} kw={56}>
        {data.change}
      </KV>
      <Row gap={6}>
        <Btn h={26} iconRight="right" to={`/services/${data.id}`}>
          {tx("打开服务", "Open service")}
        </Btn>
        <Btn h={26} kind="soft" icon="branch" to="/canvas/INV-231">
          {tx("发起调查", "Investigate")}
        </Btn>
      </Row>
    </>
  );
}

function PersonCard({ name }: { name: string }) {
  const { tx } = useI18n();
  const { data } = useGet<PersonPeek>(`/people/${encodeURIComponent(name)}/peek`);
  return (
    <>
      <Row gap={10}>
        <Avatar name={name} size={34} />
        <div className="v3-col grow" style={{ gap: 0 }}>
          <Strong size={13}>{name}</Strong>
          <Muted size={11.5}>{data?.role ?? "…"}</Muted>
        </div>
      </Row>
      <Row gap={6}>
        {data?.oncall ? (
          <Badge tone="ok">
            <Dot tone="ok" size={6} />
            {data.oncall}
          </Badge>
        ) : (
          <Badge>{tx("非值班", "Off call")}</Badge>
        )}
        <Muted size={11.5}>{data?.load}</Muted>
      </Row>
      <Row gap={6}>
        <Btn h={26} icon="message" onClick={() => openExternal(tx("飞书会话", "Feishu chat"), `https://applink.feishu.cn/client/chat/open?name=${encodeURIComponent(name)}`)}>
          {tx("飞书", "Feishu")}
        </Btn>
        <Btn h={26} icon="phone" onClick={() => openExternal(tx("电话外呼", "Phone call"), "https://oncall.example.internal/call")}>
          {tx("电话", "Call")}
        </Btn>
        <Btn
          h={26}
          kind="ghost"
          icon="user"
          onClick={async () => {
            const v = await openForm({
              title: tx("指派给 {n}", "Assign to {n}", { n: name }),
              fields: [
                {
                  key: "item",
                  label: tx("事项", "Item"),
                  type: "select",
                  options: [
                    { value: "ALR-7729", label: "ALR-7729", hint: tx("P2 · inventory-03 磁盘 IO", "P2 · inventory-03 disk IO") },
                    { value: "INC-1042", label: "INC-1042", hint: tx("checkout-api 5xx", "checkout-api 5xx") },
                    { value: "PRP-88", label: "PRP-88", hint: tx("回滚提案复核", "Rollback proposal review") },
                  ],
                },
              ],
              submit: tx("指派", "Assign"),
            });
            if (v) toast(tx("已将 {i} 指派给 {n}，对方会收到通知", "{i} assigned to {n}; they will be notified", { i: v.item, n: name }));
          }}
        >
          {tx("指派", "Assign")}
        </Btn>
      </Row>
    </>
  );
}

/** Service name with a hover preview card (status, KPIs, owner, last change). */
export function ServicePeekLink({ id, children }: { id: string; children?: ReactNode }) {
  return (
    <HoverPeek content={() => <ServiceCard id={id} />}>
      <span className="peek-target">{children ?? id}</span>
    </HoverPeek>
  );
}
/** Person name with a hover preview card (role, on-call, contact). */
export function PersonPeekLink({ name }: { name: string }) {
  return (
    <HoverPeek content={() => <PersonCard name={name} />} place="bottom-end">
      <span className="v3-row" style={{ gap: 6 }}>
        <Avatar name={name} size={22} />
        <span className="peek-target">{name}</span>
      </span>
    </HoverPeek>
  );
}
