import { useState } from "react";
import {
  ReactFlow,
  Background,
  Controls,
  applyNodeChanges,
  type Node,
  type Edge,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import type { Task, Definition } from "./types";
import { Button, ErrorNotice } from "../../components/ui";
import { Plus, X } from "../../lib/icons";

const labels: Record<string, string> = {
  container: "容器任务",
  approval: "人工审批",
  deploy: "发布",
  health: "健康验证",
};
export default function DagEditor({
  value,
  onChange,
  disabled = false,
}: {
  value: Definition;
  onChange: (v: Definition) => void;
  disabled?: boolean;
}) {
  const tasks = value.nodes ?? [];
  const [selected, setSelected] = useState("");
  const [geometry, setGeometry] = useState<
    Record<string, Pick<Node, "position" | "measured">>
  >({});
  const [advanced, setAdvanced] = useState(false),
    [raw, setRaw] = useState(""),
    [error, setError] = useState("");
  const task = tasks.find((n) => n.id === selected);
  const update = (patch: Partial<Task>) =>
    onChange({
      ...value,
      nodes: tasks.map((n) => (n.id === selected ? { ...n, ...patch } : n)),
    });
  const nodes: Node[] = tasks.map((n, i) => ({
    id: n.id,
    position: geometry[n.id]?.position ?? {
      x: (i % 3) * 250,
      y: Math.floor(i / 3) * 145,
    },
    measured: geometry[n.id]?.measured,
    data: {
      label: (
        <div className="delivery-node">
          <small>{labels[n.kind] ?? n.kind}</small>
          <strong>{n.name || n.id}</strong>
          <span>
            {n.kind === "container"
              ? n.image
              : n.kind === "approval"
                ? n.target_id
                : n.kind === "health"
                  ? n.deploy_node_id
                  : n.approval_node_id}
          </span>
        </div>
      ),
    },
    selected: n.id === selected,
    className: `delivery-node-${n.kind}`,
  }));
  const edges: Edge[] = tasks.flatMap((n) =>
    (n.depends_on ?? []).map((d) => ({
      id: `${d.node_id}:${n.id}`,
      source: d.node_id,
      target: n.id,
      label:
        d.on === "failed"
          ? "失败"
          : d.on === "skipped"
            ? "跳过"
            : d.on === "finished"
              ? "结束"
              : "成功",
      type: "smoothstep",
    })),
  );
  function add(kind: string) {
    let i = tasks.length + 1;
    while (tasks.some((n) => n.id === `task-${i}`)) i++;
    const id = `task-${i}`;
    onChange({
      ...value,
      nodes: [
        ...tasks,
        {
          id,
          name: labels[kind],
          kind,
          ...(kind === "container"
            ? {
                image: "alpine:3.23",
                command: ["sh", "-ec"],
                args: ['echo "Configure this task"'],
              }
            : {}),
        },
      ],
    });
    setSelected(id);
    setAdvanced(false);
  }
  return (
    <div className="dag-editor">
      <div className="delivery-toolbar">
        <span>拖动节点调整布局，连接端点定义依赖</span>
        <div>
          {Object.entries(labels).map(([kind, label]) => (
            <Button
              key={kind}
              variant="outline"
              type="button"
              disabled={disabled}
              onClick={() => add(kind)}
            >
              <Plus size={14} />
              {label}
            </Button>
          ))}
        </div>
      </div>
      <div className="dag-columns">
        <div className="dag-canvas" aria-label="流水线 DAG 编辑器">
          <ReactFlow
            nodes={nodes}
            edges={edges}
            fitView
            nodesDraggable={!disabled}
            nodesConnectable={!disabled}
            elementsSelectable={!disabled}
            onNodeClick={(_, n) => {
              setSelected(n.id);
              setAdvanced(false);
              setError("");
            }}
            onNodesChange={(changes) => {
              const next = applyNodeChanges(changes, nodes);
              setGeometry(
                Object.fromEntries(
                  next.map((n) => [
                    n.id,
                    { position: n.position, measured: n.measured },
                  ]),
                ),
              );
              const removed = new Set(
                changes.filter((c) => c.type === "remove").map((c) => c.id),
              );
              if (removed.size && !disabled)
                onChange({
                  ...value,
                  nodes: tasks
                    .filter((n) => !removed.has(n.id))
                    .map((n) => ({
                      ...n,
                      depends_on: n.depends_on?.filter(
                        (d) => !removed.has(d.node_id),
                      ),
                    })),
                });
            }}
            onEdgesDelete={(removed) => {
              if (!disabled)
                onChange({
                  ...value,
                  nodes: tasks.map((n) => ({
                    ...n,
                    depends_on: n.depends_on?.filter(
                      (d) =>
                        !removed.some(
                          (e) => e.source === d.node_id && e.target === n.id,
                        ),
                    ),
                  })),
                });
            }}
            onConnect={(c) => {
              if (!c.source || !c.target || c.source === c.target) return;
              onChange({
                ...value,
                nodes: tasks.map((n) =>
                  n.id === c.target
                    ? {
                        ...n,
                        depends_on: [
                          ...(n.depends_on ?? []).filter(
                            (d) => d.node_id !== c.source,
                          ),
                          { node_id: c.source!, on: "succeeded" },
                        ],
                      }
                    : n,
                ),
              });
            }}
          >
            <Background gap={20} />
            <Controls showInteractive={false} />
          </ReactFlow>
        </div>
        <aside className="dag-inspector">
          {task ? (
            <>
              <div className="delivery-toolbar">
                <strong>节点配置</strong>
                <Button
                  type="button"
                  variant="ghost"
                  disabled={disabled}
                  onClick={() => {
                    onChange({
                      ...value,
                      nodes: tasks
                        .filter((n) => n.id !== task.id)
                        .map((n) => ({
                          ...n,
                          depends_on: n.depends_on?.filter(
                            (d) => d.node_id !== task.id,
                          ),
                        })),
                    });
                    setSelected("");
                  }}
                >
                  <X size={16} />
                  移除
                </Button>
              </div>
              <small className="muted">
                {task.id} · {labels[task.kind]}
              </small>
              <label>
                名称
                <input
                  className="field"
                  disabled={disabled}
                  value={task.name}
                  onChange={(e) => update({ name: e.target.value })}
                />
              </label>
              {task.kind === "container" ? (
                <>
                  <label>
                    任务镜像
                    <input
                      className="field"
                      disabled={disabled}
                      value={task.image ?? ""}
                      onChange={(e) => update({ image: e.target.value })}
                    />
                  </label>
                  <label>
                    启动命令（每行一个参数）
                    <textarea
                      className="field"
                      disabled={disabled}
                      rows={2}
                      value={(task.command ?? []).join("\n")}
                      onChange={(e) =>
                        update({ command: e.target.value.split("\n") })
                      }
                    />
                  </label>
                  {(task.args ?? []).map((arg, index) => (
                    <label key={index}>
                      参数 {index + 1}
                      <textarea
                        className="field"
                        disabled={disabled}
                        rows={arg.includes("\n") ? 7 : 2}
                        value={arg}
                        onChange={(e) =>
                          update({
                            args: task.args?.map((v, i) =>
                              i === index ? e.target.value : v,
                            ),
                          })
                        }
                      />
                      <Button
                        type="button"
                        variant="ghost"
                        disabled={disabled}
                        onClick={() =>
                          update({
                            args: task.args?.filter((_, i) => i !== index),
                          })
                        }
                      >
                        移除此参数
                      </Button>
                    </label>
                  ))}
                  <Button
                    type="button"
                    variant="outline"
                    disabled={disabled}
                    onClick={() => update({ args: [...(task.args ?? []), ""] })}
                  >
                    添加参数
                  </Button>
                  {task.inputs?.map((input, i) => (
                    <label key={input.name}>
                      {input.name}
                      {input.from_node ? (
                        <small>
                          来自 {input.from_node} / {input.output}
                        </small>
                      ) : (
                        <input
                          className="field"
                          disabled={disabled}
                          value={input.value ?? ""}
                          onChange={(e) =>
                            update({
                              inputs: task.inputs?.map((v, j) =>
                                j === i ? { ...v, value: e.target.value } : v,
                              ),
                            })
                          }
                        />
                      )}
                    </label>
                  ))}
                </>
              ) : task.kind === "approval" ? (
                <>
                  <label>
                    产物来源节点
                    <input
                      className="field"
                      disabled={disabled}
                      value={task.source_node_id ?? ""}
                      onChange={(e) =>
                        update({ source_node_id: e.target.value })
                      }
                    />
                  </label>
                  <label>
                    已登记发布目标 ID
                    <input
                      className="field"
                      disabled={disabled}
                      value={task.target_id ?? ""}
                      onChange={(e) => update({ target_id: e.target.value })}
                    />
                  </label>
                  <p className="muted">
                    审批绑定实际产物、源码版本和发布目标。构建任务没有发布权限。
                  </p>
                </>
              ) : (
                <label>
                  {task.kind === "health" ? "发布节点 ID" : "审批节点 ID"}
                  <input
                    className="field"
                    disabled={disabled}
                    value={
                      (task.kind === "health"
                        ? task.deploy_node_id
                        : task.approval_node_id) ?? ""
                    }
                    onChange={(e) =>
                      update(
                        task.kind === "health"
                          ? { deploy_node_id: e.target.value }
                          : { approval_node_id: e.target.value },
                      )
                    }
                  />
                </label>
              )}
              <label>
                汇聚条件
                <select
                  className="field"
                  disabled={disabled || task.kind !== "container"}
                  value={task.join ?? "all"}
                  onChange={(e) => update({ join: e.target.value })}
                >
                  <option value="all">全部指定依赖满足</option>
                  <option value="any">任一指定依赖满足</option>
                  <option value="selected">
                    全部选中分支成功（未选中分支跳过）
                  </option>
                </select>
              </label>
              {task.kind === "container" && (
                <>
                  <label>
                    执行条件
                    <select
                      className="field"
                      disabled={disabled}
                      value={task.when ? "conditional" : "always"}
                      onChange={(e) =>
                        update({
                          when:
                            e.target.value === "always"
                              ? undefined
                              : {
                                  from_node:
                                    task.depends_on?.[0]?.node_id ?? "",
                                  output: "",
                                  operator: "eq",
                                  value: "",
                                },
                        })
                      }
                    >
                      <option value="always">依赖满足后执行</option>
                      <option value="conditional">按上游输出选择分支</option>
                    </select>
                  </label>
                  {task.when && (
                    <>
                      <label>
                        条件来源节点
                        <select
                          className="field"
                          disabled={disabled}
                          value={task.when.from_node}
                          onChange={(e) =>
                            update({
                              when: {
                                ...task.when!,
                                from_node: e.target.value,
                              },
                            })
                          }
                        >
                          <option value="">选择直接上游</option>
                          {task.depends_on?.map((d) => (
                            <option key={d.node_id} value={d.node_id}>
                              {tasks.find((n) => n.id === d.node_id)?.name ??
                                d.node_id}
                            </option>
                          ))}
                        </select>
                      </label>
                      <label>
                        输出名称
                        <input
                          className="field"
                          disabled={disabled}
                          value={task.when.output}
                          onChange={(e) =>
                            update({
                              when: { ...task.when!, output: e.target.value },
                            })
                          }
                        />
                      </label>
                      <label>
                        比较
                        <select
                          className="field"
                          disabled={disabled}
                          value={task.when.operator}
                          onChange={(e) =>
                            update({
                              when: {
                                ...task.when!,
                                operator: e.target.value as "eq" | "ne",
                              },
                            })
                          }
                        >
                          <option value="eq">等于</option>
                          <option value="ne">不等于</option>
                        </select>
                      </label>
                      <label>
                        比较值
                        <input
                          className="field"
                          disabled={disabled}
                          value={task.when.value}
                          onChange={(e) =>
                            update({
                              when: { ...task.when!, value: e.target.value },
                            })
                          }
                        />
                      </label>
                    </>
                  )}
                </>
              )}
              {(task.depends_on ?? []).map((d, i) => (
                <label key={d.node_id}>
                  {d.node_id}
                  <select
                    className="field"
                    disabled={disabled}
                    value={d.on ?? "succeeded"}
                    onChange={(e) =>
                      update({
                        depends_on: task.depends_on?.map((v, j) =>
                          j === i ? { ...v, on: e.target.value } : v,
                        ),
                      })
                    }
                  >
                    <option value="succeeded">成功后继续</option>
                    <option value="failed">失败后继续</option>
                    <option value="skipped">跳过后继续</option>
                    <option value="finished">结束后继续</option>
                  </select>
                </label>
              ))}
              <Button
                type="button"
                variant="outline"
                onClick={() => {
                  setRaw(JSON.stringify(task, null, 2));
                  setAdvanced(!advanced);
                }}
              >
                高级节点配置
              </Button>
              {advanced && (
                <>
                  <textarea
                    className="field"
                    aria-label="节点 JSON"
                    disabled={disabled}
                    rows={12}
                    value={raw}
                    onChange={(e) => setRaw(e.target.value)}
                  />
                  <Button
                    type="button"
                    disabled={disabled}
                    onClick={() => {
                      try {
                        const v = JSON.parse(raw) as Task;
                        if (v.id !== task.id || v.kind !== task.kind)
                          throw Error("请保留节点 ID 和类型");
                        update(v);
                        setError("");
                        setAdvanced(false);
                      } catch (e) {
                        setError(String(e));
                      }
                    }}
                  >
                    应用配置
                  </Button>
                </>
              )}
              {error && <ErrorNotice error={error} />}
            </>
          ) : (
            <p className="management-empty">
              选择一个节点，编辑任务、产物和依赖。服务端保存时检查循环与缺失产物。
            </p>
          )}
        </aside>
      </div>
    </div>
  );
}
