import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { Page } from "../../app/Shell";
import { API_MODE, api } from "../../api/client";
import { Badge, Brand, Btn, Card, Col, Field, Icon, Input, KV, LabelChip, Mono, Muted, Row, Segmented, Spacer, Stepper, Strong, Toggle } from "../../ui/core";
import { toast } from "../../ui/overlays";
import { Callout } from "../../ui/forms";
import { useI18n } from "../../i18n";
import "./knowledge.css";
import { AddChip } from "../../ui/demo";

type SourceType = "confluence" | "notion" | "feishu" | "git_markdown" | "web" | "upload";
type TestResult = { ok: boolean; message: string; spaces?: { id: string; name: string; docs: number }[] };

/** /knowledge-sources/new — type → connection (Vault ref) → scope → sync & chunking → access → preview. */
export default function NewSourcePage() {
  const { tx } = useI18n();
  const nav = useNavigate();
  const [step, setStep] = useState(0);
  const [type, setType] = useState<SourceType>(API_MODE === "http" ? "git_markdown" : "confluence");
  const [url, setUrl] = useState(API_MODE === "http" ? "" : "https://wiki.example.internal");
  const [secret, setSecret] = useState(API_MODE === "http" ? "" : "vault:kv/aiops/confluence-readonly");
  const [test, setTest] = useState<TestResult | null>(null);
  const [testing, setTesting] = useState(false);
  const [spaces, setSpaces] = useState<Record<string, boolean>>({ SRE: true, TRADE: true, ARCH: false });
  const [schedule, setSchedule] = useState<"6h" | "24h" | "manual">(API_MODE === "http" ? "manual" : "6h");
  const [chunk, setChunk] = useState<"heading" | "fixed">("heading");
  const [inherit, setInherit] = useState(true);
  const [q, setQ] = useState("");
  const [saving, setSaving] = useState(false);
  const [applicationID, setApplicationID] = useState("");
  const [teamID, setTeamID] = useState("");
  const [createError, setCreateError] = useState("");
  const supported = type === "git_markdown" || type === "web";
  const TYPES: { key: SourceType; brand?: string; icon?: "link" | "upload"; label: string }[] = [
    { key: "confluence", brand: "confluence", label: "Confluence" },
    { key: "notion", brand: "notion", label: "Notion" },
    { key: "feishu", brand: "feishu", label: tx("飞书知识库", "Feishu wiki") },
    { key: "git_markdown", brand: "gitlab", label: "Git Markdown" },
    { key: "web", icon: "link", label: tx("网页", "Web pages") },
    { key: "upload", icon: "upload", label: tx("上传", "Upload") },
  ];
  const steps = [tx("类型", "Type"), tx("连接与认证", "Connection"), tx("范围", "Scope"), tx("同步与切分", "Sync & chunking"), tx("访问权限", "Access"), tx("预览", "Preview")];
  const runTest = async () => {
    setTesting(true);
    try {
      setTest(await api<TestResult>("POST", "/knowledge-sources/test", { body: { type, url, secret_ref: secret,
        ...(type === "git_markdown" ? { application_id: applicationID.trim(), team_id: teamID.trim() } : {}) } }));
    } catch (error) {
      setTest({ ok: false, message: error instanceof Error ? error.message : tx("连接测试失败", "Connection test failed") });
    } finally {
      setTesting(false);
    }
  };
  const docs = (test?.spaces ?? []).filter((s) => spaces[s.id]).reduce((a, s) => a + s.docs, 0);
  const canNext = (step !== 0 || API_MODE === "mock" || supported) &&
    (step !== 1 || !!test?.ok) &&
    (step !== 2 || !supported || applicationID.trim().length > 0);
  const create = async () => {
    setSaving(true);
    setCreateError("");
    try {
      await api("POST", "/knowledge-sources", { body: { name: TYPES.find((t) => t.key === type)?.label, type, url,
        secret_ref: secret, application_id: applicationID.trim(), team_id: teamID.trim(),
        spaces: supported ? [] : Object.keys(spaces).filter((k) => spaces[k]),
        schedule: supported ? "manual" : schedule, chunk, inherit_acl: supported ? true : inherit } });
      toast(tx("来源已登记；请在列表中手动同步", "Source registered; sync it manually from the list"));
      nav("/knowledge-sources");
    } catch (error) {
      setCreateError(error instanceof Error ? error.message : tx("登记失败", "Registration failed"));
    } finally {
      setSaving(false);
    }
  };
  const selectType = (next: SourceType) => {
    setType(next);
    setTest(null);
    setCreateError("");
    if (next === "git_markdown" || next === "web") {
      setUrl("");
      setSecret("");
      setSchedule("manual");
      setSpaces({});
      setInherit(true);
    }
  };
  const cur = TYPES.find((t) => t.key === type)!;
  return (
    <Page flush crumbs={[tx("知识与记忆", "Knowledge"), tx("知识来源", "Sources"), tx("添加", "Add")]} ai={false} actions={<Btn kind="ghost" to="/knowledge-sources">{tx("取消", "Cancel")}</Btn>}>
      <div className="v3-split">
        <div className="main">
          <div className="v3-content" style={{ maxWidth: 880 }}>
            <Card style={{ padding: "14px 18px" }}>
              <Stepper steps={steps} active={step} />
            </Card>
            {step === 0 && (
              <div className="v3-field" role="group" aria-label={tx("类型", "Type")}>
                <span className="label">{tx("类型", "Type")}</span>
                <div className="ks-types">
                  {TYPES.map((t) => (
                    <button key={t.key} type="button" className={type === t.key ? "on" : ""} onClick={() => selectType(t.key)} aria-pressed={type === t.key}>
                      {t.brand ? <Brand name={t.brand} size={20} /> : <Icon name={t.icon!} size={15} />}
                      {t.label}
                    </button>
                  ))}
                </div>
                {API_MODE === "http" && !supported && <Callout icon="alert">{tx("此来源类型尚未接入。当前可登记公开 Markdown 网页或已由管理员配置的 Git Markdown 仓库。", "This source type is not connected yet. You can register a public Markdown page or an administrator-configured Git Markdown repository.")}</Callout>}
              </div>
            )}
            {step === 1 && (
              <>
                <Field label={tx("地址", "URL")}>
                  <Input value={url} onChange={(e) => { setUrl(e.target.value); setTest(null); }} mono />
                </Field>
                <Field label={tx("凭据（Vault 引用）", "Credential (Vault reference)")} hint={supported ? tx("Git Markdown 使用管理员绑定的 vault://mount/path#field；公开网页留空。", "Git Markdown uses an administrator-bound vault://mount/path#field reference; leave this blank for a public page.") : tx("请输入 Vault 引用，不要粘贴明文凭据。", "Enter a Vault reference, not a plaintext credential.")}>
                  <Input value={secret} onChange={(e) => { setSecret(e.target.value); setTest(null); }} mono icon="key" placeholder={type === "git_markdown" ? "vault://ops/gitlab#token" : ""} />
                </Field>
                {type === "git_markdown" && <>
                  <Field label={tx("应用 ID", "Application ID")} hint={tx("连接探测会按所选应用范围检查知识管理权限。", "The connection test checks knowledge management access for this application.")}>
                    <Input value={applicationID} onChange={(e) => { setApplicationID(e.target.value); setTest(null); }} mono />
                  </Field>
                  <Field label={tx("负责人团队 ID（可选）", "Owner team ID (optional)")}>
                    <Input value={teamID} onChange={(e) => { setTeamID(e.target.value); setTest(null); }} mono />
                  </Field>
                </>}
                <Row gap={8}>
                  <Btn icon="plug" loading={testing} disabled={type === "git_markdown" && !applicationID.trim()} onClick={runTest}>{tx("测试连接", "Test connection")}</Btn>
                  {test && (
                    <Row gap={6} className="anim-fade">
                      <Icon name={test.ok ? "check" : "alert"} size={14} color={test.ok ? "var(--ok)" : "var(--danger)"} />
                      <span style={{ fontSize: "var(--fs-sm)" }}>{test.message}</span>
                    </Row>
                  )}
                </Row>
              </>
            )}
            {step === 2 && (
              <>
                {type === "web" && <>
                  <Field label={tx("应用 ID", "Application ID")} hint={tx("来源只会登记到所选环境中的这个应用。", "This source will belong to this application in the selected environment.")}>
                    <Input value={applicationID} onChange={(e) => setApplicationID(e.target.value)} mono />
                  </Field>
                  <Field label={tx("负责人团队 ID（可选）", "Owner team ID (optional)")}>
                    <Input value={teamID} onChange={(e) => setTeamID(e.target.value)} mono />
                  </Field>
                  <Callout icon="layers">{tx("只读取这个公开 Markdown 网页；登记后可手动同步。", "Only this public Markdown page is read; you can sync manually after registration.")}</Callout>
                </>}
                {type === "git_markdown" && <Callout icon="layers">{tx("来源绑定到应用 {app}。仓库项目、分支与 Markdown 路径由管理员固定配置；登记后可手动同步。", "The source is bound to application {app}. The repository project, branch and Markdown path are fixed by an administrator; you can sync manually after registration.", { app: applicationID })}</Callout>}
                {!supported && <>
                <Field label={tx("空间 · 已选 {n} 个", "Spaces · {n} selected", { n: Object.values(spaces).filter(Boolean).length })}>
                  <ul className="ks-spaces">
                    {(test?.spaces ?? []).map((s) => (
                      <li key={s.id}>
                        <input type="checkbox" checked={!!spaces[s.id]} onChange={(e) => setSpaces({ ...spaces, [s.id]: e.target.checked })} aria-label={s.name} />
                        <Icon name="book" size={14} />
                        <b style={{ fontWeight: 500 }}>{s.name}</b>
                        <Spacer />
                        <Muted size={11.5}>{tx("{n} 篇", "{n} docs", { n: s.docs })}</Muted>
                      </li>
                    ))}
                  </ul>
                </Field>
                <div className="f-grid2">
                  <Field label={tx("包含标签", "Include labels")}>
                    <Row gap={6} wrap><LabelChip k="label" v="runbook" /><LabelChip k="label" v="postmortem" /><LabelChip k="label" v="oncall" /></Row>
                  </Field>
                  <Field label={tx("排除", "Exclude")}>
                    <Row gap={6} wrap><LabelChip k="label" v="draft" /><LabelChip k="path" v="/archive/*" /></Row>
                  </Field>
                </div>
                <Callout icon="layers">{tx("预计 {d} 篇文档 · 约 {c} 个分块 · 首次同步约 6 分钟", "~{d} documents · ~{c} chunks · first sync ~6 minutes", { d: docs, c: Math.round(docs * 14.5).toLocaleString() })}</Callout>
                </>}
              </>
            )}
            {step === 3 && (
              <>
                {supported ? <Callout icon="rotate">{tx("当前仅支持手动同步；文档切分和向量索引尚未接入。", "Only manual sync is available; chunking and vector indexing are not connected yet.")}</Callout> : <Field label={tx("同步频率", "Sync schedule")}>
                  <Segmented label={tx("同步频率", "Sync schedule")} value={schedule} onChange={setSchedule} options={[{ value: "6h", label: tx("每 6 小时 · 增量", "Every 6 h · incremental") }, { value: "24h", label: tx("每天", "Daily") }, { value: "manual", label: tx("手动", "Manual") }]} />
                </Field>}
                {!supported && <Field label={tx("切分方式", "Chunking")}>
                  <Segmented label={tx("切分方式", "Chunking")} value={chunk} onChange={setChunk} options={[{ value: "heading", label: tx("按标题 · 约 600 字", "By heading · ~600 chars") }, { value: "fixed", label: tx("固定长度 · 800 字", "Fixed · 800 chars") }]} />
                </Field>}
                {!supported && <Field label={tx("向量模型", "Embedding model")}>
                  <Row gap={6}><Brand name="ollama" size={20} /><Mono size={12}>bge-m3</Mono><Muted size={11.5}>{tx("本地 · 数据不出内网", "local · data stays on-prem")}</Muted></Row>
                </Field>}
              </>
            )}
            {step === 4 && (
              <>
                {supported ? <Callout icon="lock">{tx("此来源绑定到所选应用及其当前权限；同步前后都会重新检查访问权限。", "This source is bound to the selected application and its current permissions; access is checked before and after sync.")}</Callout> : <Row gap={10}>
                  <Toggle on={inherit} onChange={setInherit} label={tx("继承来源权限", "Inherit source permissions")} />
                  <span className="v3-col" style={{ gap: 0 }}>
                    <span style={{ fontSize: "var(--fs-sm)", fontWeight: 500 }}>{tx("继承 {t} 空间权限", "Inherit {t} space permissions", { t: cur.label })}</span>
                    <Muted size={11.5}>{tx("检索时按提问人的权限过滤，Agent 不会读到提问人看不到的文档", "Results are filtered by the asker's permissions; agents never read what the asker cannot")}</Muted>
                  </span>
                </Row>}
                {!supported && <Field label={tx("额外可见团队", "Also visible to")}>
                  <Row gap={6}><Badge>{tx("SRE", "SRE")}</Badge><Badge>{tx("交易平台", "Trading")}</Badge><AddChip label={tx("团队", "Team")} options={[tx("支付", "Payments"), tx("基础设施", "Infrastructure"), tx("数据平台", "Data platform"), tx("安全", "Security")]} /></Row>
                </Field>}
              </>
            )}
            {step === 5 && (
              <Callout tone="ok" icon="check">
                {supported ? tx("将登记 {t} 到应用 {app}。登记后请从列表手动同步；文档数按真实读取显示，索引状态保持未证实。", "Register {t} for application {app}. Sync manually from the list; document count will reflect actual reads, while indexing remains unverified.", { t: cur.label, app: applicationID }) :
                tx("将从 {t} 同步 {d} 篇文档，按「{c}」切分，{s}。", "Will sync {d} documents from {t}, chunked {c}, {s}.", {
                  t: cur.label,
                  d: docs,
                  c: chunk === "heading" ? tx("按标题", "by heading") : tx("固定长度", "fixed length"),
                  s: schedule === "manual" ? tx("手动同步", "synced manually") : schedule === "6h" ? tx("每 6 小时增量同步", "incremental every 6 h") : tx("每天同步", "daily"),
                })}
              </Callout>
            )}
            <Row gap={8}>
              {step > 0 && <Btn kind="ghost" icon="left" onClick={() => setStep(step - 1)}>{tx("上一步", "Back")}</Btn>}
              <Spacer />
              {step === 1 && !test?.ok && <Muted size={11.5}>{tx("测试连接成功后才能继续", "Test the connection to continue")}</Muted>}
              {step < 5 ? (
                <Btn kind="primary" iconRight="right" disabled={!canNext} onClick={() => setStep(step + 1)}>
                  {tx("下一步：{s}", "Next: {s}", { s: steps[step + 1] })}
                </Btn>
              ) : (
                <Btn kind="primary" icon="check" loading={saving} onClick={create}>{supported ? tx("登记来源", "Register source") : tx("添加并开始同步", "Add and start syncing")}</Btn>
              )}
            </Row>
            {createError && <Callout icon="alert">{createError}</Callout>}
          </div>
        </div>
        <aside className="aside" style={{ width: 340 }}>
          <div className="v3-col" style={{ gap: 16, padding: 16 }}>
            <Row gap={10}>
              {cur.brand ? <Brand name={cur.brand} size={36} /> : <Icon name={cur.icon!} size={24} />}
              <Col gap={0}>
                <Strong size={14}>{cur.label}</Strong>
                <Muted size={11.5}>{tx("知识来源", "Knowledge source")}</Muted>
              </Col>
            </Row>
            <Col gap={7}>
              <Muted size={11.5}>{tx("将这样接入", "How it connects")}</Muted>
              <KV k={tx("认证", "Auth")} kw={56}><Row gap={6}><Brand name="vault" size={18} /><Muted size={11.5}>{tx("Vault 引用 · 只读账号", "Vault ref · read-only")}</Muted></Row></KV>
              <KV k={tx("同步", "Sync")} kw={56}>{supported ? tx("手动", "Manual") : schedule === "6h" ? tx("每 6 小时 · 增量", "Every 6 h · incremental") : schedule === "24h" ? tx("每天", "Daily") : tx("手动", "Manual")}</KV>
              <KV k={tx("切分", "Chunks")} kw={56}>{supported ? tx("尚未索引", "Not indexed") : chunk === "heading" ? tx("按标题 · 约 600 字", "By heading · ~600") : tx("固定 800 字", "Fixed 800")}</KV>
              <KV k={tx("权限", "Access")} kw={56}>{inherit ? tx("继承来源权限", "Inherited") : tx("仅所选团队", "Selected teams")}</KV>
            </Col>
            {step >= 2 && !supported && (
              <Col gap={8}>
                <Muted size={11.5}>{tx("检索试用", "Try a search")}</Muted>
                <Input icon="search" value={q} onChange={(e) => setQ(e.target.value)} placeholder={tx("重试风暴怎么处理", "How to handle a retry storm")} />
                {[["0.82", tx("SRE 手册 / 下游超时与重试风暴", "SRE handbook / Retry storms")], ["0.77", tx("交易平台 / checkout-api 容量说明", "Trading / checkout-api capacity")], ["0.61", tx("SRE 手册 / 发布回滚规范", "SRE handbook / Rollback guide")]].map(([s, t]) => (
                  <Row key={t} gap={8} style={{ fontSize: "var(--fs-sub)" }}>
                    <Mono size={11} color="var(--muted)">{s}</Mono>
                    <span className="ellipsis">{t}</span>
                  </Row>
                ))}
              </Col>
            )}
            <Row gap={8} align="flex-start" style={{ fontSize: "var(--fs-sub)", color: "var(--muted)" }}>
              <Icon name="lock" size={13} />
              <span>{supported
                ? tx("正文将保存为私有来源快照；当前不会进入知识检索。", "Content is stored as a private source snapshot and is not yet available in knowledge search.")
                : tx("文档内容只作为检索数据，不会被当作指令执行。", "Document content is retrieval data only; it is never executed as instructions.")}</span>
            </Row>
          </div>
        </aside>
      </div>
    </Page>
  );
}
