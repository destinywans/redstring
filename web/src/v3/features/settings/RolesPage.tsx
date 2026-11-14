import { useEffect, useState } from "react";
import { Page } from "../../app/Shell";
import { API_MODE, ApiError, api, newIdempotencyKey, useGet, useGetWithEtag } from "../../api/client";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { MembersView, Role, RoleDetail } from "../../api/types";
import { AvatarGroup, Badge, Btn, Card, Chip, Col, Icon, Muted, Row, SearchBox, Section, Skeleton, Tabs, Tile, cx } from "../../ui/core";
import { toast } from "../../ui/overlays";
import { Gated, useNeed } from "../../ui/permission";
import { LoadError } from "../../ui/unknown";
import { useI18n } from "../../i18n";
import "./settings.css";
import { openForm, AddChip, usePickLists } from "../../ui/demo";
import { useFirstSelection } from "../../ui/selection";
import { useEnv } from "../../app/environment";
import { addMemberRole } from "./memberAccess";

export default function RolesPage() {
  const { tx } = useI18n();
  // Without roles.view the list is refused (403): the page says so instead of an endless skeleton (PA-200).
  const { data: list, error } = useGet<{ items: Role[] }>("/roles");
  const [sel, setSel] = useState("");
  const [q, setQ] = useState("");
  const [tab, setTab] = useState("perm");
  const env = useEnv((s) => s.env);
  useFirstSelection(list?.items, (x) => x.id, sel, setSel);
  // The matrix is saved against the revision it was read at (If-Role-Matrix-Revision, PA-158).
  const { data: read, error: detailError } = useGetWithEtag<RoleDetail>(sel ? `/roles/${sel}` : null);
  const r = read?.data;
  const lists = usePickLists();
  const [matrix, setMatrix] = useState<RoleDetail["matrix"] | null>(null);
  // Saving the matrix needs roles.manage (PA-200).
  const noManage = useNeed(["roles", "manage"]);
  const noMembers = useNeed(["members", "manage"]);
  const { data: members } = useGet<MembersView>(API_MODE === "http" && !noManage && !noMembers && r?.kind !== "machine" ? "/members" : null);
  const memberOptions = (members?.items ?? []).filter((member) => !member.agent).map((member) => ({
    value: member.id, label: member.name, hint: member.email,
  }));
  const client = useQueryClient();
  const save = useMutation<unknown, ApiError, { matrix: RoleDetail["matrix"] }>({
    mutationFn: (body) => api("PUT", `/roles/${sel}`, { body, headers: read?.revision ? { "If-Role-Matrix-Revision": read.revision } : undefined }),
    onSettled: () => client.invalidateQueries({ predicate: (q) => String(q.queryKey[0]).startsWith("/roles") }),
  });
  useEffect(() => setMatrix(r ? r.matrix : null), [r]);
  const dirty = !!r && !!matrix && JSON.stringify(r.matrix) !== JSON.stringify(matrix);
  const kinds: [Role["kind"], string][] = [
    ["system", tx("系统角色", "System roles")],
    ["business", tx("业务角色", "Business roles")],
    ["machine", tx("机器身份", "Machine identities")],
  ];
  const create = async () => {
    const v = await openForm({
      title: tx("新建角色", "New role"),
      desc: tx("为所选范围授予环境准入或只读权限。全局角色可在权限矩阵中继续配置。", "Grant environment access or read-only permissions in the selected scope. Global roles can be configured further in the permissions matrix."),
      fields: [
        { key: "name", label: tx("名称", "Name"), required: true },
        { key: "description", label: tx("说明", "Description"), type: "textarea" },
        { key: "scope", label: tx("范围", "Scope"), type: "select", required: true, initial: "environment", options: [
          { value: "environment", label: tx("当前环境 · {env}", "Current environment · {env}", { env }) },
          { value: "global", label: tx("全部环境", "All environments") },
        ] },
        { key: "baseline", label: tx("初始权限", "Initial permissions"), type: "select", required: true, initial: "empty", options: [
          { value: "empty", label: tx("仅环境准入", "Environment access only") },
          { value: "view", label: tx("全部资源只读", "Read all resources") },
        ] },
      ],
      submit: tx("创建", "Create"),
    });
    if (!v) return;
    if (API_MODE === "mock") return toast(tx("示例角色已创建", "Sample role created"));
    try {
      if (!env || env === "all" || env !== useEnv.getState().env || !r?.matrix.length) throw new Error(tx("请先选定单个环境并加载角色目录", "Select one environment and load the role catalogue first"));
      const id = `custom-${newIdempotencyKey()}`;
      await api("POST", "/roles", { body: {
        id, name: v.name, description: v.description,
        scopes: [{ kind: v.scope, id: v.scope === "global" ? "*" : env }],
        matrix: r.matrix.map((row) => ({ resource: row.resource, grants: [v.baseline === "view" ? 1 : 0, 0, 0, 0, 0] })),
      } });
      await client.invalidateQueries({ queryKey: ["/roles"] });
      setSel(id);
      toast(tx("角色已创建", "Role created"));
    } catch (e) {
      toast(e instanceof Error ? e.message : tx("创建失败", "Creation failed"), { tone: "danger" });
    }
  };
  const toggle = (ri: number, ci: number) => {
    if (!matrix || !r) return;
    // Agent service accounts may never approve; the approve column is locked for them.
    if (r.kind === "machine" && ci === 3) {
      toast(tx("Agent 服务账号不能被授予“批准”", "Agent service accounts can't be granted Approve"));
      return;
    }
    setMatrix(matrix.map((row, i) => (i === ri ? { ...row, grants: row.grants.map((g, j) => (j === ci ? (g ? 0 : 1) : g)) as RoleDetail["matrix"][number]["grants"] } : row)));
  };
  return (
    <Page
      flush
      ai={false}
      error={!list ? error : undefined}
      errorSource={tx("角色目录", "role catalogue")}
      crumbs={[tx("设置", "Settings"), tx("角色与权限", "Roles & permissions")]}
      actions={
        <>
          {dirty && (
            <Gated reason={noManage}>
              <Btn kind="primary" icon="check" loading={save.isPending} onClick={() => matrix && save.mutate({ matrix }, { onSuccess: () => toast(tx("权限已保存并写入审计", "Permissions saved and audited")), onError: (e) => toast(e.status === 409 || e.status === 412 ? tx("角色刚被其他人修改，已重新读取，请核对后再保存", "Someone just changed this role; reloaded — check and save again") : e.message, { tone: "danger" }) })}>
                {tx("保存权限", "Save permissions")}
              </Btn>
            </Gated>
          )}
          <Gated reason={noManage}>
            <Btn kind={dirty ? "secondary" : "primary"} icon="plus" disabled={API_MODE === "http" && (!r || env === "all")} onClick={create}>
              {tx("新建角色", "New role")}
            </Btn>
          </Gated>
        </>
      }
    >
      <div className="v3-split">
        <div className="side" style={{ width: 280, padding: 12, display: "flex", flexDirection: "column", gap: 12 }}>
          <SearchBox placeholder={tx("搜索角色", "Search roles")} value={q} onChange={setQ} width={256} />
          {kinds.map(([k, label]) => (
            <Section key={k} title={label}>
              <ul className="org-tree">
                {list?.items
                  .filter((x) => x.kind === k && (!q || x.name.includes(q)))
                  .map((x) => (
                    <li key={x.id}>
                      <button type="button" className={cx(sel === x.id && "on")} onClick={() => setSel(x.id)}>
                        <Icon name={k === "machine" ? (x.id === "ci" ? "link" : "cpu") : "shield"} size={14} />
                        <span className="ellipsis grow">{x.name}</span>
                        <Muted size={11.5}>{x.members}</Muted>
                      </button>
                    </li>
                  ))}
              </ul>
            </Section>
          ))}
        </div>
        <div className="main">
          {detailError && !r ? (
            // A role that can't be read says why instead of keeping the skeleton (PA-200).
            <LoadError error={detailError} source={tx("角色目录", "role catalogue")} />
          ) : !r || !matrix ? (
            <div className="v3-content">
              <Skeleton h={300} />
            </div>
          ) : (
            <div className="v3-content anim-fade" style={{ padding: "18px 22px" }} key={r.id}>
              <Row gap={12}>
                <Tile icon={r.kind === "machine" ? "cpu" : "shield"} tone="accent" size={34} />
                <Col gap={2} grow>
                  <Row gap={8}>
                    <h2 style={{ margin: 0, fontSize: 17, fontWeight: 600 }}>{r.name}</h2>
                    <Badge>{kinds.find(([k]) => k === r.kind)?.[1]}</Badge>
                  </Row>
                  <Muted>{r.desc}</Muted>
                </Col>
                <AvatarGroup names={r.member_names} />
                {r.kind !== "machine" && <Gated reason={noManage || noMembers}>
                  <Btn
                    h={28}
                    icon="user"
                    onClick={async () => {
                      const v = await openForm({
                        title: tx("管理「{r}」的成员", "Members of “{r}”", { r: r.name }),
                        desc: tx("当前成员：{m}", "Current members: {m}", { m: r.member_names.join("、") || "—" }),
                        fields: [
                          { key: "add", label: tx("添加成员", "Add a member"), type: "select", required: true, options: API_MODE === "mock" ? lists.people : memberOptions },
                        ],
                        submit: tx("添加", "Add"),
                      });
                      if (!v) return;
                      try {
                        if (API_MODE === "http") {
                          if (env !== useEnv.getState().env || env === "all") throw new Error(tx("环境已变化，请重新选择成员", "The environment changed. Select the member again."));
                          await addMemberRole(env, v.add, r.id);
                          await client.invalidateQueries({ predicate: (query) => /^\/(members|roles)(\/|$)/.test(String(query.queryKey[0])) });
                        }
                        toast(tx("成员已拥有该角色", "The member has this role"));
                      } catch (e) {
                        toast(e instanceof Error ? e.message : tx("角色分配失败", "Role assignment failed"), { tone: "danger" });
                      }
                    }}
                  >
                    {tx("管理成员", "Manage members")}
                  </Btn>
                </Gated>}
              </Row>
              <Row gap={6}>
                <Muted>{tx("范围", "Scope")}</Muted>
                {r.scope.map((s) => (
                  <Badge key={s}>{s}</Badge>
                ))}
                {/* Adding a scope only changed local state and offered sample values; sample data only (PA-204, D52). */}
                {API_MODE === "mock" && <AddChip label={tx("范围", "Scope")} free placeholder="app=payment" options={["app=payment", "app=search", "env=staging"]} />}
              </Row>
              <Tabs
                pad="0"
                value={tab}
                onChange={setTab}
                options={[
                  { value: "perm", label: tx("权限", "Permissions") },
                  { value: "members", label: tx("成员", "Members"), count: r.members },
                  { value: "history", label: tx("变更记录", "History") },
                ]}
              />
              {tab === "perm" ? (
                <Card style={{ overflow: "hidden" }}>
                  <table className="v3-table head-bg perm-table">
                    <thead>
                      <tr>
                        <th style={{ width: "30%" }}>{tx("资源", "Resource")}</th>
                        {r.actions.map((a) => (
                          <th key={a} style={{ textAlign: "center" }}>{a}</th>
                        ))}
                      </tr>
                    </thead>
                    <tbody>
                      {matrix.map((row, ri) => (
                        <tr key={row.resource} style={{ height: 36 }}>
                          <td style={{ fontWeight: 500 }}>{row.resource}</td>
                          {row.grants.map((g, ci) => (
                            <td key={ci} style={{ textAlign: "center" }}>
                              <button type="button" className={cx("perm-cell", g === 2 && "hl", g === 1 && "yes")} aria-pressed={g > 0} aria-label={`${row.resource} · ${r.actions[ci]}`} onClick={() => toggle(ri, ci)}>
                                {g ? <Icon name="check" size={15} /> : "—"}
                              </button>
                            </td>
                          ))}
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </Card>
              ) : (
                <Muted>
                  {tab === "members"
                    ? r.member_names.join("、")
                    : // The sample change stays in the sample; the server keeps no per-role history, only audit rows (PA-207, D52).
                      API_MODE === "mock"
                      ? tx("最近变更：9/20 陈雨 调整了“写操作提案 · 批准”（通行密钥必需）", "Latest: 9/20 Chen Yu changed “Write proposals · Approve” (passkey required)")
                      : tx("平台还没有按角色整理的变更记录。保存权限会写入审计日志，可以在“设置 › 审计日志”里查看。", "No per-role change history is kept yet. Saving permissions writes to the audit log; see Settings › Audit log.")}
                </Muted>
              )}
              <Row gap={8} align="flex-start">
                <Icon name="shield" size={14} color="var(--muted)" style={{ marginTop: 2 }} />
                <Muted>{r.notes}</Muted>
              </Row>
            </div>
          )}
        </div>
      </div>
    </Page>
  );
}
