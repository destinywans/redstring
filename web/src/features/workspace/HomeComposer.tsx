import { matchesSearch } from "../../lib/search";
import {
  forwardRef,
  useEffect,
  useImperativeHandle,
  useRef,
  useState,
} from "react";
import { useQuery } from "@tanstack/react-query";
import * as Popover from "@radix-ui/react-popover";
import {
  Activity,
  Box,
  Check,
  ChevronDown,
  Plus,
  Send,
  Sparkles,
  X,
} from "../../lib/icons";
import { post, request } from "../../lib/api";
import type { Application, Items, Model } from "../../lib/types";

type Extension = {
  id: string;
  application_id: string;
  kind: "skill" | "prompt";
  title: string;
  enabled: boolean;
  version: number;
};
type Candidate = {
  id: string;
  label: string;
  kind: "app" | "metric" | "skill" | "prompt" | "action";
  value?: Extension;
  text?: string;
};
export type HomeComposerHandle = {
  focus: () => void;
  prepareContent: () => Promise<string>;
};
type Props = {
  draft: string;
  onDraft: (value: string) => void;
  applicationId: string;
  onApplication: (id: string) => void;
  targetApplicationId: string;
  environmentId: string;
  applications: Application[];
  models: Model[];
  modelId: string;
  onModel: (id: string) => void;
  disabled: boolean;
  sendDisabled: boolean;
  busy: boolean;
  retry: boolean;
  preview: boolean;
  onSend: () => void;
};
const bytes = (text: string) => new TextEncoder().encode(text).length;
const quickActions: Candidate[] = [
  {
    id: "health",
    kind: "action",
    label: "检查环境健康",
    text: "检查当前应用环境的健康状态，汇总异常及需要关注的资源。",
  },
  {
    id: "logs",
    kind: "action",
    label: "查找错误日志",
    text: "查找最近的错误日志，归纳异常模式及关联资源。",
  },
];

const HomeComposer = forwardRef<HomeComposerHandle, Props>(
  function HomeComposer(props, ref) {
    const {
      draft,
      onDraft,
      applicationId,
      onApplication,
      targetApplicationId,
      environmentId,
      applications,
      models,
      modelId,
      onModel,
      disabled,
      sendDisabled,
      busy,
      retry,
      preview,
      onSend,
    } = props;
    const input = useRef<HTMLTextAreaElement>(null);
    const [extensions, setExtensions] = useState<Extension[]>([]);
    const [metrics, setMetrics] = useState<string[]>([]);
    const [notice, setNotice] = useState("");
    const [plusOpen, setPlusOpen] = useState(false);
    const [modelOpen, setModelOpen] = useState(false);
    const [category, setCategory] = useState<
      "all" | "app" | "skill" | "metric"
    >("all");
    const [search, setSearch] = useState("");
    const [trigger, setTrigger] = useState<{
      symbol: string;
      start: number;
      end: number;
      query: string;
    } | null>(null);
    const [active, setActive] = useState(0);
    const composing = useRef(false);
    const scopeKey = `${targetApplicationId}:${environmentId}`;
    const previousScope = useRef(scopeKey);
    useEffect(() => {
      if (previousScope.current !== scopeKey && !disabled) {
        if (
          extensions.some(
            (item) =>
              item.application_id &&
              item.application_id !== targetApplicationId,
          ) ||
          metrics.length
        )
          setNotice(
            "应用或环境已改变，已清除不适用的应用引用及指标查询；通用 Skill 和 Prompt 已保留。",
          );
        setExtensions((value) =>
          value.filter(
            (item) =>
              !item.application_id ||
              item.application_id === targetApplicationId,
          ),
        );
        setMetrics([]);
        setTrigger(null);
        previousScope.current = scopeKey;
      }
    }, [scopeKey, targetApplicationId, disabled, extensions, metrics.length]);
    const source = useQuery({
      queryKey: ["home-composer-extensions", targetApplicationId],
      enabled: !preview,
      queryFn: async ({ signal }) => {
        const results = await Promise.all(
          [
            "scope=workspace",
            ...(targetApplicationId
              ? [`application_id=${encodeURIComponent(targetApplicationId)}`]
              : []),
          ].flatMap((scope) =>
            ["skill", "prompt"].map((kind) =>
              request<Items<Extension>>(
                `/ai-extensions?${scope}&kind=${kind}`,
                { signal },
              ),
            ),
          ),
        );
        return results
          .flatMap((result) => result.items ?? [])
          .filter((item) => item.enabled);
      },
    });
    const telemetry = useQuery({
      queryKey: ["telemetry", targetApplicationId, environmentId, preview],
      enabled: !!targetApplicationId && !!environmentId && !preview,
      queryFn: ({ signal }) =>
        request<{ metric_query_ids: string[] }>(
          `/applications/${encodeURIComponent(targetApplicationId)}/telemetry?environment_id=${encodeURIComponent(environmentId)}`,
          { signal },
        ),
    });
    useImperativeHandle(ref, () => ({
      focus: () => input.current?.focus(),
      prepareContent: async () => {
        if (previousScope.current !== scopeKey)
          throw new Error("应用范围已改变，请重新确认引用后发送。");
        if (
          extensions.some(
            (item) =>
              item.application_id &&
              item.application_id !== targetApplicationId,
          )
        )
          throw new Error("引用与目标应用不一致，请重新选择。");
        let question = draft.trim();
        if (metrics.length)
          question += `\n\n指标查询：${metrics.map((id) => JSON.stringify(id)).join("、")}。请使用 query_metrics 工具查询这些已登记指标；当前未附带指标快照。`;
        let content = question;
        if (extensions.length) {
          if (bytes(question) > 8000)
            throw new Error(
              "使用 Skill 或 Prompt 时，问题正文请控制在 8000 字节内。",
            );
          const composed = await post<{ content: string }>(
            "/ai-extensions/compose",
            {
              application_id: targetApplicationId,
              references: extensions.map(({ id, version }) => ({
                id,
                version,
              })),
              question,
            },
          );
          content = composed.content;
        }
        if (bytes(content) > 65536)
          throw new Error("合并后的消息超过 64 KiB，请精简正文。");
        return content;
      },
    }));
    const candidates: Candidate[] = [
      ...applications.map((app) => ({
        id: app.id,
        label: app.name,
        kind: "app" as const,
      })),
      ...(source.data ?? []).map((item) => ({
        id: item.id,
        label: item.title,
        kind: item.kind,
        value: item,
      })),
      ...(telemetry.data?.metric_query_ids ?? []).map((id) => ({
        id,
        label: id,
        kind: "metric" as const,
      })),
      ...quickActions,
    ];
    const visible = candidates.filter((item) => {
      const allowed = trigger
        ? trigger.symbol === "@"
          ? ["app", "metric"].includes(item.kind)
          : ["skill", "prompt", "action"].includes(item.kind)
        : category === "all" ||
          (category === "skill"
            ? ["skill", "prompt", "action"].includes(item.kind)
            : category === item.kind);
      return allowed && matchesSearch(item.label, trigger?.query ?? search);
    });
    const currentIndex = Math.min(active, Math.max(visible.length - 1, 0));
    const detectTrigger = (value: string, caret: number) => {
      const match = /(?:^|\s)([@/])([^\s@/]*)$/.exec(value.slice(0, caret));
      if (match) {
        setPlusOpen(false);
        setModelOpen(false);
      }
      setTrigger(
        match
          ? {
              symbol: match[1],
              start: caret - match[2].length - 1,
              end: caret,
              query: match[2],
            }
          : null,
      );
      setActive(0);
    };
    const choose = (item: Candidate) => {
      if (disabled) return;
      if (
        (item.kind === "skill" || item.kind === "prompt") &&
        extensions.length >= 8 &&
        !extensions.some((ext) => ext.id === item.id)
      ) {
        setNotice("一次最多引用 8 个 Skill 或 Prompt。");
        return;
      }
      if (item.kind === "app") onApplication(item.id);
      if (item.value)
        setExtensions((value) =>
          value.some((ext) => ext.id === item.id)
            ? value
            : [...value, item.value!],
        );
      if (item.kind === "metric")
        setMetrics((value) =>
          value.includes(item.id) ? value : [...value, item.id],
        );
      const base = trigger
        ? draft.slice(0, trigger.start) + draft.slice(trigger.end)
        : draft;
      onDraft(
        item.text
          ? `${base.trim()}${base.trim() ? "\n" : ""}${item.text}`
          : base,
      );
      setTrigger(null);
      setPlusOpen(false);
      setSearch("");
      requestAnimationFrame(() => input.current?.focus());
    };
    const optionLabel = {
      app: "应用",
      skill: "Skill",
      prompt: "Prompt",
      metric: "指标查询",
      action: "快捷指令",
    };
    const list = (inline: boolean) => (
      <div
        id={inline ? "home-command-list" : "home-add-list"}
        role="listbox"
        aria-label="选择上下文"
        className="home-command-list"
      >
        {visible.map((item, index) => (
          <button
            key={`${item.kind}:${item.id}`}
            id={`home-command-${index}`}
            role="option"
            aria-selected={
              inline
                ? index === currentIndex
                : item.kind === "app"
                  ? applicationId === item.id
                  : extensions.some((ext) => ext.id === item.id) ||
                    metrics.includes(item.id)
            }
            tabIndex={inline ? -1 : 0}
            className={inline && index === currentIndex ? "is-active" : ""}
            type="button"
            onMouseDown={(event) => {
              if (inline) event.preventDefault();
            }}
            onClick={() => choose(item)}
          >
            {item.kind === "app" ? (
              <Box size={17} />
            ) : item.kind === "metric" ? (
              <Activity size={17} />
            ) : (
              <Sparkles size={17} />
            )}
            <span>
              {item.label}
              <small>
                {optionLabel[item.kind]}
                {item.value
                  ? item.value.application_id
                    ? " · 应用专用"
                    : " · 通用"
                  : ""}
              </small>
            </span>
          </button>
        ))}
        {!visible.length && (
          <p>
            {source.isFetching || telemetry.isFetching
              ? "正在读取可用选项…"
              : !targetApplicationId &&
                  (category === "skill" ||
                    category === "metric" ||
                    trigger?.symbol === "/")
                ? "没有匹配的通用指令；选择应用后还可添加应用专用 Skill 和指标。"
                : "没有匹配的选项"}
          </p>
        )}
      </div>
    );
    return (
      <form
        className="home-composer"
        onSubmit={(event) => {
          event.preventDefault();
          onSend();
        }}
      >
        <div className="home-composer-input">
          <textarea
            ref={input}
            aria-label="描述你的工作"
            aria-controls={trigger ? "home-command-list" : undefined}
            aria-expanded={!!trigger}
            aria-autocomplete="list"
            aria-activedescendant={
              trigger && visible.length
                ? `home-command-${currentIndex}`
                : undefined
            }
            value={draft}
            disabled={disabled}
            placeholder="描述问题或任务，输入 / 选择指令、@ 引用应用或指标…"
            onChange={(event) => {
              onDraft(event.target.value);
              if (!composing.current)
                detectTrigger(event.target.value, event.target.selectionStart);
            }}
            onClick={(event) =>
              detectTrigger(
                event.currentTarget.value,
                event.currentTarget.selectionStart,
              )
            }
            onBlur={() => setTrigger(null)}
            onCompositionStart={() => {
              composing.current = true;
              setTrigger(null);
            }}
            onCompositionEnd={(event) => {
              composing.current = false;
              detectTrigger(
                event.currentTarget.value,
                event.currentTarget.selectionStart,
              );
            }}
            onKeyDown={(event) => {
              if (
                composing.current ||
                event.nativeEvent.isComposing ||
                event.keyCode === 229
              )
                return;
              if (trigger) {
                if (event.key === "Escape") {
                  event.preventDefault();
                  setTrigger(null);
                  return;
                }
                if (["ArrowDown", "ArrowUp"].includes(event.key)) {
                  event.preventDefault();
                  setActive(
                    visible.length
                      ? (currentIndex +
                          (event.key === "ArrowDown" ? 1 : -1) +
                          visible.length) %
                          visible.length
                      : 0,
                  );
                  return;
                }
                if (event.key === "Enter" && !event.shiftKey) {
                  event.preventDefault();
                  if (visible[currentIndex]) choose(visible[currentIndex]);
                  return;
                }
              }
              if ((event.metaKey || event.ctrlKey) && event.key === "Enter") {
                event.preventDefault();
                if (!sendDisabled) onSend();
              }
            }}
          />
          {trigger && <div className="home-command-popup">{list(true)}</div>}
        </div>
        <div className="home-context-chips" aria-label="本次消息的上下文">
          {applicationId && (
            <span>
              <Box size={14} />
              {applications.find((app) => app.id === applicationId)?.name ||
                "当前应用不可用"}
              <button
                disabled={disabled}
                type="button"
                aria-label="移除应用"
                onClick={() => onApplication("")}
              >
                <X size={13} />
              </button>
            </span>
          )}
          {extensions.map((item) => (
            <span key={item.id}>
              <Sparkles size={14} />
              {item.title}
              <button
                disabled={disabled}
                type="button"
                aria-label={`移除 ${item.title}`}
                onClick={() =>
                  setExtensions((value) =>
                    value.filter((ext) => ext.id !== item.id),
                  )
                }
              >
                <X size={13} />
              </button>
            </span>
          ))}
          {metrics.map((id) => (
            <span key={id}>
              <Activity size={14} />
              指标查询 · {id}
              <button
                disabled={disabled}
                type="button"
                aria-label={`移除指标 ${id}`}
                onClick={() =>
                  setMetrics((value) => value.filter((metric) => metric !== id))
                }
              >
                <X size={13} />
              </button>
            </span>
          ))}
        </div>
        <div className="home-composer-toolbar">
          <Popover.Root
            open={plusOpen}
            onOpenChange={(open) => {
              setPlusOpen(open);
              setModelOpen(false);
              setTrigger(null);
              if (open) {
                setCategory("all");
                setSearch("");
              }
            }}
          >
            <Popover.Trigger asChild>
              <button
                className="home-add-button"
                type="button"
                disabled={disabled}
                aria-label="添加上下文"
                title="添加上下文"
              >
                <Plus size={23} />
              </button>
            </Popover.Trigger>
            <Popover.Portal>
              <Popover.Content
                className="home-add-popup"
                side="top"
                align="start"
                sideOffset={10}
                collisionPadding={12}
              >
                <div className="home-add-tabs" aria-label="上下文类型">
                  {(
                    [
                      ["all", "全部"],
                      ["app", "应用"],
                      ["skill", "Skills / Prompt"],
                      ["metric", "指标"],
                    ] as const
                  ).map(([key, label]) => (
                    <button
                      type="button"
                      aria-pressed={category === key}
                      key={key}
                      onClick={() => setCategory(key)}
                    >
                      {label}
                    </button>
                  ))}
                </div>
                <input
                  aria-label="搜索可用上下文"
                  placeholder="搜索应用、Skill 或指标…"
                  value={search}
                  onChange={(event) => setSearch(event.target.value)}
                  onKeyDown={(event) => {
                    if (["ArrowDown", "ArrowUp"].includes(event.key)) {
                      event.preventDefault();
                      event.currentTarget.parentElement
                        ?.querySelector<HTMLButtonElement>('[role="option"]')
                        ?.focus();
                    }
                  }}
                />
                <div
                  onKeyDown={(event) => {
                    if (
                      !["ArrowDown", "ArrowUp", "Home", "End"].includes(
                        event.key,
                      )
                    )
                      return;
                    const options = Array.from(
                      event.currentTarget.querySelectorAll<HTMLButtonElement>(
                        '[role="option"]',
                      ),
                    );
                    const at = options.indexOf(
                      event.target as HTMLButtonElement,
                    );
                    if (at < 0) return;
                    event.preventDefault();
                    options[
                      event.key === "Home"
                        ? 0
                        : event.key === "End"
                          ? options.length - 1
                          : (at +
                              (event.key === "ArrowDown" ? 1 : -1) +
                              options.length) %
                            options.length
                    ]?.focus();
                  }}
                >
                  {list(false)}
                </div>
                {(source.error || telemetry.error) && (
                  <p role="status">部分选项读取失败，请稍后重试。</p>
                )}
              </Popover.Content>
            </Popover.Portal>
          </Popover.Root>
          <div className="home-composer-send-group">
            <Popover.Root
              open={modelOpen}
              onOpenChange={(open) => {
                setModelOpen(open);
                setPlusOpen(false);
                setTrigger(null);
              }}
            >
              <Popover.Trigger asChild>
                <button
                  type="button"
                  className="home-model-button"
                  disabled={disabled || !models.length}
                  aria-label="选择本轮模型"
                >
                  {modelId
                    ? models.find((model) => model.id === modelId)?.name ||
                      "模型不可用"
                    : models.length
                      ? "默认模型"
                      : "暂无可用模型"}
                  <ChevronDown size={14} />
                </button>
              </Popover.Trigger>
              <Popover.Portal>
                <Popover.Content
                  className="home-model-popup"
                  align="end"
                  side="top"
                  sideOffset={10}
                  collisionPadding={12}
                >
                  <p>本轮模型 · 后续可切换</p>
                  <div
                    role="listbox"
                    aria-label="可用模型"
                    onKeyDown={(event) => {
                      if (
                        !["ArrowDown", "ArrowUp", "Home", "End"].includes(
                          event.key,
                        )
                      )
                        return;
                      const options = Array.from(
                        event.currentTarget.querySelectorAll<HTMLButtonElement>(
                          "button",
                        ),
                      );
                      const at = options.indexOf(
                        event.target as HTMLButtonElement,
                      );
                      if (at < 0) return;
                      event.preventDefault();
                      options[
                        event.key === "Home"
                          ? 0
                          : event.key === "End"
                            ? options.length - 1
                            : (at +
                                (event.key === "ArrowDown" ? 1 : -1) +
                                options.length) %
                              options.length
                      ]?.focus();
                    }}
                  >
                    {[
                      { id: "", name: "默认模型", provider: models[0]?.name },
                      ...models,
                    ].map((model) => (
                      <button
                        type="button"
                        key={model.id}
                        role="option"
                        aria-selected={modelId === model.id}
                        onClick={() => {
                          onModel(model.id);
                          setModelOpen(false);
                        }}
                      >
                        <span>
                          {model.name}
                          <small>{model.provider}</small>
                        </span>
                        {modelId === model.id && <Check size={16} />}
                      </button>
                    ))}
                  </div>
                </Popover.Content>
              </Popover.Portal>
            </Popover.Root>
            <button
              className="home-start-button"
              type="submit"
              disabled={sendDisabled}
              aria-label={busy ? "正在发送" : retry ? "重试发送" : "发送消息"}
              title={
                busy
                  ? "正在发送"
                  : retry
                    ? "重试发送"
                    : "发送 · ⌘ / Ctrl + Enter"
              }
            >
              <Send size={20} />
            </button>
          </div>
        </div>
        {notice && (
          <p className="home-context-notice" role="status">
            {notice}
          </p>
        )}
      </form>
    );
  },
);
export default HomeComposer;
