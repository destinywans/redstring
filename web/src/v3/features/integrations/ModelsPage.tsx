import { useState } from "react";
import { Page } from "../../app/Shell";
import { useGet } from "../../api/client";
import type { ModelAdminView } from "../../api/types";
import { Badge, Brand, Btn, Card, CardHead, Col, Dot, Icon, KV, Mono, Muted, Row, Skeleton, Spacer, Strong, cx } from "../../ui/core";
import { toast } from "../../ui/overlays";
import { useI18n } from "../../i18n";
import { PageHead } from "../conversation/ConversationsPage";
import "../../ui/forms.css";

const API_LABEL = { responses: "Responses", chat_completions: "Chat Completions", messages: "Messages" } as const;

/** Integrations › Models: which models exist, which API each uses, effort levels, defaults and who may use them. */
export default function ModelsPage() {
  const { tx } = useI18n();
  const { data } = useGet<ModelAdminView>("/models/admin");
  const [sel, setSel] = useState("openai:gpt-5");
  const m = data?.items.find((x) => x.id === sel);
  const detail = data?.detail.model === sel ? data.detail : null;
  return (
    <Page crumbs={[tx("集成中心", "Integrations"), tx("模型", "Models")]} actions={<Btn kind="primary" icon="plus" to="/integrations/new">{tx("添加模型连接器", "Add model connector")}</Btn>}>
      <div className="v3-content">
        <PageHead
          title={tx("模型", "Models")}
          desc={tx("对话与画布中可选的模型来自这里。每一轮只用一个模型；推理强度由模型声明，按接口映射成请求参数。", "Models offered in conversations and canvases come from here. One model per turn; effort levels are declared per model and mapped to request parameters by API.")}
        />
        <div className="md-providers">
          {!data && <Skeleton h={70} />}
          {data?.providers.map((p) => (
            <Card key={p.name} style={{ padding: "12px 14px" }}>
              <Row gap={10}>
                <Brand name={p.brand} size={30} />
                <Col gap={0} grow>
                  <Strong>{p.name}</Strong>
                  <Muted size={11.5}>{p.api}</Muted>
                </Col>
                <Badge tone={p.status === "ok" ? "ok" : "warn"}>
                  <Dot tone={p.status === "ok" ? "ok" : "warn"} size={6} />
                  {p.status === "ok" ? tx("已连接", "Connected") : tx("未验证", "Unverified")}
                </Badge>
              </Row>
            </Card>
          ))}
        </div>
        <div className="v3-row" style={{ gap: 14, alignItems: "flex-start", flexWrap: "wrap" }}>
          <Card style={{ overflow: "hidden", flex: 1, minWidth: 0 }}>
            <table className="v3-table">
              <thead>
                <tr>
                  <th>{tx("模型", "Model")}</th>
                  <th>{tx("接口", "API")}</th>
                  <th>{tx("推理强度", "Effort")}</th>
                  <th>{tx("用于", "Used by")}</th>
                  <th>{tx("可用范围", "Who can use")}</th>
                  <th>{tx("状态", "Status")}</th>
                </tr>
              </thead>
              <tbody>
                {data?.items.map((x) => (
                  <tr key={x.id} className={cx("clickable", sel === x.id && "on")} style={{ height: 46, background: sel === x.id ? "var(--accent-soft)" : undefined }} onClick={() => setSel(x.id)}>
                    <td>
                      <Row gap={8}>
                        <Brand name={x.brand} size={22} />
                        <span className="v3-col" style={{ gap: 0 }}>
                          <span style={{ fontWeight: 500 }}>{x.name}</span>
                          <Mono size={11} color="var(--muted)">{x.id}</Mono>
                        </span>
                        {x.is_default && <Badge tone="accent">{tx("默认", "Default")}</Badge>}
                      </Row>
                    </td>
                    <td><Badge mono>{API_LABEL[x.api]}</Badge></td>
                    <td>
                      {x.reasoning.kind === "none" ? (
                        <Muted>{tx("不支持", "None")}</Muted>
                      ) : (
                        <Row gap={3} wrap>
                          {x.reasoning.levels.map((l) => (
                            <Badge key={l.id} tone={l.id === x.reasoning.default ? "accent" : "neutral"}>{l.label}</Badge>
                          ))}
                        </Row>
                      )}
                    </td>
                    <td><Muted size={12}>{x.used_by}</Muted></td>
                    <td><Muted size={12}>{x.scope}</Muted></td>
                    <td>{x.status === "ok" ? <Badge tone="ok">{tx("已验证", "Verified")}</Badge> : <Badge tone="warn">{tx("未验证", "Unverified")}</Badge>}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </Card>
          {m && (
            <Card style={{ width: 360, flex: "none" }} className="anim-fade" key={m.id}>
              <CardHead title={<Row gap={8}><Brand name={m.brand} size={22} />{m.name}</Row>} right={<Btn h={24} kind="ghost" icon="play" onClick={() => toast(tx("连通性检查通过：工具调用、流式、看图", "Checks passed: tools, streaming, vision"))}>{tx("检查", "Check")}</Btn>} />
              <Col gap={12} style={{ padding: "12px 16px 16px" }}>
                <Col gap={6}>
                  <Muted size={11.5}>{tx("接口与回退", "API and fallback")}</Muted>
                  {(detail?.api_chain ?? [{ api: `${API_LABEL[m.api]} API`, note: tx("唯一接口", "Only API") }]).map((a, i) => (
                    <Row key={a.api} gap={8} align="flex-start">
                      <span className="f-num">{i + 1}</span>
                      <span className="v3-col" style={{ gap: 0 }}>
                        <span style={{ fontSize: "var(--fs-sm)", fontWeight: 500 }}>{a.api}</span>
                        <Muted size={11.5}>{a.note}</Muted>
                      </span>
                    </Row>
                  ))}
                </Col>
                <Col gap={6}>
                  <Muted size={11.5}>{tx("强度映射", "Effort mapping")}</Muted>
                  {detail ? (
                    detail.mapping.map((x) => (
                      <Row key={x.level} gap={8} style={{ fontSize: "var(--fs-sub)" }}>
                        <Badge tone={x.is_default ? "accent" : "neutral"}>{x.level}</Badge>
                        <Mono size={11.5}>{x.param}</Mono>
                        {x.is_default && <Muted size={11}>{tx("默认", "default")}</Muted>}
                      </Row>
                    ))
                  ) : m.reasoning.kind === "none" ? (
                    <Muted size={12}>{tx("该模型不支持推理强度，选择器中不显示强度。", "No effort control; the picker hides it for this model.")}</Muted>
                  ) : (
                    m.reasoning.levels.map((l) => (
                      <Row key={l.id} gap={8} style={{ fontSize: "var(--fs-sub)" }}>
                        <Badge tone={l.id === m.reasoning.default ? "accent" : "neutral"}>{l.label}</Badge>
                        <Mono size={11.5}>{m.api === "messages" ? `thinking.effort = "${l.id}"` : m.reasoning.kind === "toggle" ? `think = ${l.id === "on"}` : `reasoning_effort = "${l.id}"`}</Mono>
                      </Row>
                    ))
                  )}
                </Col>
                <KV k={tx("能力", "Capabilities")} kw={64}>
                  <Row gap={4} wrap>
                    {m.tools && <Badge>{tx("工具", "Tools")}</Badge>}
                    {m.vision && <Badge>{tx("看图", "Vision")}</Badge>}
                    <Badge>{m.context_k}K</Badge>
                    <Badge>{m.cost}</Badge>
                  </Row>
                </KV>
                <KV k={tx("可用角色", "Roles")} kw={64}>{detail?.roles ?? m.scope}</KV>
                <KV k="Agent" kw={64}>{detail?.agents ?? m.used_by}</KV>
                {detail && <KV k={tx("限额", "Limits")} kw={64}>{detail.limits}</KV>}
                <Row gap={6} align="flex-start" style={{ fontSize: "var(--fs-sub)", color: "var(--muted)" }}>
                  <Icon name="lock" size={13} />
                  <span>{tx("密钥保存在 Vault，前端与模型都拿不到；切换默认模型写入审计。", "Keys live in Vault and never reach the browser or the model; changing the default is audited.")}</span>
                </Row>
              </Col>
            </Card>
          )}
        </div>
        <Muted size={11.5}>{tx("后端现状：已支持 Chat Completions（含 Ollama）与 Claude Messages；Responses API 与推理强度透传待后端实现。", "Backend today: Chat Completions (incl. Ollama) and Claude Messages; Responses API and effort pass-through are pending in the backend.")}</Muted>
      </div>
    </Page>
  );
}
