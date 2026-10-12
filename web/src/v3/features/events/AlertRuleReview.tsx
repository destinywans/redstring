import type { Proposal, ProposalAlertRule } from "../../api/types";
import { Brand, Card, CardHead, Code, Col, Icon, KV, LabelChip, Muted, Row, TextLink } from "../../ui/core";
import { useI18n } from "../../i18n";

/**
 * Review content of an alert rule enable or disable (approved D11 board, §11.4): the whole rule (enable only), where
 * it is written and how it is merged and loaded, and the whole file's difference. Approval stays the two-person
 * passkey flow on the right.
 */
export function AlertRuleReviewCards({ p, ar }: { p: Proposal; ar: ProposalAlertRule }) {
  const { tx } = useI18n();
  const repo = ar.repository;
  const lines = ar.change?.lines ?? [];
  const added = lines.filter((l) => l.op === "+").length;
  const removed = lines.filter((l) => l.op === "-").length;
  const project = repo.project_path ?? tx("项目 {id}", "Project {id}", { id: repo.project_id });
  const minutes = Math.round(ar.prometheus.readback_timeout_seconds / 60);
  const kw = 76;
  return (
    <>
      {ar.operation === "enable" && (
        <Card>
          <CardHead title={tx("规则", "Rule")} icon="bell" />
          <Col gap={10} style={{ padding: "14px 16px" }}>
            <KV k={tx("名称", "Name")} kw={kw}>
              <span className="mono">{ar.rule.name}</span>
            </KV>
            <KV k={tx("表达式", "Expression")} kw={kw}>
              <Code style={{ margin: 0, whiteSpace: "pre-wrap", wordBreak: "break-word" }}>{ar.rule.expr}</Code>
            </KV>
            <KV k={tx("持续", "For")} kw={kw}>
              <span className="mono">{ar.rule.for}</span>
            </KV>
            <KV k={tx("标签", "Labels")} kw={kw}>
              <Row gap={6} wrap>
                {Object.entries(ar.rule.labels).map(([k, v]) => (
                  <LabelChip key={k} k={k} v={v} />
                ))}
              </Row>
            </KV>
            {Object.keys(ar.rule.annotations).length > 0 && (
              <KV k={tx("注解", "Annotations")} kw={kw}>
                <Col gap={3}>
                  {Object.entries(ar.rule.annotations).map(([k, v]) => (
                    <span key={k} style={{ fontSize: "var(--fs-sm)" }}>
                      <span className="mono muted">{k}:</span> {v}
                    </span>
                  ))}
                </Col>
              </KV>
            )}
          </Col>
        </Card>
      )}
      <Card>
        <CardHead
          title={
            <Row gap={8}>
              <Brand name="gitlab" size={18} />
              {tx("写到哪里", "Where it's written")}
            </Row>
          }
        />
        <Col gap={10} style={{ padding: "14px 16px" }}>
          <KV k={tx("仓库", "Repository")} kw={kw}>
            {repo.web_url ? (
              <TextLink icon="external" onClick={() => window.open(repo.web_url!, "_blank", "noopener")}>
                {project}
              </TextLink>
            ) : (
              <span className="mono">{project}</span>
            )}
          </KV>
          <KV k={tx("分支", "Branch")} kw={kw}>
            <span>
              <span className="mono">{repo.target_branch}</span>
              {ar.merge.method === "fast_forward" && <Muted> · {tx("只允许 fast-forward 合并", "fast-forward merges only")}</Muted>}
            </span>
          </KV>
          <KV k={tx("文件", "File")} kw={kw}>
            <span className="mono">{repo.path}</span>
          </KV>
          <KV k={tx("规则组", "Rule group")} kw={kw}>
            <span className="mono">{ar.prometheus.group}</span>
          </KV>
          <KV k={tx("基础提交", "Base commit")} kw={kw}>
            <span>
              <span className="mono">{repo.base_sha.slice(0, 7)}</span>
              <Muted> · {tx("批准后 {b} 若有新提交，执行会停下", "if {b} gets a new commit after approval, the execution stops", { b: repo.target_branch })}</Muted>
            </span>
          </KV>
          <KV k={tx("合并与加载", "Merge & load")} kw={kw}>
            <span style={{ fontSize: "var(--fs-sm)" }}>
              {ar.merge.by === "executor" ? tx("两人批准后由执行器自行合并；", "After two approvals the executor merges it itself; ") : tx("两人批准后由人在 GitLab 里合并请求；", "After two approvals a person merges the request in GitLab; ")}
              {ar.operation === "enable"
                ? tx("Prometheus 加载后，{m} 分钟内读回一致才算生效。", "once Prometheus loads it, it counts as in effect only if the readback matches within {m} min.", { m: minutes })
                : tx("Prometheus 重载后，{m} 分钟内读不到这条规则才算停用。", "once Prometheus reloads, it counts as disabled only when the rule is gone within {m} min.", { m: minutes })}
            </span>
          </KV>
        </Col>
      </Card>
      <Card>
        <CardHead
          title={tx("整文件差异", "Whole-file difference")}
          icon="code"
          right={
            ar.change && (
              <Muted size={11.5}>
                {added > 0 && `+${added}`}
                {added > 0 && removed > 0 && " "}
                {removed > 0 && `−${removed}`} {tx("行", "lines")}
              </Muted>
            )
          }
        />
        <Col gap={8} style={{ padding: "14px 16px" }}>
          {ar.change ? (
            <>
              <div className="review-diff">
                <div className="path">{repo.path}</div>
                {lines.map((l, i) => (
                  <div key={i} className={l.op === "-" ? "del" : l.op === "+" ? "add" : ""}>
                    {l.op}
                    {l.text}
                  </div>
                ))}
              </div>
              <Row gap={6} align="flex-start">
                <Icon name="lock" size={12} color="var(--faint)" style={{ marginTop: 2 }} />
                <Muted size={11.5}>
                  {tx("只改这些行，其余字节不变；执行前核对文件摘要 {d}，与批准时不一致就停下。", "Only these lines change and every other byte stays; the executor checks file digest {d} first and stops if it differs from the approved one.", { d: `${ar.change.content_sha256.slice(0, 12)}…` })}
                </Muted>
              </Row>
            </>
          ) : (
            // No difference was recorded with this proposal: the reviewed record (generic content) still stands.
            <>
              <Muted size={12}>{tx("这份提案没有记录整文件差异。下面是提案的审阅内容，批准仍以它和执行前的文件摘要核对为准。", "No whole-file difference was recorded with this proposal. Below is its reviewed content; approval still rests on it and the file digest checked before execution.")}</Muted>
              <div className="review-diff">
                <div className="path">{p.diff.path}</div>
                {p.diff.lines.map((l, i) => (
                  <div key={i} style={{ whiteSpace: "pre-wrap", wordBreak: "break-all" }}>
                    {l}
                  </div>
                ))}
              </div>
            </>
          )}
        </Col>
      </Card>
      {p.impact && (
        <Card>
          <CardHead title={tx("风险与影响", "Risk & impact")} icon="alert" />
          <Col gap={6} style={{ padding: "14px 16px", fontSize: "var(--fs-sm)" }}>
            <span>{p.impact}</span>
          </Col>
        </Card>
      )}
    </>
  );
}
