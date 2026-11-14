import { useState } from "react";
import { useGet, useSend, ApiError } from "../../api/client";
import { serviceApplicationId } from "../../api/ids";
import { useI18n } from "../../i18n";
import { Btn, Field, Input, Muted, Row, Section, Toggle } from "../../ui/core";
import { Dialog, toast } from "../../ui/overlays";
import { ChipSelect, usePickLists } from "../../ui/demo";

type Tool = {
  name: string;
  description: string;
  tool_hash: string;
  decision: string;
  enabled: boolean;
  revision: number;
  input_schema: Record<string, unknown>;
};

type Candidate = {
  snapshot_id: string;
  registration_version: number;
  registration_hash: string;
  tools_hash: string;
  reviewed: boolean;
  tools: Tool[];
};

function reportError(error: unknown, fallback: string) {
  toast(error instanceof ApiError ? error.message : fallback, { tone: "danger" });
}

export function McpRegistration({ onClose, onCreated }: { onClose: () => void; onCreated: (id: string) => void }) {
  const { tx } = useI18n();
  const { services } = usePickLists();
  const [service, setService] = useState("");
  const [title, setTitle] = useState("");
  const [maintainer, setMaintainer] = useState("");
  const [endpoint, setEndpoint] = useState("");
  const [credential, setCredential] = useState("");
  const [allowHttp, setAllowHttp] = useState(false);
  const create = useSend<object, { id: string }>("POST", "/mcp-servers", ["/mcp-servers"]);
  const application = serviceApplicationId(service);
  const submit = (event: React.FormEvent) => {
    event.preventDefault();
    if (!application || create.isPending) return;
    create.mutate({
      application_id: application, title: title.trim(), maintainer: maintainer.trim(),
      endpoint: endpoint.trim(), credential_ref: credential.trim(), allow_http: allowHttp,
    }, { onSuccess: (result) => onCreated(result.id) });
  };
  return <Dialog open onClose={onClose} title={tx("登记 MCP 服务器", "Register MCP server")}>
    <form className="v3-col" style={{ gap: 10 }} onSubmit={submit}>
      <Muted>{tx("端点、应用和凭据引用须已由部署方批准。", "The endpoint, application and credential reference must be approved by the deployer.")}</Muted>
      <Field label={tx("应用", "Application")}><ChipSelect label={tx("应用", "Application")} value={service || undefined} onChange={(id) => setService(String(id))} options={services} /></Field>
      <Field label={tx("名称", "Name")}><Input aria-label={tx("名称", "Name")} required value={title} onChange={(e) => setTitle(e.target.value)} /></Field>
      <Field label={tx("维护方", "Maintainer")}><Input aria-label={tx("维护方", "Maintainer")} required value={maintainer} onChange={(e) => setMaintainer(e.target.value)} /></Field>
      <Field label={tx("端点", "Endpoint")}><Input aria-label={tx("端点", "Endpoint")} required type="url" value={endpoint} onChange={(e) => setEndpoint(e.target.value)} /></Field>
      <Field label={tx("凭据引用", "Credential reference")}><Input aria-label={tx("凭据引用", "Credential reference")} value={credential} onChange={(e) => setCredential(e.target.value)} /></Field>
      <Row gap={8}><Toggle label={tx("允许本机 HTTP", "Allow loopback HTTP")} on={allowHttp} onChange={setAllowHttp} />{tx("允许本机 HTTP", "Allow loopback HTTP")}</Row>
      <Row gap={8}><Btn type="submit" kind="primary" loading={create.isPending} disabled={!application}>{tx("登记", "Register")}</Btn></Row>
    </form>
  </Dialog>;
}

export function McpGovernance({ id }: { id: string }) {
  const { tx } = useI18n();
  const path = "/mcp-servers/" + encodeURIComponent(id);
  const candidate = useGet<Candidate>(path + "/review-candidate", undefined, { retry: false });
  const discover = useSend<void>("POST", path + "/discover", [path, "/mcp-servers"]);
  const review = useSend<object>("POST", path + "/review-version", [path, "/mcp-servers"]);
  const change = useSend<{ name: string; input: object }>("PATCH", (body) => path + "/reviewed-tools/" + encodeURIComponent(body.name), [path, "/mcp-servers"], (body) => body.input);
  const [decisions, setDecisions] = useState<Record<string, "allow_read_only" | "deny">>({});
  const data = candidate.data;
  const seal = data ? {
    snapshot_id: data.snapshot_id, registration_version: data.registration_version,
    registration_hash: data.registration_hash, tools_hash: data.tools_hash,
  } : null;

  function saveReview() {
    if (!data || !seal) return;
    review.mutate({ ...seal, decisions: data.tools.map((tool) => ({
      name: tool.name, tool_hash: tool.tool_hash, decision: decisions[tool.tool_hash] ?? "deny",
    })) }, { onSuccess: () => toast(tx("已保存工具审核", "Tool review saved")) });
  }

  return <Section title={tx("发现与审核", "Discovery and review")}>
    <Muted>{tx("审核和开关记录当前工具声明。执行权限由运行时单独校验。", "Review and switches apply to the current tool declarations. The runtime checks execution permissions separately.")}</Muted>
    <Row><Btn loading={discover.isPending} onClick={() => discover.mutate(undefined, {
      onSuccess: () => { setDecisions({}); toast(tx("已发现工具", "Tools discovered")); },
      onError: (error) => reportError(error, tx("发现失败", "Discovery failed")),
    })}>{tx("发现工具", "Discover tools")}</Btn></Row>
    {data?.tools.map((tool) => <div className="v3-col" style={{ gap: 4 }} key={tool.tool_hash}>
      <strong>{tool.name}</strong>
      <Muted>{tool.description}</Muted>
      <details><summary>{tx("输入定义", "Input schema")}</summary><pre style={{ whiteSpace: "pre-wrap" }}>{JSON.stringify(tool.input_schema, null, 2)}</pre></details>
      {data.reviewed ? <Row gap={8}>
        <Muted>{tool.decision === "allow_read_only" ? tx("已审为只读", "Approved as read-only") : tx("已拒绝", "Denied")}</Muted>
        {tool.decision === "allow_read_only" && seal && <Toggle on={tool.enabled} label={tool.name} onChange={(enabled) => {
          if (change.isPending) return;
          change.mutate({ name: tool.name, input: { ...seal, tool_hash: tool.tool_hash, expected_revision: tool.revision, enabled } });
        }} />}
      </Row> : <label>{tx("工具审核决定", "Tool review decision")}{" "}
        <select aria-label={tx("审核 {name}", "Review {name}", { name: tool.name })} value={decisions[tool.tool_hash] ?? "deny"}
          onChange={(e) => setDecisions((prev) => ({ ...prev, [tool.tool_hash]: e.target.value === "allow_read_only" ? "allow_read_only" : "deny" }))}>
          <option value="deny">{tx("拒绝", "Deny")}</option>
          <option value="allow_read_only">{tx("允许只读", "Allow read-only")}</option>
        </select>
      </label>}
    </div>)}
    {data && !data.reviewed && <Row><Btn kind="primary" loading={review.isPending} onClick={saveReview}>{tx("保存工具审核", "Save tool review")}</Btn></Row>}
  </Section>;
}
