import { useEffect, useState, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Page } from "../../app/Shell";
import { useGet, useSend } from "../../api/client";
import type { ModelChoice } from "../../api/types";
import { Btn, Col, Input, Muted, Panel, Segmented, Skeleton, Strong, TextLink, Toggle } from "../../ui/core";
import { toast } from "../../ui/overlays";
import { ModelPicker } from "../../ui/ModelPicker";
import { useI18n } from "../../i18n";
import { SettingsHead } from "./common";
import "./settings.css";

type AiPrefs = { default: ModelChoice; canvas_inherit: boolean; show_reasoning: "expanded" | "collapsed" | "hidden"; auto_context: boolean; memory: boolean; budget_warn_tokens: number };

function Line({ title, desc, children }: { title: ReactNode; desc: ReactNode; children: ReactNode }) {
  return (
    <li>
      <Col gap={0} grow>
        <Strong>{title}</Strong>
        <Muted size={11.5}>{desc}</Muted>
      </Col>
      {children}
    </li>
  );
}

/** Personal AI defaults. Which models exist and who may use them is admin territory (Integrations › Models). */
export default function AiPrefsPage() {
  const { tx } = useI18n();
  const qc = useQueryClient();
  const { data } = useGet<AiPrefs>("/me/ai-preferences");
  const [f, setF] = useState<AiPrefs | null>(null);
  const save = useSend<AiPrefs>("PUT", "/me/ai-preferences");
  useEffect(() => {
    if (data) setF(structuredClone(data));
  }, [data]);
  const dirty = !!data && !!f && JSON.stringify(data) !== JSON.stringify(f);
  const up = (patch: Partial<AiPrefs>) => setF((x) => (x ? { ...x, ...patch } : x));
  const onSave = () =>
    f &&
    save.mutate(f, {
      onSuccess: () => {
        qc.invalidateQueries({ predicate: (q) => typeof q.queryKey[0] === "string" && (q.queryKey[0] as string).startsWith("/me/ai") || q.queryKey[0] === "/models" });
        toast(tx("AI 偏好已保存，新对话开始生效", "AI preferences saved; new conversations use them"));
      },
    });
  return (
    <Page
      ai={false}
      bodyClass="set-narrow"
      crumbs={[tx("设置", "Settings"), tx("模型与 AI", "Models & AI")]}
      actions={
        <Btn kind="primary" icon="check" disabled={!dirty} loading={save.isPending} onClick={onSave}>
          {tx("保存", "Save")}
        </Btn>
      }
    >
      <div className="v3-content anim-fade">
        <SettingsHead
          title={tx("模型与 AI", "Models & AI")}
          desc={
            <>
              {tx("你自己的默认值，每次对话仍可在输入框里按轮切换。可用的模型由管理员在", "Your own defaults; you can still switch per turn in the composer. Available models are managed by admins in ")}
              <TextLink to="/integrations/models">{tx("集成中心 › 模型", "Integrations › Models")}</TextLink>
              {tx("配置。", ".")}
            </>
          }
        />
        {!f ? (
          <Skeleton h={320} />
        ) : (
          <>
            <Panel title={tx("默认模型", "Default model")}>
              <ul className="set-list">
                <Line title={tx("新对话与画布节点", "New conversations and canvas nodes")} desc={tx("推理强度越高越慢、成本越高", "Higher effort is slower and costs more")}>
                  <ModelPicker value={f.default} onChange={(c) => up({ default: c })} />
                </Line>
                <Line title={tx("画布子分支继承父节点的模型", "Canvas branches inherit the parent's model")} desc={tx("关闭后，新分支使用上面的默认模型", "When off, new branches use the default above")}>
                  <Toggle on={f.canvas_inherit} onChange={(v) => up({ canvas_inherit: v })} label={tx("继承父节点模型", "Inherit parent model")} />
                </Line>
              </ul>
            </Panel>
            <Panel title={tx("回复", "Replies")}>
              <ul className="set-list">
                <Line title={tx("推理过程", "Reasoning")} desc={tx("模型返回的思考摘要如何显示", "How the model's reasoning summary is shown")}>
                  <Segmented
                    label={tx("推理过程", "Reasoning")}
                    value={f.show_reasoning}
                    onChange={(v) => up({ show_reasoning: v })}
                    options={[{ value: "expanded", label: tx("展开", "Expanded") }, { value: "collapsed", label: tx("折叠", "Collapsed") }, { value: "hidden", label: tx("隐藏", "Hidden") }]}
                  />
                </Line>
                <Line title={tx("自动带入当前视图", "Include the current view")} desc={tx("从指标、链路、日志页发起提问时，自动附上当前视图", "Attach the current view when asking from metrics, traces or logs")}>
                  <Toggle on={f.auto_context} onChange={(v) => up({ auto_context: v })} label={tx("自动带入当前视图", "Include the current view")} />
                </Line>
                <Line title={tx("使用记忆", "Use memory")} desc={tx("允许召回过往调查、复盘和 Runbook", "Recall past investigations, postmortems and runbooks")}>
                  <Toggle on={f.memory} onChange={(v) => up({ memory: v })} label={tx("使用记忆", "Use memory")} />
                </Line>
                <Line title={tx("单轮用量提醒", "Per-turn usage warning")} desc={tx("单轮超过这个 token 数时提醒", "Warn when a turn uses more tokens than this")}>
                  <Input width={140} mono value={String(f.budget_warn_tokens)} onChange={(e) => up({ budget_warn_tokens: Number(e.target.value.replace(/\D/g, "")) || 0 })} right={<Muted>tokens</Muted>} />
                </Line>
              </ul>
            </Panel>
            <Muted size={11.5}>{tx("无论如何设置，AI 都只读取证；写操作一律生成提案，经人工批准后由执行器执行。", "Whatever you choose, the AI only reads; every write becomes a proposal that a person approves and an executor runs.")}</Muted>
          </>
        )}
      </div>
    </Page>
  );
}
