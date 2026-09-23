import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Button, ErrorNotice } from "../../components/ui";
import { post } from "../../lib/api";
import { useWorkspace } from "../../lib/workspace";
import DagEditor from "./DagEditor";
import type { Pipeline, Definition, Catalog } from "./types";
export default function PipelineEditor({
  pipeline,
  definition,
  catalog,
  onSaved,
  onClose,
}: {
  pipeline?: Pipeline;
  definition: Definition;
  catalog: Catalog;
  onSaved: (p: Pipeline) => void;
  onClose: () => void;
}) {
  const { applications, notify } = useWorkspace();
  const client = useQueryClient();
  const [name, setName] = useState(pipeline?.name ?? ""),
    [app, setApp] = useState(pipeline?.application_id ?? ""),
    [env, setEnv] = useState(pipeline?.environment_id ?? ""),
    [value, setValue] = useState(definition);
  const selectedApp = applications.find((a) => a.id === app);
  const save = useMutation({
    mutationFn: () =>
      post<Pipeline>(
        pipeline
          ? `/delivery/pipelines/${pipeline.id}/versions`
          : "/delivery/pipelines",
        {
          application_id: app,
          environment_id: env,
          name,
          definition: value,
          expected_version: pipeline?.version ?? 0,
        },
      ),
    onSuccess: (p) => {
      void client.invalidateQueries({ queryKey: ["delivery"] });
      notify(`已保存版本 v${p.version}`);
      onSaved(p);
    },
  });
  return (
    <section className="card delivery-editor">
      <header className="delivery-toolbar">
        <div>
          <h2>{pipeline ? "编辑流水线" : "创建流水线"}</h2>
          <small className="muted">
            {value.provider === "gitlab"
              ? "关联外部项目，定义与执行仍由 GitLab 管理。"
              : "每次保存生成不可变版本，运行时冻结版本与目标。"}
          </small>
        </div>
        <Button variant="ghost" onClick={onClose} disabled={save.isPending}>
          关闭编辑
        </Button>
      </header>
      <div className="delivery-form">
        <label>
          流水线名称
          <input
            className="field"
            value={name}
            onChange={(e) => setName(e.target.value)}
            maxLength={200}
          />
        </label>
        <label>
          应用
          <select
            className="field"
            disabled={!!pipeline}
            value={app}
            onChange={(e) => {
              setApp(e.target.value);
              setEnv("");
            }}
          >
            <option value="">选择应用</option>
            {applications.map((a) => (
              <option key={a.id} value={a.id}>
                {a.name}
              </option>
            ))}
          </select>
        </label>
        <label>
          环境
          <select
            className="field"
            disabled={!!pipeline}
            value={env}
            onChange={(e) => setEnv(e.target.value)}
          >
            <option value="">选择环境</option>
            {selectedApp?.environments
              ?.filter((e) => !e.archived)
              .map((e) => (
                <option key={e.id} value={e.id}>
                  {e.name}
                </option>
              ))}
          </select>
        </label>
      </div>
      {value.provider === "native" ? (
        <DagEditor
          value={value}
          onChange={setValue}
          disabled={save.isPending}
        />
      ) : (
        <div className="delivery-form">
          <label>
            GitLab 项目绑定
            <select
              className="field"
              value={`${value.source_id ?? ""}:${value.project_id ?? ""}`}
              onChange={(e) => {
                const [source, id] = e.target.value.split(":");
                const b = catalog.gitlab_sources
                  .find((s) => s.id === source)
                  ?.bindings.find((b) => b.project_id === Number(id));
                setValue({
                  ...value,
                  source_id: source,
                  project_id: Number(id),
                  ref: "",
                });
                if (b) {
                  setApp(b.application_id);
                  setEnv(b.environment_id);
                }
              }}
            >
              <option value=":">选择已登记的项目</option>
              {catalog.gitlab_sources.flatMap((s) =>
                s.bindings.map((b) => (
                  <option
                    key={`${s.id}:${b.project_id}`}
                    value={`${s.id}:${b.project_id}`}
                  >
                    {s.name} · 项目 #{b.project_id}
                  </option>
                )),
              )}
            </select>
          </label>
          <label>
            分支 / Tag
            <input
              className="field"
              value={value.ref ?? ""}
              onChange={(e) => setValue({ ...value, ref: e.target.value })}
              placeholder="登记允许的分支或 Tag"
            />
          </label>
        </div>
      )}
      {save.error && <ErrorNotice error={save.error} />}
      <footer className="delivery-toolbar">
        <span className="muted">保存不触发执行</span>
        <Button
          disabled={save.isPending || !name.trim() || !app || !env}
          onClick={() => save.mutate()}
        >
          {save.isPending
            ? "正在保存…"
            : pipeline
              ? "保存新版本"
              : "保存流水线"}
        </Button>
      </footer>
    </section>
  );
}
