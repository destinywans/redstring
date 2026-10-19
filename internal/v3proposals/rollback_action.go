package v3proposals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/store"
	"github.com/destinywans/redstring/internal/v3alertmanager"
	"github.com/destinywans/redstring/internal/v3kube"
	"github.com/jackc/pgx/v5"
)

// RollbackActionKind is a Kubernetes Deployment rollback (PA-08).
const RollbackActionKind = "kubernetes.deployment.rollback"

// RollbackVerification is the reviewed recovery check copied from the
// catalog into the action, so the approvers approve this exact plan.
type RollbackVerification struct {
	ConnectorID        string  `json:"connector_id"`
	ResourceID         string  `json:"resource_id"`
	RequestRateQueryID string  `json:"request_rate_query_id"`
	ErrorRatioQueryID  string  `json:"error_ratio_query_id"`
	SampleAgeQueryID   string  `json:"sample_age_query_id"`
	MaxErrorRatio      float64 `json:"max_error_ratio"`
	MinRequestRate     float64 `json:"min_request_rate"`
}

// RollbackAction is the immutable action snapshot of a rollback proposal.
type RollbackAction struct {
	SchemaVersion          int                  `json:"schema_version"`
	Kind                   string               `json:"kind"`
	ActorID                string               `json:"actor_id"`
	EnvironmentID          string               `json:"environment_id"`
	ApplicationID          string               `json:"application_id"`
	OwnerTeamID            string               `json:"owner_team_id"`
	InvestigationID        string               `json:"investigation_id"`
	InvestigationUpdatedAt time.Time            `json:"investigation_updated_at"`
	ServiceID              string               `json:"service_id"`
	ConnectorID            string               `json:"connector_id"`
	TargetID               string               `json:"target_id"`
	ResourceID             string               `json:"resource_id"`
	Namespace              string               `json:"namespace"`
	Deployment             string               `json:"deployment"`
	UID                    string               `json:"uid"`
	FromRevision           string               `json:"from_revision"`
	ExpectedTemplateDigest string               `json:"expected_template_digest"`
	ExpectedSpecDigest     string               `json:"expected_spec_digest"`
	TargetRevision         string               `json:"target_revision"`
	TargetTemplate         json.RawMessage      `json:"target_template"`
	TargetTemplateDigest   string               `json:"target_template_digest"`
	Containers             []v3kube.Container   `json:"containers"`
	Changes                []v3kube.Change      `json:"changes"`
	ObservationSeconds     int                  `json:"observation_seconds"`
	Verification           RollbackVerification `json:"verification"`
	Reason                 string               `json:"reason"`
	IdempotencyKey         string               `json:"idempotency_key"`
	RequestHash            string               `json:"request_hash"`
	// BindingFingerprint covers the connector, target, catalog scope and
	// write approval the proposal was built from; the executor recomputes it.
	BindingFingerprint string `json:"binding_fingerprint"`
}

var rollbackDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)
var rollbackName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
var rollbackRevision = regexp.MustCompile(`^[1-9][0-9]{0,9}$`)

// Valid checks the snapshot's shape and internal consistency.
func (a RollbackAction) Valid() bool {
	v := a.Verification
	return a.SchemaVersion == 1 && a.Kind == RollbackActionKind && bounded(a.ActorID, 128) &&
		bounded(a.EnvironmentID, 128) && a.EnvironmentID != "all" && bounded(a.ApplicationID, 128) &&
		bounded(a.InvestigationID, 128) && !a.InvestigationUpdatedAt.IsZero() && bounded(a.ServiceID, 1024) &&
		bounded(a.ConnectorID, 128) && bounded(a.TargetID, 128) && bounded(a.ResourceID, 128) &&
		rollbackName.MatchString(a.Namespace) && rollbackName.MatchString(a.Deployment) && bounded(a.UID, 128) &&
		rollbackRevision.MatchString(a.FromRevision) && rollbackRevision.MatchString(a.TargetRevision) &&
		a.FromRevision != a.TargetRevision && rollbackDigest.MatchString(a.ExpectedTemplateDigest) &&
		rollbackDigest.MatchString(a.ExpectedSpecDigest) && rollbackDigest.MatchString(a.TargetTemplateDigest) &&
		a.TargetTemplateDigest != a.ExpectedTemplateDigest && v3kube.Digest(a.TargetTemplate) == a.TargetTemplateDigest &&
		len(a.Containers) > 0 && len(a.Containers) <= 50 && len(a.Changes) <= 50 &&
		a.ObservationSeconds >= 60 && a.ObservationSeconds <= 3600 && bounded(v.ConnectorID, 128) &&
		bounded(v.ResourceID, 128) && bounded(v.RequestRateQueryID, 128) && bounded(v.ErrorRatioQueryID, 128) &&
		bounded(v.SampleAgeQueryID, 128) && v.MaxErrorRatio > 0 && v.MaxErrorRatio <= 1 &&
		!math.IsNaN(v.MinRequestRate) && v.MinRequestRate >= 0 && !math.IsInf(v.MinRequestRate, 0) &&
		bounded(a.Reason, 2000) && silenceProposalKey.MatchString(a.IdempotencyKey) &&
		rollbackDigest.MatchString(a.RequestHash) && rollbackDigest.MatchString(a.BindingFingerprint)
}

// RollbackRequestHash identifies a request for idempotent replay: the same
// key with the same request returns the same proposal.
func RollbackRequestHash(actorID, investigationID, serviceID, targetRevision, expectedTemplateDigest, reason, key string) string {
	raw, _ := canonicalProposalJSON(map[string]string{"actor_id": actorID, "investigation_id": investigationID,
		"service_id": serviceID, "target_revision": targetRevision, "expected_template_digest": expectedTemplateDigest,
		"reason": reason, "idempotency_key": key})
	return proposalDigest("aiops.v3.rollback.proposal.request.v1", raw)
}

// ErrIdempotencyConflict: the key was used for a different request.
var ErrIdempotencyConflict = errors.New("idempotency key reused with a different request")

// LookupRollbackReplay finds the proposal an earlier request with this key
// created, so a replay answers the same proposal even after the Deployment
// has changed (for example because the rollback ran).
func LookupRollbackReplay(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, actorID, key, requestHash string) (string, bool, error) {
	var id, hash string
	err := q.QueryRow(ctx, `SELECT proposal_id,request_hash FROM v3_proposal_actions WHERE creator_id=$1 AND idempotency_key=$2`,
		actorID, key).Scan(&id, &hash)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, err
	case hash != requestHash:
		return "", false, ErrIdempotencyConflict
	}
	return id, true, nil
}

// WritePendingRollbackWithTx writes the review and the immutable action in
// the caller's transaction. The caller has resolved and authorized the
// target. It returns the existing proposal for a replayed request.
func (s *Service) WritePendingRollbackWithTx(ctx context.Context, tx pgx.Tx, actor core.User, a RollbackAction,
	english bool) (string, bool, error) {
	if s == nil || tx == nil || actor.ID == "" || actor.Disabled || actor.ID != a.ActorID || !a.Valid() {
		return "", false, ErrInvalidProposal
	}
	var existingID, existingHash string
	err := tx.QueryRow(ctx, `SELECT proposal_id,request_hash FROM v3_proposal_actions
 WHERE creator_id=$1 AND idempotency_key=$2 FOR SHARE`, actor.ID, a.IdempotencyKey).Scan(&existingID, &existingHash)
	if err == nil {
		if existingHash != a.RequestHash {
			return "", false, ErrIdempotencyConflict
		}
		return existingID, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", false, err
	}
	var investigationUpdatedAt time.Time
	err = tx.QueryRow(ctx, `SELECT i.updated_at FROM investigations i
 JOIN application_environments e ON e.id=i.environment_id AND e.application_id=i.application_id
 WHERE i.id=$1 AND i.application_id=$2 AND i.status<>'closed'
   AND e.key=$3 AND NOT e.archived FOR SHARE OF i`, a.InvestigationID, a.ApplicationID, a.EnvironmentID).Scan(&investigationUpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, store.ErrNotFound
	}
	if err != nil {
		return "", false, err
	}
	if !investigationUpdatedAt.Equal(a.InvestigationUpdatedAt) {
		return "", false, store.ErrConflict
	}
	content := rollbackReviewContent(a, english)
	if !validContent(content) {
		return "", false, ErrInvalidProposal
	}
	actionRaw, err := canonicalProposalJSON(a)
	if err != nil {
		return "", false, err
	}
	contentRaw, err := canonicalProposalJSON(content)
	if err != nil {
		return "", false, err
	}
	actionDigest := proposalDigest("aiops.v3.proposal.action.v1", actionRaw)
	contentDigest := proposalDigest("aiops.v3.proposal.review.v1", contentRaw)
	proposalID := store.ID()
	if _, err = tx.Exec(ctx, `INSERT INTO v3_proposals
 (id,investigation_id,application_id,environment_id,content,created_by,creator_kind)
 VALUES($1,$2,$3,$4,$5::jsonb,$6,'user')`, proposalID, a.InvestigationID, a.ApplicationID, a.EnvironmentID,
		contentRaw, actor.ID); err != nil {
		return "", false, err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO v3_proposal_actions
 (proposal_id,action_kind,schema_version,action,action_digest,review_content_digest,request_hash,
  binding_fingerprint,creator_id,environment_id,application_id,investigation_id,
  investigation_updated_at,idempotency_key,source_kind)
 VALUES($1,$2,1,$3::jsonb,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
 ON CONFLICT DO NOTHING`, proposalID, RollbackActionKind, actionRaw, actionDigest, contentDigest, a.RequestHash,
		a.BindingFingerprint, actor.ID, a.EnvironmentID, a.ApplicationID, a.InvestigationID, investigationUpdatedAt,
		a.IdempotencyKey, silenceProposalSource)
	if err != nil {
		return "", false, err
	}
	if tag.RowsAffected() != 1 {
		return "", false, store.ErrConflict
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,actor_kind,application_id,action,target,outcome,changes)
 VALUES($1,'user',$2,'v3.proposal.rollback_requested',$3,'success',
 jsonb_build_object('environment_id',$4::text,'investigation_id',$5::text,'namespace',$6::text,
 'deployment',$7::text,'from_revision',$8::text,'target_revision',$9::text,
 'action_digest',$10::text,'review_content_digest',$11::text))`,
		actor.ID, a.ApplicationID, proposalID, a.EnvironmentID, a.InvestigationID, a.Namespace, a.Deployment,
		a.FromRevision, a.TargetRevision, actionDigest, contentDigest)
	if err != nil {
		return "", false, err
	}
	return proposalID, false, nil
}

func pick(english bool, zh, en string) string {
	if english {
		return en
	}
	return zh
}

func rollbackChangeLine(c v3kube.Change, english bool) string {
	text := func(p *string) string {
		if p == nil {
			return pick(english, "（无）", "(none)")
		}
		value := *p
		if len([]rune(value)) > 160 {
			value = string([]rune(value)[:160]) + "…"
		}
		return value
	}
	switch {
	case c.Field == "env" && c.ValueHidden:
		return fmt.Sprintf("%s env %s: %s", c.Container, *c.Name, pick(english, "引用 Secret/ConfigMap 的值已变化（不显示）", "value from Secret/ConfigMap changed (hidden)"))
	case c.Field == "env":
		return fmt.Sprintf("%s env %s: %s → %s", c.Container, *c.Name, text(c.From), text(c.To))
	case c.Field == "other" && c.Container == "":
		return pick(english, "Pod 规格的其他字段有变化", "Other Pod spec fields change")
	case c.Field == "other":
		return fmt.Sprintf("%s: %s", c.Container, pick(english, "其他容器字段有变化", "other container fields change"))
	default:
		return fmt.Sprintf("%s %s: %s → %s", c.Container, c.Field, text(c.From), text(c.To))
	}
}

func rollbackReviewContent(a RollbackAction, english bool) Content {
	lines := make([]string, 0, len(a.Changes)+1)
	for _, c := range a.Changes {
		lines = append(lines, rollbackChangeLine(c, english))
	}
	if len(lines) == 0 {
		lines = append(lines, pick(english, "目标修订与当前修订的 Pod 模板相同", "The target revision has the same Pod template"))
	}
	images := make([]string, 0, len(a.Containers))
	for _, c := range a.Containers {
		images = append(images, c.Name+"="+c.Image)
	}
	v := a.Verification
	return Content{
		Title: fmt.Sprintf(pick(english, "回滚 %s 到修订 %s", "Roll back %s to revision %s"), a.Deployment, a.TargetRevision),
		Risk:  "high",
		Impact: pick(english,
			fmt.Sprintf("将 %s/%s 的 Pod 模板从修订 %s 恢复到修订 %s，会触发一次滚动更新。", a.Namespace, a.Deployment, a.FromRevision, a.TargetRevision),
			fmt.Sprintf("Restores the Pod template of %s/%s from revision %s to revision %s, which starts a rolling update.", a.Namespace, a.Deployment, a.FromRevision, a.TargetRevision)),
		Reversible:      true,
		ProposedBy:      a.ActorID,
		InvestigationID: a.InvestigationID,
		Target:          fmt.Sprintf("Deployment %s/%s (%s)", a.Namespace, a.Deployment, strings.Join(images, ", ")),
		Diff:            Diff{Path: fmt.Sprintf("deployments/%s/%s", a.Namespace, a.Deployment), Lines: lines},
		Method: pick(english,
			"独立执行器用专用写凭据做一次条件补丁：只有 Deployment 仍是提案时的对象和模板时才写入，只替换 Pod 模板，不改副本数。不会自动重试或再次写入。",
			"The independent executor makes one conditional patch with its own write credential: it writes only while the Deployment is still the proposed object and template, replaces only the Pod template and leaves replicas unchanged. It never retries or writes a second time."),
		Evidence: []Evidence{{N: 1, Icon: "git-branch", Title: pick(english, "提案时读取的 Deployment 与修订", "Deployment and revisions read at proposal time"),
			Desc: fmt.Sprintf(pick(english, "当前修订 %s，目标修订 %s 仍由该 Deployment 拥有。原因：%s", "Current revision %s; target revision %s is still owned by the Deployment. Reason: %s"),
				a.FromRevision, a.TargetRevision, a.Reason)}},
		Verification: []string{
			pick(english, "等待滚动完成：已更新、可用副本数等于期望值。", "Wait for the rollout: updated and available replicas equal the desired count."),
			fmt.Sprintf(pick(english, "滚动完成后观察 %d 秒：错误率每次都 ≤ %g，请求率 ≥ %g/s，样本不超过 60 秒。", "Observe for %d s after the rollout: error ratio ≤ %g on every sample, request rate ≥ %g/s, samples newer than 60 s."),
				a.ObservationSeconds, v.MaxErrorRatio, v.MinRequestRate),
		},
		VerificationNote: pick(english, "查询为审查目录中的："+v.ErrorRatioQueryID+"、"+v.RequestRateQueryID+"、"+v.SampleAgeQueryID+"。",
			"Reviewed catalog queries: "+v.ErrorRatioQueryID+", "+v.RequestRateQueryID+", "+v.SampleAgeQueryID+"."),
		Checklist: []string{
			pick(english, "两位非发起人的审批人用通行密钥批准。", "Two approvers other than the requester approve with a passkey."),
			pick(english, "执行时 Deployment 已被他人修改则不写入。", "Nothing is written if the Deployment changed in the meantime."),
		},
		Requirement: pick(english, "待审提案不授予任何写权限；验证失败不会自动再次回滚。", "A pending review grants no write; a failed verification is not rolled back automatically."),
	}
}

// loadRollbackApprovalActionTx rebuilds the approval envelope of a rollback
// from its stored action, checking both digests against the stored bytes.
func (s *Service) loadRollbackApprovalActionTx(ctx context.Context, tx pgx.Tx, id string,
	review record) (v3alertmanager.SilenceReviewedAction, error) {
	var zero v3alertmanager.SilenceReviewedAction
	a, actionDigest, reviewDigest, err := loadRollbackActionTx(ctx, tx, id)
	if err != nil {
		return zero, err
	}
	var creatorKind, parentCreator string
	var contentRaw []byte
	if err = tx.QueryRow(ctx, `SELECT creator_kind,created_by,content FROM v3_proposals WHERE id=$1 FOR SHARE`, id).
		Scan(&creatorKind, &parentCreator, &contentRaw); err != nil {
		return zero, err
	}
	var content any
	if err = json.Unmarshal(contentRaw, &content); err != nil {
		return zero, ErrInvalidProposal
	}
	canonical, err := canonicalProposalJSON(content)
	if err != nil || proposalDigest("aiops.v3.proposal.review.v1", canonical) != reviewDigest ||
		creatorKind != "user" || parentCreator != a.ActorID || a.EnvironmentID != review.EnvironmentID ||
		a.ApplicationID != review.ApplicationID || a.InvestigationID != review.InvestigationID {
		return zero, ErrInvalidProposal
	}
	return v3alertmanager.SilenceReviewedAction{ProposalID: id, Kind: RollbackActionKind, Status: review.Status,
		ActionDigest: actionDigest, ReviewContentDigest: reviewDigest, CreatorID: a.ActorID,
		EnvironmentID: a.EnvironmentID, ApplicationID: a.ApplicationID, InvestigationID: a.InvestigationID,
		InvestigationUpdatedAt: a.InvestigationUpdatedAt}, nil
}

// LoadRollbackActionTx reads and verifies one rollback action snapshot: the
// stored bytes must still match their digest and the relational columns.
// Snapshots are immutable (trigger), so read-only callers need no lock.
func LoadRollbackActionTx(ctx context.Context, tx pgx.Tx, id string) (RollbackAction, string, string, error) {
	return loadRollbackActionTx(ctx, tx, id)
}

func loadRollbackActionTx(ctx context.Context, tx pgx.Tx, id string) (RollbackAction, string, string, error) {
	var a RollbackAction
	var kind, actionDigest, reviewDigest, creator, env, app, investigation, sourceKind, key, requestHash, fingerprint string
	var version int
	var updatedAt time.Time
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT action_kind,schema_version,action,action_digest,review_content_digest,creator_id,
 environment_id,application_id,investigation_id,investigation_updated_at,source_kind,idempotency_key,request_hash,binding_fingerprint
 FROM v3_proposal_actions WHERE proposal_id=$1`, id).Scan(&kind, &version, &raw, &actionDigest, &reviewDigest,
		&creator, &env, &app, &investigation, &updatedAt, &sourceKind, &key, &requestHash, &fingerprint)
	if err != nil {
		return a, "", "", err
	}
	var decoded any
	if kind != RollbackActionKind || version != 1 || json.Unmarshal(raw, &decoded) != nil {
		return a, "", "", ErrInvalidProposal
	}
	canonical, err := canonicalProposalJSON(decoded)
	if err != nil || proposalDigest("aiops.v3.proposal.action.v1", canonical) != actionDigest ||
		json.Unmarshal(raw, &a) != nil || !a.Valid() || a.ActorID != creator || a.EnvironmentID != env ||
		a.ApplicationID != app || a.InvestigationID != investigation || !a.InvestigationUpdatedAt.Equal(updatedAt) ||
		sourceKind != silenceProposalSource || a.IdempotencyKey != key || a.RequestHash != requestHash ||
		a.BindingFingerprint != fingerprint {
		return RollbackAction{}, "", "", ErrInvalidProposal
	}
	return a, actionDigest, reviewDigest, nil
}

// RollbackSummary is the proposal detail's structured rollback summary.
type RollbackSummary struct {
	ServiceID      string                   `json:"service_id"`
	ApplicationID  string                   `json:"application_id"`
	EnvironmentID  string                   `json:"environment_id"`
	Namespace      string                   `json:"namespace"`
	Deployment     string                   `json:"deployment"`
	FromRevision   string                   `json:"from_revision"`
	TargetRevision string                   `json:"target_revision"`
	Containers     []v3kube.Container       `json:"containers"`
	Changes        []v3kube.Change          `json:"changes"`
	Verification   RollbackVerificationPlan `json:"verification"`
	Reason         string                   `json:"reason"`
}

// RollbackVerificationPlan is the reviewer-facing verification plan.
type RollbackVerificationPlan struct {
	ObservationSeconds int     `json:"observation_seconds"`
	RequestRateQueryID string  `json:"request_rate_query_id"`
	ErrorRatioQueryID  string  `json:"error_ratio_query_id"`
	SampleAgeQueryID   string  `json:"sample_age_query_id"`
	MaxErrorRatio      float64 `json:"max_error_ratio"`
	MinRequestRate     float64 `json:"min_request_rate"`
}

// Plan returns the reviewer-facing verification plan of an action.
func (a RollbackAction) Plan() RollbackVerificationPlan {
	v := a.Verification
	return RollbackVerificationPlan{ObservationSeconds: a.ObservationSeconds, RequestRateQueryID: v.RequestRateQueryID,
		ErrorRatioQueryID: v.ErrorRatioQueryID, SampleAgeQueryID: v.SampleAgeQueryID, MaxErrorRatio: v.MaxErrorRatio,
		MinRequestRate: v.MinRequestRate}
}

func rollbackSummary(a RollbackAction) *RollbackSummary {
	changes := a.Changes
	if changes == nil {
		changes = []v3kube.Change{}
	}
	return &RollbackSummary{ServiceID: a.ServiceID, ApplicationID: a.ApplicationID, EnvironmentID: a.EnvironmentID,
		Namespace: a.Namespace, Deployment: a.Deployment, FromRevision: a.FromRevision, TargetRevision: a.TargetRevision,
		Containers: a.Containers, Changes: changes, Verification: a.Plan(), Reason: a.Reason}
}

// actionSummaryTx reads the action kind of a proposal and, for a rollback,
// its summary. Display-only proposals have no action (nil kind).
func actionSummaryTx(ctx context.Context, tx pgx.Tx, id string) (*string, *RollbackSummary, error) {
	var kind string
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT action_kind,action FROM v3_proposal_actions WHERE proposal_id=$1`, id).Scan(&kind, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if kind != RollbackActionKind {
		return &kind, nil, nil
	}
	var a RollbackAction
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, nil, err
	}
	return &kind, rollbackSummary(a), nil
}
