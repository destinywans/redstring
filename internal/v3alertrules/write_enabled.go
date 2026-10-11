package v3alertrules

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3proposals"
)

const alertRuleEnableKind = "prometheus.alert_rule.enable"

// alertRuleDisableKind removes an active rule through the same approval and
// executor (D11 decision 7). Its intent is always schema version 2.
const alertRuleDisableKind = "prometheus.alert_rule.disable"

// alertRuleActionKinds are the alert rule proposal actions.
var alertRuleActionKinds = []string{alertRuleEnableKind, alertRuleDisableKind}

func alertRuleActionKind(kind string) bool {
	return kind == alertRuleEnableKind || kind == alertRuleDisableKind
}

// AlertRuleEnableIntent is review evidence, never execution authority. WAN-117
// must verify independent human approval, current grants, source content, MR
// bytes and target binding before an independent executor changes a rule.
//
// Schema version 1 (WAN-118) is bound to an exported nondeployable draft MR,
// whose fields are always set. Version 2 (D11) has no draft: the request
// proposes the rule and its deployable plan directly, and the draft fields
// are absent.
type AlertRuleEnableIntent struct {
	Kind                       string                  `json:"kind"`
	SchemaVersion              int                     `json:"schema_version"`
	InvestigationID            string                  `json:"investigation_id"`
	InvestigationUpdatedAt     time.Time               `json:"investigation_updated_at"`
	DraftOperationID           string                  `json:"draft_operation_id,omitempty"`
	DraftID                    string                  `json:"draft_id,omitempty"`
	DraftVersion               int                     `json:"draft_version,omitempty"`
	EnvironmentID              string                  `json:"environment_id"`
	ApplicationID              string                  `json:"application_id"`
	ProviderFileIdentityDigest string                  `json:"provider_file_identity_digest"`
	ObservedRuleID             string                  `json:"observed_rule_id,omitempty"`
	ObservedDigest             string                  `json:"observed_digest,omitempty"`
	SpecDigest                 string                  `json:"spec_digest"`
	ManifestDigest             string                  `json:"manifest_digest,omitempty"`
	ProjectID                  int64                   `json:"project_id,omitempty"`
	MR                         int64                   `json:"mr_iid,omitempty"`
	MRURL                      string                  `json:"mr_url,omitempty"`
	Branch                     string                  `json:"branch,omitempty"`
	Path                       string                  `json:"path,omitempty"`
	Deployment                 AlertRuleDeploymentPlan `json:"deployment"`
	Spec                       DraftRuleSpec           `json:"spec"`
}

// alertRuleEnableIntentDirect is the schema version of a request without a
// draft (D11 decision 6).
const alertRuleEnableIntentDirect = 2

func enableDigest(domain string, raw []byte) string {
	sum := sha256.Sum256(append([]byte(domain+"\x00"), raw...))
	return hex.EncodeToString(sum[:])
}

// PostgreSQL jsonb reorders keys and spaces values on retrieval. Hash the
// decoded value in a stable form both before storage and after loading it.
func canonicalEnableJSON(raw []byte) ([]byte, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

// handleEnable proposes the rule and its exact deployable plan (D11
// decision 6): no nondeployable draft MR is exported first.
func (s *WriteService) handleEnable(w http.ResponseWriter, r *http.Request, env string, actor core.User, key string, in createRequest) {
	if s.EnableApplier == nil {
		writeError(w, problem(503, "rule_applier_not_connected", "Alert rule deployment target is not connected"))
		return
	}
	if s.EnableReady == nil || s.EnableReady(r.Context(), env) != nil {
		writeError(w, problem(503, "enable_not_connected", "Alert rule approval worker is not connected"))
		return
	}
	if !validEnableInvestigationID(in.InvestigationID) || len(key) < 8 {
		writeError(w, problem(422, "invalid_enable_request", "An investigation ID and an Idempotency-Key of at least eight characters are required"))
		return
	}
	spec := in.DraftRuleSpec
	// The stored spec keeps the draft rule format; enabling is the proposal
	// action, never a mode of the rule itself.
	spec.Mode = "draft"
	if err := validateDraftSpec(spec, env, spec.ApplicationID, s.Reader.ApplicationLabel(env, spec.ApplicationID)); err != nil {
		writeError(w, validationProblem(err))
		return
	}
	digest, err := requestHash("enable", env, "", in)
	if err != nil {
		writeError(w, err)
		return
	}
	if _, err := s.enablePreflight(r.Context(), actor, env, spec.ApplicationID, in.InvestigationID, key, digest, alertRuleEnableKind); err != nil {
		writeError(w, err)
		return
	}
	if result, found, err := s.replayEnable(r.Context(), actor, key, digest, in.InvestigationID, alertRuleEnableKind); err != nil {
		writeError(w, err)
		return
	} else if found {
		httpapi.WriteJSON(w, http.StatusAccepted, result)
		return
	}
	groups, observed, err := s.inspectCandidates(r.Context(), env, spec.ApplicationID, []DraftRuleSpec{spec})
	if err != nil {
		writeError(w, err)
		return
	}
	if observed[0].exists && spec.ObservedRuleID == "" {
		writeError(w, problem(409, "duplicate_rule", "A provider rule already uses this identity"))
		return
	}
	if err := validateObservedHint(spec.ObservedRuleID, observed[0]); err != nil {
		writeError(w, err)
		return
	}
	if err := s.validatePromQL(r.Context(), env, spec.Expr); err != nil {
		writeError(w, err)
		return
	}
	result, err := s.persistEnableIntent(r.Context(), actor, env, key, digest, in.InvestigationID, spec, groups[0], observed[0], alertRuleEnableKind)
	if err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, result)
}

// Canvas creates INV-prefixed IDs; legacy investigations retain naked IDs.
// Validate both formats without normalizing the persisted lookup identity.
// Proposal, draft and execution IDs continue to use the stricter hexID helper.
func validEnableInvestigationID(id string) bool {
	if hexID(id) {
		return true
	}
	suffix, prefixed := strings.CutPrefix(id, "INV-")
	return prefixed && hexID(suffix)
}

func (s *WriteService) enablePreflight(ctx context.Context, actor core.User, env, app, investigation, key, requestDigest, actionKind string) (time.Time, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return time.Time{}, err
	}
	defer tx.Rollback(ctx)
	updated, err := s.enableScopeTx(ctx, tx, actor, env, app, investigation)
	if err != nil {
		return time.Time{}, err
	}
	var kind, storedDigest string
	err = tx.QueryRow(ctx, `SELECT action_kind,request_hash FROM v3_proposal_actions
 WHERE creator_id=$1 AND idempotency_key=$2 FOR SHARE`, actor.ID, key).Scan(&kind, &storedDigest)
	if err == nil && (kind != actionKind || storedDigest != requestDigest) {
		return time.Time{}, problem(409, "idempotency_conflict", "Idempotency key was used for another proposal")
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, err
	}
	return updated, tx.Commit(ctx)
}

func (s *WriteService) enableScopeTx(ctx context.Context, tx pgx.Tx, actor core.User,
	env, app, investigation string) (time.Time, error) {
	if err := s.authorizeTx(ctx, tx, actor, env, app); err != nil {
		return time.Time{}, err
	}
	var human bool
	if err := tx.QueryRow(ctx, `SELECT v3_identity_kind='human' FROM users WHERE id=$1 AND NOT disabled
 AND deleted_at IS NULL AND source<>'settings_demo' FOR SHARE`, actor.ID).Scan(&human); err != nil || !human {
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return time.Time{}, err
		}
		return time.Time{}, problem(403, "human_required", "A current human identity is required")
	}
	var owner string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(owner_team_id,'') FROM applications
 WHERE id=$1 AND NOT archived FOR SHARE`, app).Scan(&owner); err != nil {
		return time.Time{}, err
	}
	decision := rbac.V3Decision{Module: "proposals", Action: "change", EnvironmentID: env, ApplicationID: app}
	err := s.Access.AuthorizeWithTx(ctx, tx, actor, decision)
	if errors.Is(err, store.ErrForbidden) && owner != "" {
		decision.ApplicationID = ""
		decision.TeamID = owner
		err = s.Access.AuthorizeWithTx(ctx, tx, actor, decision)
	}
	if errors.Is(err, store.ErrForbidden) {
		return time.Time{}, problem(403, "forbidden", "Proposal change permission required")
	}
	if err != nil {
		return time.Time{}, err
	}
	var updated time.Time
	err = tx.QueryRow(ctx, `SELECT i.updated_at FROM investigations i
 JOIN application_environments e ON e.id=i.environment_id AND e.application_id=i.application_id
 WHERE i.id=$1 AND i.application_id=$2 AND i.status<>'closed'
 AND e.key=$3 AND NOT e.archived FOR SHARE OF i,e`, investigation, app, env).Scan(&updated)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, problem(404, "investigation_not_found", "Open investigation not found in the selected application and environment")
	}
	return updated.UTC(), err
}

func enableReviewContent(in AlertRuleEnableIntent, actorID string) v3proposals.Content {
	if in.Kind == alertRuleDisableKind {
		return disableReviewContent(in, actorID)
	}
	if in.SchemaVersion == alertRuleEnableIntentDirect {
		return directEnableReviewContent(in, actorID)
	}
	lines := []string{fmt.Sprintf("Rule: %s / %s", in.Spec.Group, in.Spec.Name),
		"For: " + in.Spec.For, "Severity: " + in.Spec.Labels["severity"]}
	// The complete action is shown, including every label/annotation and the
	// exact GitOps target, base revision, and proposed YAML digest. Keep every
	// line within the V3 proposal rendering bound without truncating content.
	fullAction, _ := json.Marshal(in)
	runes := []rune(string(fullAction))
	for start := 0; start < len(runes); start += 1200 {
		end := start + 1200
		if end > len(runes) {
			end = len(runes)
		}
		lines = append(lines, fmt.Sprintf("Action JSON %d-%d: %s", start+1, end, string(runes[start:end])))
	}
	return v3proposals.Content{
		Title: fmt.Sprintf("Enable alert rule %s", in.Spec.Name), Risk: "high",
		Impact:     "An independent executor could make this alert rule active after separate human approval. This request does not enable or notify.",
		Reversible: false, ProposedBy: actorID, InvestigationID: in.InvestigationID,
		Target:           fmt.Sprintf("Prometheus application=%s environment=%s group=%s", in.ApplicationID, in.EnvironmentID, in.Spec.Group),
		Diff:             v3proposals.Diff{Path: in.Deployment.Path, Lines: lines},
		Method:           "Review the immutable draft and exact GitLab MR, approve with independent human authority, then let a separate executor revalidate source and apply.",
		Evidence:         []v3proposals.Evidence{{N: 1, Icon: "activity", Title: "Bound rule draft", Desc: fmt.Sprintf("Draft project %d MR !%d; draft %s version %d. Proposed deployable YAML digest %s is based on source revision %s.", in.ProjectID, in.MR, in.DraftID, in.DraftVersion, in.Deployment.ContentDigest, in.Deployment.BaseRevision)}},
		Verification:     []string{"Recheck current human approval, grants, investigation, application/environment and group binding, provider rule identity/content, GitLab MR state/branch/path/bytes, and duplicate rules before execution."},
		VerificationNote: "PromQL syntax was parsed by the configured Prometheus. Execution must recheck input scope, source revision and exact proposed YAML bytes.",
		Checklist:        []string{"Separate approval and independent executor are required; do not merge this nondeployable JSON as a rule."},
		Requirement:      "Pending review only; no rule is active as a result of this request.",
	}
}

// directEnableReviewContent shows a version 2 request: the complete rule,
// the exact target file and its base revision, and the proposed bytes digest.
// The text must stay a pure function of the intent, which the loader rechecks.
func directEnableReviewContent(in AlertRuleEnableIntent, actorID string) v3proposals.Content {
	// D32: the scope labels decide which instance and service the rule's
	// alerts reach, so the summary names them.
	lines := []string{fmt.Sprintf("Rule: %s / %s", in.Spec.Group, in.Spec.Name),
		"For: " + in.Spec.For, "Severity: " + in.Spec.Labels["severity"],
		fmt.Sprintf("Scope labels: environment=%s application=%s", in.Spec.Labels["environment"], in.Spec.Labels["application"])}
	fullAction, _ := json.Marshal(in)
	runes := []rune(string(fullAction))
	for start := 0; start < len(runes); start += 1200 {
		end := min(start+1200, len(runes))
		lines = append(lines, fmt.Sprintf("Action JSON %d-%d: %s", start+1, end, string(runes[start:end])))
	}
	method := "Approve with two independent humans; a separate executor revalidates source and target, opens the MR and waits for its review and sync."
	checklist := "Separate approval and independent executor are required; the GitLab MR still needs its own review."
	if in.Deployment.ExecutorMerges() {
		method = "Approve with two independent humans; a separate executor revalidates source and target, commits this exact file, merges it fast-forward and reads the rule back from Prometheus."
		checklist = "This approval covers the executor merging the MR: review the complete rule and the target file before approving."
	}
	return v3proposals.Content{
		Title: fmt.Sprintf("Enable alert rule %s", in.Spec.Name), Risk: "high",
		Impact:     "An independent executor could make this alert rule active after separate human approval. This request does not enable or notify.",
		Reversible: false, ProposedBy: actorID, InvestigationID: in.InvestigationID,
		Target: fmt.Sprintf("Prometheus application=%s environment=%s group=%s", in.ApplicationID, in.EnvironmentID, in.Spec.Group),
		Diff:   v3proposals.Diff{Path: in.Deployment.Path, Lines: lines},
		Method: method,
		Evidence: []v3proposals.Evidence{{N: 1, Icon: "activity", Title: "Target rule file", Desc: fmt.Sprintf("Project %d branch %s file %s at source revision %s; proposed YAML digest %s.",
			in.Deployment.ProjectID, in.Deployment.TargetBranch, in.Deployment.Path, in.Deployment.BaseRevision, in.Deployment.ContentDigest)}},
		Verification:     []string{"Recheck current human approval, grants, investigation, application/environment and group binding, provider rule identity/content, target branch, protection, base revision and exact bytes before execution."},
		VerificationNote: "PromQL syntax was parsed by the configured Prometheus. Execution must recheck input scope, source revision and exact proposed YAML bytes.",
		Checklist:        []string{checklist},
		Requirement:      "Pending review only; no rule is active as a result of this request.",
	}
}

// persistEnableIntent stores a version 2 proposal of kind (enable or
// disable): the rule, the observed identity it was checked against and the
// adapter's exact deployable plan.
func (s *WriteService) persistEnableIntent(ctx context.Context, actor core.User, env, key, requestDigest, investigation string,
	spec DraftRuleSpec, group boundGroup, observed observedIdentity, kind string) (map[string]any, error) {
	specHash, _, err := specDigest(spec)
	if err != nil {
		return nil, err
	}
	intent := AlertRuleEnableIntent{Kind: kind, SchemaVersion: alertRuleEnableIntentDirect,
		InvestigationID: investigation, EnvironmentID: env, ApplicationID: spec.ApplicationID,
		ProviderFileIdentityDigest: fileIdentityDigest(group.file), ObservedRuleID: observed.id, ObservedDigest: observed.digest,
		SpecDigest: specHash, Spec: spec}
	plan, err := s.EnableApplier.Plan(ctx, intent)
	if err != nil {
		return nil, problem(503, "rule_applier_unavailable", "Alert rule deployment plan is unavailable")
	}
	if err := validateDeploymentPlan(plan); err != nil {
		return nil, err
	}
	// D11: only the executor's merge and readback can remove a rule.
	if kind == alertRuleDisableKind && !plan.ExecutorMerges() {
		return nil, problem(422, "disable_unavailable", "This rule group is deployed without an executor merge; disable it in its repository")
	}
	intent.Deployment = plan
	change, hasChange := s.proposalChange(ctx, intent)
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	updated, err := s.enableScopeTx(ctx, tx, actor, env, spec.ApplicationID, investigation)
	if err != nil {
		return nil, err
	}
	intent.InvestigationUpdatedAt = updated
	if prior, found, err := replayEnableTx(ctx, tx, actor, key, requestDigest, investigation, kind); err != nil || found {
		if err != nil {
			return nil, err
		}
		return prior, tx.Commit(ctx)
	}
	content := enableReviewContent(intent, actor.ID)
	contentRaw, err := json.Marshal(content)
	if err != nil {
		return nil, err
	}
	contentRaw, err = canonicalEnableJSON(contentRaw)
	if err != nil {
		return nil, err
	}
	actionRaw, err := json.Marshal(intent)
	if err != nil {
		return nil, err
	}
	actionRaw, err = canonicalEnableJSON(actionRaw)
	if err != nil {
		return nil, err
	}
	actionDigest := enableDigest("aiops.v3.proposal.action.v1", actionRaw)
	binding := enableDigest("aiops.v3.alert_rule.binding.v1", []byte(strings.Join([]string{
		env, spec.ApplicationID, spec.Group, intent.ProviderFileIdentityDigest,
		fmt.Sprint(plan.ProjectID)}, "\x00")))
	proposalID := store.ID()
	_, err = tx.Exec(ctx, `INSERT INTO v3_proposals
 (id,investigation_id,application_id,environment_id,content,created_by,creator_kind)
 VALUES($1,$2,$3,$4,$5::jsonb,$6,'user')`, proposalID, investigation, spec.ApplicationID, env, contentRaw, actor.ID)
	if err != nil {
		return nil, dbWriteProblem(err)
	}
	// The action envelope stays at schema_version 1; the intent carries its
	// own version.
	_, err = tx.Exec(ctx, `INSERT INTO v3_proposal_actions
 (proposal_id,action_kind,schema_version,action,action_digest,review_content_digest,request_hash,
 binding_fingerprint,creator_id,environment_id,application_id,investigation_id,
 investigation_updated_at,idempotency_key,source_kind)
 VALUES($1,$2,1,$3::jsonb,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,'human_request')`,
		proposalID, kind, actionRaw, actionDigest,
		enableDigest("aiops.v3.proposal.review.v1", contentRaw), requestDigest, binding,
		actor.ID, env, spec.ApplicationID, investigation, updated, key)
	if err != nil {
		return nil, dbWriteProblem(err)
	}
	if hasChange {
		if _, err = tx.Exec(ctx, `INSERT INTO v3_alert_rule_proposal_changes(proposal_id,base_revision,content_sha256,lines)
 VALUES($1,$2,$3,$4::jsonb)`, proposalID, plan.BaseRevision, plan.ContentDigest, change); err != nil {
			return nil, err
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,actor_kind,application_id,action,target,outcome,changes)
 VALUES($1,'user',$2,$11,$3,'pending',jsonb_build_object(
 'environment_id',$4::text,'investigation_id',$5::text,'group',$6::text,'alert',$7::text,
 'base_revision',$8::text,'content_digest',$9::text,'action_digest',$10::text))`, actor.ID, spec.ApplicationID, proposalID,
		env, investigation, spec.Group, spec.Name, plan.BaseRevision, plan.ContentDigest, actionDigest, requestedAudit(kind))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return enabledResponse(intent, proposalID, "pending"), nil
}

func requestedAudit(kind string) string {
	if kind == alertRuleDisableKind {
		return "v3.alert_rule.disable_requested"
	}
	return "v3.alert_rule.enable_requested"
}

// replayEnable answers a repeated request with the proposal it created.
func (s *WriteService) replayEnable(ctx context.Context, actor core.User, key, requestDigest, investigation, actionKind string) (map[string]any, bool, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx)
	result, found, err := replayEnableTx(ctx, tx, actor, key, requestDigest, investigation, actionKind)
	if err != nil || !found {
		return nil, false, err
	}
	return result, true, tx.Commit(ctx)
}

func replayEnableTx(ctx context.Context, tx pgx.Tx, actor core.User, key, requestDigest, investigation, actionKind string) (map[string]any, bool, error) {
	var proposalID, storedDigest, kind, status string
	err := tx.QueryRow(ctx, `SELECT a.proposal_id,a.request_hash,a.action_kind,p.status
 FROM v3_proposal_actions a JOIN v3_proposals p ON p.id=a.proposal_id
 WHERE a.creator_id=$1 AND a.idempotency_key=$2 FOR SHARE OF a,p`, actor.ID, key).
		Scan(&proposalID, &storedDigest, &kind, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if kind != actionKind || storedDigest != requestDigest {
		return nil, false, problem(409, "idempotency_conflict", "Idempotency key was used for another proposal")
	}
	intent, err := loadAlertRuleEnableIntentWithTx(ctx, tx, proposalID, false)
	if err != nil || intent.InvestigationID != investigation {
		return nil, false, problem(409, "enable_intent_changed", "Existing proposal does not match the request")
	}
	return enabledResponse(intent, proposalID, status), true, nil
}

// enabledResponse reports the proposal. Only a version 1 proposal has a
// draft, version and draft MR; version 2 reports them as null.
func enabledResponse(intent AlertRuleEnableIntent, proposalID, proposalStatus string) map[string]any {
	status := "pending_approval"
	if proposalStatus == "rejected" {
		status = "rejected"
	} else if proposalStatus == "approved" {
		status = "approved_pending_execution"
	}
	if intent.Kind == alertRuleDisableKind {
		return map[string]any{"proposal_id": proposalID, "status": status}
	}
	result := map[string]any{"draft_id": nil, "version": nil, "mr": nil, "proposal_id": proposalID, "status": status}
	if intent.SchemaVersion == 1 {
		result["draft_id"], result["version"] = intent.DraftID, intent.DraftVersion
		result["mr"] = map[string]any{"project_id": strconv.FormatInt(intent.ProjectID, 10), "iid": intent.MR, "url": intent.MRURL}
	}
	return result
}

// LoadAlertRuleEnableIntentWithTx verifies the immutable action and review
// bytes for proposal review. It does not assert approval or execution authority.
func LoadAlertRuleEnableIntentWithTx(ctx context.Context, tx pgx.Tx, proposalID string) (AlertRuleEnableIntent, error) {
	return loadAlertRuleEnableIntentWithTx(ctx, tx, proposalID, false)
}

func loadAlertRuleEnableIntentWithTx(ctx context.Context, tx pgx.Tx, proposalID string, requirePending bool) (AlertRuleEnableIntent, error) {
	var intent AlertRuleEnableIntent
	var actionRaw, reviewRaw []byte
	var actionHash, reviewHash, kind, status, env, app, investigation, source, creator string
	var schemaVersion int
	err := tx.QueryRow(ctx, `SELECT a.action,a.action_digest,a.review_content_digest,a.action_kind,
 p.content,p.status,p.environment_id,p.application_id,p.investigation_id,a.schema_version,a.source_kind,a.creator_id
 FROM v3_proposal_actions a JOIN v3_proposals p ON p.id=a.proposal_id
 WHERE a.proposal_id=$1 FOR SHARE OF a,p`, proposalID).
		Scan(&actionRaw, &actionHash, &reviewHash, &kind, &reviewRaw, &status, &env, &app, &investigation, &schemaVersion, &source, &creator)
	if err != nil {
		return intent, err
	}
	canonicalAction, actionErr := canonicalEnableJSON(actionRaw)
	canonicalReview, reviewErr := canonicalEnableJSON(reviewRaw)
	if !alertRuleActionKind(kind) || schemaVersion != 1 || source != "human_request" ||
		(requirePending && status != "pending") || (status != "pending" && status != "rejected" && status != "approved") ||
		actionErr != nil || reviewErr != nil ||
		enableDigest("aiops.v3.proposal.action.v1", canonicalAction) != actionHash ||
		enableDigest("aiops.v3.proposal.review.v1", canonicalReview) != reviewHash ||
		json.Unmarshal(actionRaw, &intent) != nil || intent.Kind != kind ||
		(intent.SchemaVersion != 1 && intent.SchemaVersion != alertRuleEnableIntentDirect) ||
		(kind == alertRuleDisableKind && intent.SchemaVersion != alertRuleEnableIntentDirect) ||
		intent.EnvironmentID != env || intent.ApplicationID != app || intent.InvestigationID != investigation {
		return AlertRuleEnableIntent{}, problem(409, "enable_intent_invalid", "Alert rule enable proposal is invalid or rejected")
	}
	draft := hexID(intent.DraftID) && hexID(intent.DraftOperationID) && intent.ProjectID > 0 && intent.MR > 0 &&
		intent.MRURL != "" && intent.DraftVersion >= 1
	direct := intent.DraftID == "" && intent.DraftOperationID == "" && intent.DraftVersion == 0 && intent.ManifestDigest == "" &&
		intent.ProjectID == 0 && intent.MR == 0 && intent.MRURL == "" && intent.Branch == "" && intent.Path == ""
	hash, _, err := specDigest(intent.Spec)
	if err != nil || hash != intent.SpecDigest || intent.Spec.Mode != "draft" ||
		intent.Spec.EnvironmentID != env || intent.Spec.ApplicationID != app ||
		(intent.SchemaVersion == 1 && !draft) || (intent.SchemaVersion == alertRuleEnableIntentDirect && !direct) ||
		(kind == alertRuleDisableKind && (intent.ObservedRuleID == "" || intent.ObservedDigest == "")) ||
		validateDeploymentPlan(intent.Deployment) != nil {
		return AlertRuleEnableIntent{}, problem(409, "enable_intent_invalid", "Alert rule enable proposal snapshot is invalid")
	}
	expectedReview, err := json.Marshal(enableReviewContent(intent, creator))
	if err != nil {
		return AlertRuleEnableIntent{}, problem(409, "enable_intent_invalid", "Alert rule review could not be verified")
	}
	expectedReview, err = canonicalEnableJSON(expectedReview)
	if err != nil || string(expectedReview) != string(canonicalReview) {
		return AlertRuleEnableIntent{}, problem(409, "enable_intent_invalid", "Alert rule review does not match the action")
	}
	return intent, nil
}
