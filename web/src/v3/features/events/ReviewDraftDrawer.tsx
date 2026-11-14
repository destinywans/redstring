import { useRef, useState } from "react";
import { ApiError, api, newIdempotencyKey, useGetWithEtag } from "../../api/client";
import { serviceApplicationId } from "../../api/ids";
import type { RouteNodeRead } from "../../api/types";
import { useI18n } from "../../i18n";
import { Btn, Field, Input, Muted, Row, Spacer, Toggle } from "../../ui/core";
import { ChipSelect, usePickLists } from "../../ui/demo";
import { MatcherList, type Matcher } from "../../ui/forms";
import { Drawer } from "../../ui/overlays";

type Outcome = { status: string; review: { merge_request_iid: number; url: string } };
type InhibitionPreview = { potentially_suppressed: number; samples?: string[] };
type RouteTree = { root: RouteNodeRead | null };

function receivers(tree?: RouteNodeRead | null): string[] {
  if (!tree) return [];
  return [...new Set([...tree.receiver_refs, ...(tree.children ?? []).flatMap(receivers)])];
}

function safeReviewLink(value: string) {
  try {
    const url = new URL(value);
    return ["https:", "http:"].includes(url.protocol) ? url.href : undefined;
  } catch {
    return undefined;
  }
}

export function ReviewDraftDrawer({ kind, onClose }: { kind: "route" | "inhibition"; onClose: () => void }) {
  const { tx } = useI18n();
  const { services } = usePickLists();
  const route = kind === "route";
  const endpoint = route ? "/notification-routes" : "/inhibitions";
  const version = useGetWithEtag<RouteTree>(endpoint);
  const request = useRef<{ identity: string; key: string } | null>(null);
  const [service, setService] = useState("");
  const [name, setName] = useState("");
  const [receiver, setReceiver] = useState("");
  const [source, setSource] = useState<Matcher[]>([]);
  const [target, setTarget] = useState<Matcher[]>([]);
  const [labels, setLabels] = useState(route ? "alertname, application" : "");
  const [wait, setWait] = useState("10s");
  const [interval, setInterval] = useState("30s");
  const [repeat, setRepeat] = useState("4h");
  const [continued, setContinued] = useState(false);
  const [preview, setPreview] = useState<InhibitionPreview | null>(null);
  const [result, setResult] = useState<Outcome | null>(null);
  const [pending, setPending] = useState(false);
  const [problem, setProblem] = useState("");
  const application = serviceApplicationId(service);
  const selectedLabels = labels.split(",").map((label) => label.trim()).filter(Boolean);
  const routeReceivers = route ? receivers(version.data?.data.root) : [];
  const etag = version.data?.etag;
  const heading = route ? tx("新建通知路由草稿", "New notification route draft") : tx("新建抑制规则草稿", "New inhibition draft");

  const save = async (onlyPreview: boolean) => {
    if (!application || pending) return;
    const draft = route ? {
      application_id: application, name: name.trim(), receiver_ref: receiver,
      matchers: source, group_by: selectedLabels, group_wait: wait.trim(),
      group_interval: interval.trim(), repeat_interval: repeat.trim(), continue: continued,
    } : { application_id: application, source, target, equal: selectedLabels };
    setPending(true);
    setProblem("");
    try {
      if (onlyPreview) {
        setPreview(await api<InhibitionPreview>("POST", endpoint + "/preview", { body: draft }));
        return;
      }
      if (!etag) return;
      const identity = JSON.stringify({ etag, draft });
      if (request.current?.identity !== identity) request.current = { identity, key: newIdempotencyKey() };
      setResult(await api<Outcome>("POST", endpoint, {
        body: draft, headers: { "If-Match": etag }, idempotencyKey: request.current.key,
      }));
    } catch (error) {
      setProblem(error instanceof ApiError ? error.message : tx("操作失败，请重试", "The request failed. Try again."));
    } finally {
      setPending(false);
    }
  };
  const canSubmit = !!application && selectedLabels.length > 0 &&
    (route ? !!name.trim() && routeReceivers.includes(receiver) : source.length > 0 && target.length > 0);

  return <Drawer open onClose={onClose} width={530} title={heading}
    footer={<>
      <Btn kind="ghost" onClick={onClose}>{tx("关闭", "Close")}</Btn>
      <Spacer />
      {!result && <Btn kind="primary" loading={pending} disabled={!canSubmit || !etag}
        onClick={() => void save(false)}>{tx("提交草稿", "Submit draft")}</Btn>}
    </>}>
    <div className="f-drawer-body">
      {result ? <>
        <strong role="status">{tx("草稿已提交，等待 GitLab 审核", "Draft submitted for GitLab review")}</strong>
        <a href={safeReviewLink(result.review.url)} target="_blank" rel="noreferrer">GitLab !{result.review.merge_request_iid}</a>
      </> : <>
        <Muted>{tx("提交会创建 GitLab 合并请求。生效需要另行发布并核验 Alertmanager 配置。", "Submitting opens a GitLab merge request. Activation requires separately publishing and verifying the Alertmanager configuration.")}</Muted>
        <Field label={tx("应用", "Application")}><ChipSelect label={tx("应用", "Application")} value={service || undefined}
          options={services} onChange={(v) => { setService(String(v)); setPreview(null); }} /></Field>
        {route && <>
          <Field label={tx("名称", "Name")}><Input aria-label={tx("名称", "Name")} value={name} onChange={(event) => setName(event.target.value)} /></Field>
          <Field label={tx("接收方", "Receiver")}><ChipSelect label={tx("接收方", "Receiver")} value={receiver || undefined}
            options={routeReceivers.map((ref) => ({ value: ref, label: ref }))} onChange={(v) => setReceiver(String(v))} /></Field>
        </>}
        <Field label={route ? tx("附加匹配条件", "Additional matchers") : tx("来源告警", "Source alert")}>
          <MatcherList items={source} onChange={(items) => { setSource(items); setPreview(null); }} />
        </Field>
        {!route && <Field label={tx("目标告警", "Target alert")} hint={tx("目标必须声明 P2–P4 严重度。", "The target must declare a P2–P4 severity.")}>
          <MatcherList items={target} onChange={(items) => { setTarget(items); setPreview(null); }} />
        </Field>}
        <Field label={route ? tx("分组标签", "Group-by labels") : tx("相同标签", "Equal labels")} hint={tx("使用逗号分隔。", "Separate labels with commas.")}>
          <Input aria-label={route ? tx("分组标签", "Group-by labels") : tx("相同标签", "Equal labels")} value={labels} onChange={(event) => { setLabels(event.target.value); setPreview(null); }} />
        </Field>
        {route ? <>
          <Row gap={8}>
            <Field label={tx("等待", "Wait")}><Input value={wait} onChange={(event) => setWait(event.target.value)} width={100} /></Field>
            <Field label={tx("间隔", "Interval")}><Input value={interval} onChange={(event) => setInterval(event.target.value)} width={100} /></Field>
            <Field label={tx("重复", "Repeat")}><Input value={repeat} onChange={(event) => setRepeat(event.target.value)} width={100} /></Field>
          </Row>
          <Row gap={6}><Toggle label={tx("继续匹配", "Continue matching")} on={continued} onChange={setContinued} />{tx("继续匹配", "Continue matching")}</Row>
        </> : <>
          <Row><Btn loading={pending} disabled={!canSubmit} onClick={() => void save(true)}>{tx("回放过去 7 天", "Replay last 7 days")}</Btn></Row>
          {preview && <div role="status"><Muted>{tx("预计抑制 {n} 次触发", "Would suppress {n} occurrences", { n: preview.potentially_suppressed })}</Muted></div>}
        </>}
        {!etag && !version.isFetching && <Muted>{tx("当前配置版本不可用。", "The current configuration version is unavailable.")}</Muted>}
        {problem && <div role="alert">{problem}</div>}
      </>}
    </div>
  </Drawer>;
}
