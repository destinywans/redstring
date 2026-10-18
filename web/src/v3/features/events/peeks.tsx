import type { ReactNode } from "react";
import { API_MODE, ApiError, useGet } from "../../api/client";
import { Unavailable, UnknownValue, errorReason, type UnknownReason } from "../../ui/unknown";
import type { PersonPeek, ServicePeek } from "../../api/types";
import { Avatar, Badge, Btn, Dot, Env, KV, Muted, Row, Skeleton, Strong, Tier, Tile } from "../../ui/core";
import { HoverPeek, toast } from "../../ui/overlays";
import { useI18n } from "../../i18n";
import { openExternal, openForm } from "../../ui/demo";
import { useServiceStatus } from "../observe/ServicesPage";

/**
 * Why a peek couldn't be read: 403/404 is something this viewer can't see (the person peek answers 404 without
 * members.view), 409 is an ambiguous name, anything else a failed source.
 */
const peekReason = (error: unknown): UnknownReason | undefined =>
  error instanceof ApiError && (error.status === 403 || error.status === 404) ? "no_authorized_target" : error instanceof ApiError && error.status === 409 ? undefined : errorReason(error);

function ServiceCard({ id }: { id: string }) {
  const { tx } = useI18n();
  const serviceStatus = useServiceStatus();
  const { data, error } = useGet<ServicePeek>(`/services/${id}/peek`);
  // A failed read says why instead of an endless skeleton (PA-221).
  if (!data && error) return <Unavailable reason={peekReason(error)} icon="box" />;
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
            {data.tier && <Tier tier={data.tier} />}
          </Row>
          <Muted size={11}>
            {data.team ?? <UnknownValue label={tx("团队 —", "team —")} />} · <Env env={data.env} />
          </Muted>
        </div>
        {data.status ? (
          // Same words as the service catalog: warn also comes from the error budget or burn, not only alerts (PA-209).
          <Badge tone={data.status} title={serviceStatus(data.status).hint}>
            <Dot tone={data.status} size={6} />
            {serviceStatus(data.status).label}
          </Badge>
        ) : (
          <Badge>
            <UnknownValue label={tx("状态 —", "status —")} />
          </Badge>
        )}
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
        {data.owner ?? <UnknownValue />}
      </KV>
      <KV k={tx("值班", "On call")} kw={56}>
        {data.oncall ?? <UnknownValue />}
      </KV>
      <KV k={tx("最近变更", "Last change")} kw={56}>
        {data.change ?? <UnknownValue />}
      </KV>
      <Row gap={6}>
        <Btn h={26} iconRight="right" to={`/services/${data.id}`}>
          {tx("打开服务", "Open service")}
        </Btn>
        <Btn h={26} kind="soft" icon="branch" to={API_MODE === "mock" ? "/canvas/INV-231" : `/canvases?new=investigation&service=${encodeURIComponent(data.id)}`}>
          {tx("发起调查", "Investigate")}
        </Btn>
      </Row>
    </>
  );
}

function PersonCard({ name }: { name: string }) {
  const { tx } = useI18n();
  const { data, error } = useGet<PersonPeek>(`/people/${encodeURIComponent(name)}/peek`);
  // PA-214: unknown is never "off call". A failed read (404 without members.view, 409, 503) and a null on-call are
  // unknown; while it loads nothing is claimed. Only the sample's null means off call.
  const failed = !data && !!error;
  return (
    <>
      <Row gap={10}>
        <Avatar name={name} size={34} />
        <div className="v3-col grow" style={{ gap: 0 }}>
          <Strong size={13}>{name}</Strong>
          <Muted size={11.5}>{data ? data.role ?? "—" : failed ? <UnknownValue reason={peekReason(error)} /> : "…"}</Muted>
        </div>
      </Row>
      <Row gap={6}>
        {data?.oncall ? (
          <Badge tone="ok">
            <Dot tone="ok" size={6} />
            {data.oncall}
          </Badge>
        ) : data && API_MODE === "mock" ? (
          <Badge>{tx("非值班", "Off call")}</Badge>
        ) : data || failed ? (
          // The platform has no on-call source, so null proves nothing either way.
          <Badge>
            <UnknownValue reason={failed ? peekReason(error) : "not_tracked"} label={tx("值班 —", "on call —")} />
          </Badge>
        ) : (
          <Badge>…</Badge>
        )}
        {data?.load && <Muted size={11.5}>{data.load}</Muted>}
      </Row>
      {/* Chat, call and assign have no contract target yet; they only exist in sample data. */}
      {API_MODE === "mock" && (
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
      )}
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
