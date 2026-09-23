import { useRef, useState } from "react";
import { useLocation, useNavigate, useSearchParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus, Play, GitBranch, Box, ArrowRight } from "../../lib/icons";
import { request } from "../../lib/api";
import { useWorkspace } from "../../lib/workspace";
import ScopeFilter, { usePageScope } from "../../components/ScopeFilter";
import { Button, ErrorNotice, Modal } from "../../components/ui";
import PipelineEditor from "./PipelineEditor";
import RunDetails from "./RunDetails";
import ReleasesPage from "./ReleasesPage";
import type { Catalog, Pipeline, Run, Definition, Items } from "./types";
import { displayTime, statusLabel } from "./types";
import "../workspace/workspace.css";
import "./delivery.css";
const blank: Definition = { provider: "native", nodes: [] };
export default function DeliveryPage() {
  const { preview, user, applications } = useWorkspace(),
    scope = usePageScope(),
    location = useLocation(),
    navigate = useNavigate(),
    client = useQueryClient();
  const [params, setParams] = useSearchParams();
  const view = location.pathname.split("/")[2] || "pipelines";
  const catalog = useQuery({
    queryKey: ["delivery", "catalog"],
    queryFn: () => request<Catalog>("/delivery/catalog"),
    enabled: !preview,
  });
  const filter = new URLSearchParams(scope.query);
  const pipes = useQuery({
    queryKey: ["delivery", "pipelines", filter.toString()],
    queryFn: () => request<Items<Pipeline>>(`/delivery/pipelines?${filter}`),
    enabled: !preview && view === "pipelines",
  });
  const runs = useQuery({
    queryKey: ["delivery", "runs", filter.toString()],
    queryFn: () => request<Items<Run>>(`/delivery/runs?${filter}`),
    enabled: !preview && view === "runs",
    refetchInterval: 5000,
  });
  const [editing, setEditing] = useState<{
      pipeline?: Pipeline;
      definition: Definition;
    } | null>(null),
    [start, setStart] = useState<Pipeline | null>(null),
    [external, setExternal] = useState("");
  const [project, setProject] = useState("");
  const [remotePage, setRemotePage] = useState(1);
  const launchKey = useRef<string | null>(null);
  const launch = useMutation({
    mutationFn: () => {
      launchKey.current ??= crypto.randomUUID();
      return request<Run>("/delivery/runs", {
        method: "POST",
        headers: { "Idempotency-Key": launchKey.current },
        body: JSON.stringify({
          pipeline_id: start!.id,
          version: start!.version,
        }),
      });
    },
    onSuccess: (r) => {
      setStart(null);
      launchKey.current = null;
      void client.invalidateQueries({ queryKey: ["delivery"] });
      navigate(`/delivery/runs?run=${r.id}`);
    },
  });
  const remote = useQuery({
    queryKey: ["delivery", "external", project, remotePage],
    queryFn: () => {
      const [s, p] = project.split(":");
      return request<
        Items<{
          id: number;
          ref: string;
          status: string;
          sha: string;
          web_url: string;
        }> & { next_page: number }
      >(`/delivery/gitlab/${s}/projects/${p}/pipelines?page=${remotePage}`);
    },
    enabled: !!project,
  });
  const associate = useMutation({
    mutationFn: (id: number) => {
      const [s, p] = project.split(":");
      return request<Run>(
        `/delivery/gitlab/${s}/projects/${p}/pipelines/${id}/import`,
        { method: "POST" },
      );
    },
    onSuccess: (r) => {
      setExternal("");
      navigate(`/delivery/runs?run=${r.id}`);
    },
  });
  const chooseRun = (id: string) =>
    setParams((p) => {
      p.set("run", id);
      return p;
    });
  const inScope = (p: Pipeline | Run) =>
    !scope.environmentIds.length ||
    scope.environmentIds.includes(p.environment_id);
  const canEdit = user.role !== "viewer" && !preview;
  return (
    <div className="management-page delivery-page">
      <header className="management-heading">
        <div>
          <span className="eyebrow">DELIVERY</span>
          <h1>
            {{
              pipelines: "流水线",
              templates: "流水线模板",
              runs: "运行记录",
              releases: "发布记录",
            }[view] ?? "交付中心"}
          </h1>
          <p>连接构建、变更与恢复，让每次发布都可追溯。</p>
        </div>
        {view === "pipelines" && canEdit && (
          <div className="delivery-toolbar">
            <Button
              variant="outline"
              onClick={() => {
                setExternal("browse");
                setProject("");
              }}
            >
              关联 GitLab 运行
            </Button>
            <Button
              variant="outline"
              onClick={() =>
                setEditing({ definition: { provider: "gitlab", ref: "" } })
              }
            >
              关联 GitLab 流程
            </Button>
            <Button onClick={() => setEditing({ definition: blank })}>
              <Plus size={16} />
              新建流水线
            </Button>
          </div>
        )}
      </header>
      <ScopeFilter />
      {preview && (
        <p className="management-notice">设计预览 · 未连接交付引擎。</p>
      )}
      {catalog.error && <ErrorNotice error={catalog.error} />}
      {catalog.data && !catalog.data.native_enabled && (
        <p className="management-notice">
          原生执行引擎尚未配置。可以先编辑并保存定义，登记 Argo Workflows
          后再运行。
        </p>
      )}
      {editing && catalog.data && (
        <PipelineEditor
          key={
            editing.pipeline
              ? `${editing.pipeline.id}:${editing.pipeline.version}`
              : JSON.stringify(editing.definition)
          }
          pipeline={editing.pipeline}
          definition={editing.definition}
          catalog={catalog.data}
          onClose={() => setEditing(null)}
          onSaved={() => setEditing(null)}
        />
      )}
      {view === "pipelines" && (
        <>
          {pipes.error && <ErrorNotice error={pipes.error} />}
          <div className="card delivery-list">
            {pipes.isPending && !preview ? (
              <p className="management-empty">正在读取流水线…</p>
            ) : !pipes.data?.items?.filter(inScope).length ? (
              <div className="delivery-empty">
                <GitBranch size={30} />
                <h2>从一条可靠的交付流程开始</h2>
                <p>关联现有 GitLab 流程，或从模板创建原生 DAG。</p>
                <Button
                  variant="outline"
                  onClick={() => navigate("/delivery/templates")}
                >
                  浏览模板 <ArrowRight size={15} />
                </Button>
              </div>
            ) : (
              pipes.data.items.filter(inScope).map((p) => (
                <article className="delivery-row" key={p.id}>
                  <div className="delivery-pipeline-icon">
                    <GitBranch size={22} />
                  </div>
                  <div className="grow">
                    <strong>{p.name}</strong>
                    <small>
                      {applications.find((a) => a.id === p.application_id)
                        ?.name ?? p.application_id}{" "}
                      ·{" "}
                      {p.definition.provider === "gitlab"
                        ? "GitLab"
                        : "原生 DAG"}{" "}
                      · v{p.version}
                    </small>
                  </div>
                  <span className="muted">{displayTime(p.created_at)}</span>
                  {canEdit && (
                    <>
                      <Button
                        variant="outline"
                        onClick={() =>
                          setEditing({
                            pipeline: p,
                            definition: structuredClone(p.definition),
                          })
                        }
                      >
                        编辑
                      </Button>
                      <Button
                        variant="outline"
                        disabled={
                          p.definition.provider === "native" &&
                          !catalog.data?.native_enabled
                        }
                        onClick={() => {
                          launch.reset();
                          launchKey.current = null;
                          setStart(p);
                        }}
                      >
                        <Play size={14} />
                        运行
                      </Button>
                    </>
                  )}
                </article>
              ))
            )}
          </div>
        </>
      )}
      {view === "templates" && (
        <div className="delivery-templates">
          {catalog.data?.templates.map((t) => (
            <article className="card" key={t.id}>
              <div className="delivery-pipeline-icon">
                <Box size={24} />
              </div>
              <h2>{t.name}</h2>
              <p>{t.description}</p>
              <small className="muted">
                配置项：{t.required_inputs.join(" · ")}
              </small>
              <Button
                disabled={!canEdit}
                onClick={() =>
                  setEditing({ definition: structuredClone(t.definition) })
                }
              >
                使用并编辑 <ArrowRight size={14} />
              </Button>
            </article>
          ))}
        </div>
      )}
      {view === "runs" && (
        <>
          {runs.error && <ErrorNotice error={runs.error} />}
          <div className="card delivery-list">
            {!runs.data?.items?.filter(inScope).length ? (
              <p className="management-empty">暂无运行记录</p>
            ) : (
              runs.data.items.filter(inScope).map((r) => (
                <button
                  className="delivery-row delivery-row-button"
                  key={r.id}
                  onClick={() => chooseRun(r.id)}
                >
                  <span className={`delivery-status state-${r.status}`}>
                    {statusLabel(r.status)}
                  </span>
                  <div className="grow">
                    <strong>{r.snapshot.name}</strong>
                    <small>
                      {r.provider === "gitlab" ? "GitLab" : "Argo Workflows"} ·
                      v{r.version} ·{" "}
                      {r.projection.commit_sha?.slice(0, 8) || "等待源码信息"}
                    </small>
                  </div>
                  <span>{displayTime(r.created_at)}</span>
                  <ArrowRight size={16} />
                </button>
              ))
            )}
          </div>
          {params.get("run") && (
            <RunDetails
              key={params.get("run")}
              id={params.get("run")!}
              onClose={() =>
                setParams((p) => {
                  p.delete("run");
                  return p;
                })
              }
            />
          )}
        </>
      )}
      {view === "releases" && <ReleasesPage />}
      <Modal
        open={!!start}
        onOpenChange={(v) => {
          if (!v && !launch.isPending) setStart(null);
        }}
        title="运行已保存的流水线"
        description="本次运行使用这个版本的定义与目标，后续编辑不会改变正在执行的任务。"
      >
        <p>
          {start?.name} · v{start?.version}
        </p>
        <p className="muted">
          {start?.application_id} / {start?.environment_id}
        </p>
        {start?.definition.provider === "gitlab" && (
          <p>外部流程将按 GitLab 自身的权限与规则执行。</p>
        )}
        {launch.error && <ErrorNotice error={launch.error} />}
        <Button disabled={launch.isPending} onClick={() => launch.mutate()}>
          {launch.isPending ? "正在提交…" : "确认运行"}
        </Button>
      </Modal>
      <Modal
        open={!!external}
        onOpenChange={(v) => {
          if (!v) setExternal("");
        }}
        title="关联 GitLab 已有运行"
        description="只关联实际执行记录，不转换 GitLab CI 配置。"
      >
        <label>
          已登记项目
          <select
            className="field"
            value={project}
            onChange={(e) => {
              setProject(e.target.value);
              setRemotePage(1);
            }}
          >
            <option value="">选择项目</option>
            {catalog.data?.gitlab_sources.flatMap((s) =>
              s.bindings.map((b) => (
                <option
                  key={`${s.id}:${b.project_id}`}
                  value={`${s.id}:${b.project_id}`}
                >
                  {s.name} · #{b.project_id}
                </option>
              )),
            )}
          </select>
        </label>
        {!catalog.data?.gitlab_sources.length && (
          <p>请先登记 GitLab 连接与应用绑定。</p>
        )}
        {remote.error && <ErrorNotice error={remote.error} />}
        {remote.data?.items.map((r) => (
          <div className="delivery-row" key={r.id}>
            <span>
              #{r.id} · {r.ref} · {statusLabel(r.status)}
            </span>
            <Button
              disabled={associate.isPending}
              variant="outline"
              onClick={() => associate.mutate(r.id)}
            >
              关联
            </Button>
          </div>
        ))}
        <div className="delivery-toolbar">
          <Button
            variant="outline"
            disabled={remotePage === 1 || remote.isFetching}
            onClick={() => setRemotePage((p) => p - 1)}
          >
            上一页
          </Button>
          <span>第 {remotePage} 页</span>
          <Button
            variant="outline"
            disabled={!remote.data?.next_page || remote.isFetching}
            onClick={() => setRemotePage(remote.data!.next_page)}
          >
            下一页
          </Button>
        </div>
        {associate.error && <ErrorNotice error={associate.error} />}
      </Modal>
    </div>
  );
}
