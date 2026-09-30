package v3alertrules

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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

// AlertRuleEnableIntent is review evidence, never execution authority. WAN-117
// must verify independent human approval, current grants, source content, MR
// bytes and target binding before an independent executor changes a rule.
type AlertRuleEnableIntent struct {
	Kind                       string                  `json:"kind"`
	SchemaVersion              int                     `json:"schema_version"`
	InvestigationID            string                  `json:"investigation_id"`
	InvestigationUpdatedAt     time.Time               `json:"investigation_updated_at"`
	DraftOperationID           string                  `json:"draft_operation_id"`
	DraftID                    string                  `json:"draft_id"`
	DraftVersion               int                     `json:"draft_version"`
	EnvironmentID              string                  `json:"environment_id"`
	ApplicationID              string                  `json:"application_id"`
	ProviderFileIdentityDigest string                  `json:"provider_file_identity_digest"`
	ObservedRuleID             string                  `json:"observed_rule_id,omitempty"`
	ObservedDigest             string                  `json:"observed_digest,omitempty"`
	SpecDigest                 string                  `json:"spec_digest"`
	ManifestDigest             string                  `json:"manifest_digest"`
	ProjectID                  int64                   `json:"project_id"`
	MR                         int64                   `json:"mr_iid"`
	MRURL                      string                  `json:"mr_url"`
	Branch                     string                  `json:"branch"`
	Path                       string                  `json:"path"`
	Deployment                 AlertRuleDeploymentPlan `json:"deployment"`
	Spec                       DraftRuleSpec           `json:"spec"`
}

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

func (s *WriteService) handleEnable(w http.ResponseWriter, r *http.Request, env string, actor core.User, key string, in createRequest) {
	if err := s.requireDraftRepository(); err != nil {
		writeError(w, err)
		return
	}
	if s.EnableApplier == nil {
		writeError(w, problem(503, "rule_applier_not_connected", "Alert rule deployment target is not connected"))
		return
	}
	if s.EnableReady == nil || s.EnableReady(r.Context(), env) != nil {
		writeError(w, problem(503, "enable_not_connected", "Alert rule approval worker is not connected"))
		return
	}
	if !hexID(in.InvestigationID) || len(key) < 8 {
		writeError(w, problem(422, "invalid_enable_request", "An investigation ID and an Idempotency-Key of at least eight characters are required"))
		return
	}
	spec := in.DraftRuleSpec
	spec.Mode = "draft" // The remote artifact is always a nondeployable draft.
	if err := validateDraftSpec(spec, env, spec.ApplicationID); err != nil {
		writeError(w, validationProblem(err))
		return
	}
	digest, err := requestHash("enable", env, "", in)
	if err != nil {
		writeError(w, err)
		return
	}
	if _, err := s.enablePreflight(r.Context(), actor, env, spec.ApplicationID, in.InvestigationID, key, digest); err != nil {
		writeError(w, err)
		return
	}
	if result, found, err := s.replay(r, actor, "enable", key, digest, env, spec.ApplicationID); err != nil {
		writeError(w, err)
		return
	} else if found {
		s.writeEnabledResult(w, r, actor, env, key, digest, in.InvestigationID, result)
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
	result, err := s.reserveCreateOperation(r.Context(), actor, env, key, digest, "enable", spec, groups[0], observed[0])
	if err != nil {
		writeError(w, err)
		return
	}
	result, err = s.export(r, actor, "enable", key, digest, result)
	if err != nil {
		writeError(w, err)
		return
	}
	s.writeEnabledResult(w, r, actor, env, key, digest, in.InvestigationID, result)
}

func (s *WriteService) writeEnabledResult(w http.ResponseWriter, r *http.Request, actor core.User,
	env, key, digest, investigation string, draft map[string]any) {
	result, err := s.persistEnableIntent(r.Context(), actor, env, key, digest, investigation, draft)
	if err != nil {
		var p *writeProblem
		if errors.As(err, &p) && (p.status == http.StatusConflict || p.status == http.StatusForbidden || p.status == http.StatusNotFound) {
			if markErr := s.markOrphanedEnableDraft(r.Context(), actor, key, digest); markErr != nil {
				writeError(w, markErr)
				return
			}
		}
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, result)
}

func (s *WriteService) enablePreflight(ctx context.Context, actor core.User, env, app, investigation, key, requestDigest string) (time.Time, error) {
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
	if err == nil && (kind != alertRuleEnableKind || storedDigest != requestDigest) {
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

func (s *WriteService) persistEnableIntent(ctx context.Context, actor core.User,
	env, key, requestDigest, investigation string, draft map[string]any) (map[string]any, error) {
	var record operationRecord
	err := s.DB.QueryRow(ctx, `SELECT id,environment_id,application_id,branch,path,project_id,payload,response,state,
 COALESCE(mr_iid,0),mr_url FROM v3_alert_rule_draft_operations
 WHERE actor_id=$1 AND operation='enable' AND idempotency_key=$2 AND request_digest=$3`,
		actor.ID, key, requestDigest).Scan(&record.ID, &record.EnvironmentID, &record.ApplicationID,
		&record.Branch, &record.Path, &record.ProjectID, &record.Payload, &record.Response, &record.State,
		&record.MR, &record.URL)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, problem(409, "enable_operation_missing", "Rule draft operation is unavailable")
	}
	if err != nil {
		return nil, err
	}
	if record.State != "pending_review" || record.EnvironmentID != env || record.MR <= 0 || record.URL == "" ||
		record.ProjectID != s.Repository.ProjectID() || record.Branch != s.Repository.BranchPrefix()+record.ID ||
		record.Path != "ops-config/alert-rule-drafts/"+record.ID+".json" {
		return nil, problem(409, "enable_draft_unavailable", "A confirmed, unchanged draft MR is required")
	}
	if prior, found, err := s.existingEnableIntent(ctx, actor, key, requestDigest, investigation, record, draft); err != nil {
		return nil, err
	} else if found {
		return prior, nil
	}
	if err := s.recheckManifestSources(ctx, record); err != nil {
		return nil, err
	}
	var manifest draftManifest
	if err := json.Unmarshal(record.Payload, &manifest); err != nil || len(manifest.Rules) != 1 || manifest.OperationID != record.ID ||
		manifest.EnvironmentID != env || manifest.ApplicationID != record.ApplicationID || manifest.Deployable {
		return nil, problem(503, "enable_draft_invalid", "Confirmed draft manifest is invalid")
	}
	item := manifest.Rules[0]
	if !hexID(item.DraftID) || item.Version != 1 || draft["draft_id"] != item.DraftID || draft["version"] != float64(1) && draft["version"] != 1 {
		return nil, problem(503, "enable_draft_invalid", "Confirmed draft response differs from its manifest")
	}
	specHash, _, err := specDigest(item.Spec)
	if err != nil {
		return nil, err
	}
	intent := AlertRuleEnableIntent{Kind: alertRuleEnableKind, SchemaVersion: 1,
		InvestigationID: investigation, DraftOperationID: record.ID, DraftID: item.DraftID, DraftVersion: item.Version,
		EnvironmentID: env, ApplicationID: record.ApplicationID, ProviderFileIdentityDigest: item.ProviderFileIdentityDigest,
		ObservedRuleID: item.ObservedRuleID, ObservedDigest: item.ObservedDigest, SpecDigest: specHash,
		ManifestDigest: enableDigest("aiops.v3.alert_rule.manifest.v1", record.Payload), ProjectID: record.ProjectID,
		MR: record.MR, MRURL: record.URL, Branch: record.Branch, Path: record.Path, Spec: item.Spec}
	plan, err := s.EnableApplier.Plan(ctx, intent)
	if err != nil {
		return nil, problem(503, "rule_applier_unavailable", "Alert rule deployment plan is unavailable")
	}
	if err := validateDeploymentPlan(plan); err != nil {
		return nil, err
	}
	intent.Deployment = plan
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	updated, err := s.enableScopeTx(ctx, tx, actor, env, record.ApplicationID, investigation)
	if err != nil {
		return nil, err
	}
	intent.InvestigationUpdatedAt = updated
	var currentVersion int
	var storedSpecHash, operationState string
	err = tx.QueryRow(ctx, `SELECT d.current_version,r.spec_digest,o.state FROM v3_alert_rule_drafts d
 JOIN v3_alert_rule_draft_revisions r ON r.draft_id=d.id AND r.version=$2
 JOIN v3_alert_rule_draft_operations o ON o.id=r.operation_id
 WHERE d.id=$1 AND r.operation_id=$3 AND d.environment_id=$4 AND d.application_id=$5
	 FOR SHARE OF d,r,o`, item.DraftID, item.Version, record.ID, env, record.ApplicationID).
		Scan(&currentVersion, &storedSpecHash, &operationState)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, problem(409, "enable_draft_changed", "The reviewed draft revision changed")
	}
	if err != nil {
		return nil, err
	}
	if currentVersion != item.Version || storedSpecHash != specHash || operationState != "pending_review" {
		return nil, problem(409, "enable_draft_changed", "The reviewed draft revision changed")
	}
	var existingID, existingRequestHash, existingKind, existingStatus string
	err = tx.QueryRow(ctx, `SELECT a.proposal_id,a.request_hash,a.action_kind,p.status FROM v3_proposal_actions a
 JOIN v3_proposals p ON p.id=a.proposal_id
 WHERE a.creator_id=$1 AND a.idempotency_key=$2 FOR SHARE OF a,p`, actor.ID, key).
		Scan(&existingID, &existingRequestHash, &existingKind, &existingStatus)
	if err == nil {
		if existingKind != alertRuleEnableKind || existingRequestHash != requestDigest {
			return nil, problem(409, "idempotency_conflict", "Idempotency key was used for another proposal")
		}
		stored, loadErr := loadAlertRuleEnableIntentWithTx(ctx, tx, existingID, false)
		if loadErr != nil || stored.DraftOperationID != record.ID || stored.InvestigationID != investigation {
			return nil, problem(409, "enable_intent_changed", "Existing proposal does not match the draft")
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return enabledResponse(draft, existingID, existingStatus), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
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
	binding := enableDigest("aiops.v3.alert_rule.binding.v1", []byte(strings.Join([]string{
		env, record.ApplicationID, item.Spec.Group, item.ProviderFileIdentityDigest,
		fmt.Sprint(record.ProjectID)}, "\x00")))
	proposalID := store.ID()
	_, err = tx.Exec(ctx, `INSERT INTO v3_proposals
 (id,investigation_id,application_id,environment_id,content,created_by,creator_kind)
 VALUES($1,$2,$3,$4,$5::jsonb,$6,'user')`, proposalID, investigation, record.ApplicationID, env, contentRaw, actor.ID)
	if err != nil {
		return nil, dbWriteProblem(err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO v3_proposal_actions
 (proposal_id,action_kind,schema_version,action,action_digest,review_content_digest,request_hash,
 binding_fingerprint,creator_id,environment_id,application_id,investigation_id,
 investigation_updated_at,idempotency_key,source_kind)
 VALUES($1,$2,1,$3::jsonb,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,'human_request')`,
		proposalID, alertRuleEnableKind, actionRaw, enableDigest("aiops.v3.proposal.action.v1", actionRaw),
		enableDigest("aiops.v3.proposal.review.v1", contentRaw), requestDigest, binding,
		actor.ID, env, record.ApplicationID, investigation, updated, key)
	if err != nil {
		return nil, dbWriteProblem(err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,actor_kind,application_id,action,target,outcome,changes)
 VALUES($1,'user',$2,'v3.alert_rule.enable_requested',$3,'pending',jsonb_build_object(
 'environment_id',$4::text,'investigation_id',$5::text,'draft_id',$6::text,'draft_version',$7::integer,
 'draft_operation_id',$8::text,'action_digest',$9::text))`, actor.ID, record.ApplicationID, proposalID,
		env, investigation, item.DraftID, item.Version, record.ID, enableDigest("aiops.v3.proposal.action.v1", actionRaw))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return enabledResponse(draft, proposalID, "pending"), nil
}

func (s *WriteService) existingEnableIntent(ctx context.Context, actor core.User, key, requestDigest,
	investigation string, record operationRecord, draft map[string]any) (map[string]any, bool, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx)
	var proposalID, storedDigest, kind, status string
	err = tx.QueryRow(ctx, `SELECT a.proposal_id,a.request_hash,a.action_kind,p.status
 FROM v3_proposal_actions a JOIN v3_proposals p ON p.id=a.proposal_id
 WHERE a.creator_id=$1 AND a.idempotency_key=$2 FOR SHARE OF a,p`, actor.ID, key).
		Scan(&proposalID, &storedDigest, &kind, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if kind != alertRuleEnableKind || storedDigest != requestDigest {
		return nil, false, problem(409, "idempotency_conflict", "Idempotency key was used for another proposal")
	}
	intent, err := loadAlertRuleEnableIntentWithTx(ctx, tx, proposalID, false)
	if err != nil || intent.DraftOperationID != record.ID || intent.InvestigationID != investigation ||
		intent.EnvironmentID != record.EnvironmentID || intent.ApplicationID != record.ApplicationID ||
		intent.DraftID != draft["draft_id"] {
		return nil, false, problem(409, "enable_intent_changed", "Existing proposal does not match the draft")
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	return enabledResponse(draft, proposalID, status), true, nil
}

func enabledResponse(draft map[string]any, proposalID, proposalStatus string) map[string]any {
	status := "pending_approval"
	if proposalStatus == "rejected" {
		status = "rejected"
	} else if proposalStatus == "approved" {
		status = "approved_pending_execution"
	}
	return map[string]any{"draft_id": draft["draft_id"], "version": draft["version"],
		"mr": draft["mr"], "proposal_id": proposalID, "status": status}
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
	if kind != alertRuleEnableKind || schemaVersion != 1 || source != "human_request" ||
		(requirePending && status != "pending") || (status != "pending" && status != "rejected" && status != "approved") ||
		actionErr != nil || reviewErr != nil ||
		enableDigest("aiops.v3.proposal.action.v1", canonicalAction) != actionHash ||
		enableDigest("aiops.v3.proposal.review.v1", canonicalReview) != reviewHash ||
		json.Unmarshal(actionRaw, &intent) != nil || intent.Kind != kind || intent.SchemaVersion != 1 ||
		intent.EnvironmentID != env || intent.ApplicationID != app || intent.InvestigationID != investigation {
		return AlertRuleEnableIntent{}, problem(409, "enable_intent_invalid", "Alert rule enable proposal is invalid or rejected")
	}
	hash, _, err := specDigest(intent.Spec)
	if err != nil || hash != intent.SpecDigest || intent.Spec.Mode != "draft" ||
		intent.Spec.EnvironmentID != env || intent.Spec.ApplicationID != app || !hexID(intent.DraftID) || !hexID(intent.DraftOperationID) ||
		intent.ProjectID <= 0 || intent.MR <= 0 || intent.MRURL == "" || intent.DraftVersion < 1 ||
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

// A cross-kind proposal key can race the pre-export check. If that happens,
// keep the already-opened nondeployable MR visible as stale for human review.
func (s *WriteService) markOrphanedEnableDraft(ctx context.Context, actor core.User, key, digest string) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	tx, err := s.DB.Begin(cleanup)
	if err != nil {
		return err
	}
	defer tx.Rollback(cleanup)
	var opID, state, app, mrURL string
	err = tx.QueryRow(cleanup, `SELECT id,state,application_id,mr_url FROM v3_alert_rule_draft_operations
 WHERE actor_id=$1 AND operation='enable' AND idempotency_key=$2 AND request_digest=$3 FOR UPDATE`,
		actor.ID, key, digest).Scan(&opID, &state, &app, &mrURL)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var attached bool
	if err := tx.QueryRow(cleanup, `SELECT EXISTS(SELECT 1 FROM v3_proposal_actions
 WHERE creator_id=$1 AND idempotency_key=$2 AND action_kind=$3 AND request_hash=$4
 AND action->>'draft_operation_id'=$5)`,
		actor.ID, key, alertRuleEnableKind, digest, opID).Scan(&attached); err != nil {
		return err
	}
	if attached || state != "pending_review" {
		return tx.Commit(cleanup)
	}
	_, err = tx.Exec(cleanup, `UPDATE v3_alert_rule_draft_operations SET state='stale_remote',
 updated_at=clock_timestamp() WHERE id=$1 AND state='pending_review'`, opID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(cleanup, `INSERT INTO management_audit(actor_id,application_id,action,target,outcome,changes)
 VALUES($1,$2,'v3.alert_rule.enable.orphan_draft',$3,'needs_review',jsonb_build_object(
 'mr_url',$4::text,'reason','proposal_conflict_after_export'))`, actor.ID, app, opID, mrURL)
	if err != nil {
		return err
	}
	return tx.Commit(cleanup)
}
