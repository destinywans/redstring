import SelectField from "../../components/SelectField";
import { useState } from "react";
import { Download, Upload } from "../../lib/icons";
import { post } from "../../lib/api";
import { Modal, ErrorNotice } from "../../components/ui";
import { useWorkspace } from "../../lib/workspace";
import { memberError, importReason } from "./errors";

type ImportRow = { external_id: string; email: string; name: string; department: string; job_title: string };
type ImportPreview = { fingerprint: string; can_import: boolean; items: { external_id: string; email: string; action: "create" | "update" | "conflict"; reason?: string }[] };
type PreparedImport = { source: string; rows: ImportRow[]; preview: ImportPreview };
const columns = ["external_id", "email", "name", "department", "job_title"] as const;

// Respect quoted separators and line breaks exported by common IAM directories.
function parseCSV(text: string): string[][] {
  const rows: string[][] = [];
  let row: string[] = [], cell = "", quoted = false, closedQuote = false;
  for (let i = 0; i < text.length; i++) {
    const c = text[i];
    if (quoted) {
      if (c === '"' && text[i + 1] === '"') { cell += '"'; i++; }
      else if (c === '"') { quoted = false; closedQuote = true; }
      else cell += c;
    } else if (c === '"') {
      if (cell || closedQuote) throw new Error("CSV 引号格式不正确，请使用下载的模板。");
      quoted = true;
    } else if (c === ",") { row.push(cell); cell = ""; closedQuote = false; }
    else if (c === "\n" || c === "\r") {
      row.push(cell); if (row.some(v => v.trim())) rows.push(row);
      row = []; cell = ""; closedQuote = false;
      if (c === "\r" && text[i + 1] === "\n") i++;
    } else {
      if (closedQuote && c.trim()) throw new Error("CSV 引号后有多余内容，请检查导出文件。");
      if (!closedQuote) cell += c;
    }
  }
  if (quoted) throw new Error("CSV 引号未闭合，请检查导出文件。");
  row.push(cell); if (row.some(v => v.trim())) rows.push(row);
  return rows;
}
function readRows(input: string, format: "csv" | "json"): ImportRow[] {
  const text = input.replace(/^\uFEFF/, "").trim();
  if (!text) throw new Error("请选择文件或粘贴成员数据。");
  if (text.length > 2_000_000) throw new Error("导入文件过大，请拆分为不超过 2 MB 的文件。");
  let values: unknown[];
  if (format === "json") {
    const parsed: unknown = JSON.parse(text);
    if (!Array.isArray(parsed)) throw new Error("JSON 应为成员对象数组，请参考模板。");
    values = parsed;
  } else {
    const [headers, ...records] = parseCSV(text);
    if (!headers || !["external_id", "email", "name"].every(k => headers.includes(k))) throw new Error("CSV 必须包含 external_id、email、name 表头。");
    if (new Set(headers).size !== headers.length || headers.some(k => !(columns as readonly string[]).includes(k))) throw new Error("CSV 包含重复或不支持的表头，请使用模板中的字段。");
    values = records.map((record, i) => {
      if (record.length !== headers.length) throw new Error(`第 ${i + 2} 行列数与表头不一致。`);
      return Object.fromEntries(headers.map((key, j) => [key, record[j]]));
    });
  }
  if (!values.length) throw new Error("文件没有成员记录。");
  if (values.length > 200) throw new Error("每次最多导入 200 位成员，请拆分文件。");
  return values.map((value, i) => {
    if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error(`第 ${i + 1} 位成员格式不正确。`);
    const v = value as Record<string, unknown>;
    if (Object.keys(v).some(k => !(columns as readonly string[]).includes(k))) throw new Error(`第 ${i + 1} 位成员包含不支持的字段。请勿导入密码、角色或登录令牌。`);
    const result = {} as ImportRow;
    for (const key of columns) {
      if (v[key] !== undefined && typeof v[key] !== "string") throw new Error(`第 ${i + 1} 位成员的 ${key} 必须是文本。`);
      result[key] = String(v[key] ?? "").trim();
    }
    if (!result.external_id || !result.email || !result.name) throw new Error(`第 ${i + 1} 位成员缺少外部标识、邮箱或姓名。`);
    return result;
  });
}
function downloadTemplate(format: "csv" | "json") {
  const sample = { external_id: "employee-001", email: "member@example.com", name: "示例成员", department: "平台工程", job_title: "工程师" };
  const body = format === "csv" ? `\uFEFF${columns.join(",")}\n${columns.map(k => sample[k]).join(",")}\n` : JSON.stringify([sample], null, 2);
  const url = URL.createObjectURL(new Blob([body], { type: format === "csv" ? "text/csv;charset=utf-8" : "application/json" }));
  const link = document.createElement("a"); link.href = url; link.download = `iam-members-template.${format}`; link.click(); setTimeout(() => URL.revokeObjectURL(url), 1000);
}

export default function MemberImport({ open, onClose, onImported }: { open: boolean; onClose: () => void; onImported: () => Promise<unknown> }) {
  return <Modal open={open} onOpenChange={v => !v && onClose()} title="导入 IAM 成员">{open && <ImportForm onClose={onClose} onImported={onImported} />}</Modal>;
}
function ImportForm({ onClose, onImported }: { onClose: () => void; onImported: () => Promise<unknown> }) {
  const { notify } = useWorkspace();
  const [source, setSource] = useState("");
  const [format, setFormat] = useState<"csv" | "json">("csv");
  const [text, setText] = useState("");
  const [prepared, setPrepared] = useState<PreparedImport | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [confirmed, setConfirmed] = useState(false);
  function resetPreview() { setPrepared(null); setConfirmed(false); setError(null); }
  async function previewImport() {
    setError(null); setPrepared(null); setConfirmed(false); setBusy(true);
    try {
      if (!source.trim()) throw new Error("请填写 IAM 来源名称，用于识别后续导入的同一批账号。");
      if (!/^[A-Za-z0-9._-]{1,100}$/.test(source.trim()) || source.trim().toLowerCase() === "local") throw new Error("IAM 来源名称只支持字母、数字、点、下划线和短横线，且不能使用 local。");
      const rows = readRows(text, format), selectedSource = source.trim();
      const preview = await post<ImportPreview>("/users/import/preview", { source: selectedSource, rows });
      setPrepared({ source: selectedSource, rows, preview });
    } catch (e) { setError(memberError(e)); } finally { setBusy(false); }
  }
  async function importMembers() {
    if (!prepared?.preview.can_import || !confirmed) return;
    setBusy(true); setError(null);
    try {
      const result = await post<{ created: number; updated: number }>("/users/import", { source: prepared.source, rows: prepared.rows, fingerprint: prepared.preview.fingerprint });
      await onImported(); notify(`导入完成：新增 ${result.created} 位，更新 ${result.updated} 位成员`, "success"); onClose();
    } catch (e) { setError(memberError(e)); setPrepared(null); setConfirmed(false); } finally { setBusy(false); }
  }
  return <div className="management-form member-import">
    <p>从企业 IAM 导出 CSV 或 JSON 成员目录，预览后导入平台。此操作不会建立持续同步或单点登录。</p>
    <div className="members-actions"><button className="btn" onClick={() => downloadTemplate("csv")}><Download size={15} />CSV 模板</button><button className="btn" onClick={() => downloadTemplate("json")}><Download size={15} />JSON 模板</button></div>
    <label>IAM 来源名称<input className="field" value={source} disabled={busy} maxLength={100} placeholder="例如 company-iam" onChange={e => {setSource(e.target.value);resetPreview();}} /></label>
    <p className="muted">同一来源请始终使用相同名称；external_id 是 IAM 中稳定的成员编号。重新导入只更新同来源、同编号的成员资料，不接管已有本地账号。</p>
    <div className="member-form-grid"><label>文件格式<SelectField className="field" value={format} disabled={busy} onChange={e => {setFormat(e.target.value as "csv" | "json");resetPreview();}}><option value="csv">CSV</option><option value="json">JSON 数组</option></SelectField></label><label>选择文件<input className="field" type="file" accept=".csv,.json,text/csv,application/json" disabled={busy} onChange={async e => {const file = e.target.files?.[0];if (!file) return;resetPreview();try {if (file.size > 2_000_000) throw new Error("文件超过 2 MB，请拆分后导入。");const value = await file.text();setFormat(file.name.toLowerCase().endsWith(".json") ? "json" : "csv");setText(value);} catch (error) {setError(error);}}} /></label></div>
    <label>成员数据<textarea className="field member-import-text" rows={7} value={text} disabled={busy} placeholder="选择文件，或直接粘贴 CSV / JSON 内容" onChange={e => {setText(e.target.value);resetPreview();}} /></label>
    <p className="muted">必填：external_id、email、name；可选：department、job_title。每次最多 200 位成员。新账号默认为只读且停用，管理员需设置本地密码后再启用。</p>
    {!!error && <ErrorNotice error={error} />}
    {prepared && <section className="member-import-preview"><h3>导入预览</h3><p>{prepared.preview.items.filter(i => i.action === "create").length} 位新增 · {prepared.preview.items.filter(i => i.action === "update").length} 位更新 · {prepared.preview.items.filter(i => i.action === "conflict").length} 项冲突</p><div className="management-table-wrap"><table className="management-table"><thead><tr><th>外部标识 / 邮箱</th><th>处理方式</th><th>说明</th></tr></thead><tbody>{prepared.preview.items.map((i, index) => <tr key={`${i.external_id}-${index}`}><td>{i.external_id}<small>{i.email}</small></td><td>{{create:"新增",update:"更新资料",conflict:"冲突"}[i.action]}</td><td>{importReason(i.reason)}</td></tr>)}</tbody></table></div>{!prepared.preview.can_import ? <p role="alert">请修正冲突后重新预览，当前内容不能导入。</p> : <label className="checkbox-label"><input type="checkbox" checked={confirmed} disabled={busy} onChange={e => setConfirmed(e.target.checked)} />我已核对导入名单，确认新增与更新成员资料。</label>}</section>}
    <footer><button className="btn" disabled={busy} onClick={onClose}>取消</button><button className="btn" disabled={busy || !text.trim()} onClick={() => void previewImport()}><Upload size={15} />{busy ? "处理中…" : "预览导入"}</button><button className="btn btn-primary" disabled={busy || !prepared?.preview.can_import || !confirmed} onClick={() => void importMembers()}>确认导入</button></footer>
  </div>;
}
