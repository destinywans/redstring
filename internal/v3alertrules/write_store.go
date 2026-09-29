package v3alertrules

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type draftManifest struct {
	Schema             string              `json:"schema"`
	Kind               string              `json:"kind"`
	Deployable         bool                `json:"deployable"`
	QueryScopeVerified bool                `json:"query_scope_verified"`
	OperationID        string              `json:"operation_id"`
	EnvironmentID      string              `json:"environment_id"`
	ApplicationID      string              `json:"application_id"`
	Rules              []draftManifestRule `json:"rules"`
}

type draftManifestRule struct {
	DraftID                    string        `json:"draft_id"`
	Version                    int           `json:"version"`
	ProviderFileIdentityDigest string        `json:"provider_file_identity_digest"`
	ObservedRuleID             string        `json:"observed_rule_id,omitempty"`
	ObservedDigest             string        `json:"observed_digest,omitempty"`
	Spec                       DraftRuleSpec `json:"spec"`
}

type operationRecord struct {
	ID, EnvironmentID, ApplicationID, Branch, Path, State, URL string
	Payload, Response                                          []byte
	ProjectID, MR, Attempt                                     int64
}

func asJSON(v any) ([]byte, error) { return json.Marshal(v) }

func parseResponse(raw []byte) (map[string]any, error) {
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil || result == nil {
		return nil, problem(503, "draft_ledger_invalid", "Alert rule draft response is unavailable")
	}
	return result, nil
}

func responseDraftIDs(raw []byte) []string {
	response, err := parseResponse(raw)
	if err != nil {
		return nil
	}
	ids := make([]string, 0, 20)
	if id, ok := response["draft_id"].(string); ok && hexID(id) {
		ids = append(ids, id)
	}
	if candidates, ok := response["candidates"].([]any); ok {
		for _, candidate := range candidates {
			row, ok := candidate.(map[string]any)
			if !ok {
				continue
			}
			if id, ok := row["draft_id"].(string); ok && hexID(id) {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// This identifies the provider's configured file path, not its contents.
func fileIdentityDigest(file string) string {
	h := sha256.Sum256([]byte(file))
	return hex.EncodeToString(h[:])
}

func specDigest(spec DraftRuleSpec) (string, []byte, error) {
	b, err := asJSON(spec)
	if err != nil {
		return "", nil, err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), b, nil
}

func newManifest(opID, env, app string, rules []draftManifestRule) ([]byte, error) {
	encoded, err := asJSON(draftManifest{Schema: "aiops.alert_rule_draft.v1", Kind: "review_draft", Deployable: false, QueryScopeVerified: false,
		OperationID: opID, EnvironmentID: env, ApplicationID: app, Rules: rules})
	if err != nil {
		return nil, err
	}
	// PostgreSQL jsonb adds whitespace when it serializes this value again.
	// Leave headroom below GitLab's 256 KiB file ceiling.
	if len(encoded) > 128<<10 {
		return nil, problem(413, "draft_too_large", "Alert rule draft exceeds the GitOps file limit")
	}
	return encoded, nil
}

func (s *WriteService) authorize(ctx context.Context, actor core.User, env, app string) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := s.authorizeTx(ctx, tx, actor, env, app); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *WriteService) authorizeTx(ctx context.Context, tx pgx.Tx, actor core.User, env, app string) error {
	if app == "" || len(app) > 200 || env == "" {
		return problem(404, "not_found", "Application not found")
	}
	var userID, owner string
	if err := tx.QueryRow(ctx, `SELECT id FROM users WHERE id=$1 AND NOT disabled AND deleted_at IS NULL AND source<>'settings_demo' FOR SHARE`, actor.ID).Scan(&userID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return problem(403, "forbidden", "Current account is unavailable")
		}
		return err
	}
	if err := tx.QueryRow(ctx, `SELECT COALESCE(a.owner_team_id,'') FROM applications a
 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id=$2
 JOIN application_environments e ON e.application_id=a.id AND e.key=$2 AND NOT e.archived
 WHERE a.id=$1 AND NOT a.archived FOR SHARE OF a,ve,e`, app, env).Scan(&owner); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return problem(404, "not_found", "Application not found")
		}
		return err
	}
	decision := rbac.V3Decision{Module: "alert_rules", Action: "change", EnvironmentID: env, ApplicationID: app}
	err := s.Access.AuthorizeWithTx(ctx, tx, actor, decision)
	if errors.Is(err, store.ErrForbidden) && owner != "" {
		decision.ApplicationID = ""
		decision.TeamID = owner
		err = s.Access.AuthorizeWithTx(ctx, tx, actor, decision)
	}
	if errors.Is(err, store.ErrForbidden) {
		return problem(403, "forbidden", "Alert rule change permission required")
	}
	return err
}

func dbWriteProblem(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.Code == "23505" || pgErr.Code == "23514") {
		return problem(409, "draft_conflict", "Alert rule draft identity or version changed")
	}
	return err
}

func (s *WriteService) insertOperation(ctx context.Context, tx pgx.Tx, opID string, actor core.User,
	operation, key, requestDigest, env, app string, payload []byte, result map[string]any) error {
	response, err := asJSON(result)
	if err != nil {
		return err
	}
	branch := s.Repository.BranchPrefix() + opID
	path := "ops-config/alert-rule-drafts/" + opID + ".json"
	tag, err := tx.Exec(ctx, `INSERT INTO v3_alert_rule_draft_operations
 (id,actor_id,operation,idempotency_key,request_digest,environment_id,application_id,payload,response,
 branch,path,project_id,state)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9::jsonb,$10,$11,$12,'reserved')
 ON CONFLICT(actor_id,operation,idempotency_key) DO NOTHING`, opID, actor.ID, operation, key, requestDigest, env, app,
		payload, response, branch, path, s.Repository.ProjectID())
	if err != nil {
		return dbWriteProblem(err)
	}
	if tag.RowsAffected() != 1 {
		return problem(409, "idempotency_conflict", "Idempotency request is already in progress; retry the same key")
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,application_id,action,target,outcome,changes)
 VALUES($1,$2,'v3.alert_rule_draft.reserved',$3,'pending',jsonb_build_object(
 'environment_id',$4::text,'operation',$5::text,'project_id',$6::bigint))`, actor.ID, app, opID, env, operation, s.Repository.ProjectID())
	return err
}

func (s *WriteService) insertRevision(ctx context.Context, tx pgx.Tx, draftID string, version int, opID string,
	actor core.User, spec DraftRuleSpec) error {
	digest, encoded, err := specDigest(spec)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO v3_alert_rule_draft_revisions(draft_id,version,operation_id,spec,spec_digest,actor_id)
 VALUES($1,$2,$3,$4::jsonb,$5,$6)`, draftID, version, opID, encoded, digest, actor.ID)
	return dbWriteProblem(err)
}

func draftResult(id string, version int) map[string]any {
	return map[string]any{"draft_id": id, "version": version, "status": "pending_review"}
}

func (s *WriteService) reserveCreate(ctx context.Context, actor core.User, env, key, digest string, spec DraftRuleSpec,
	group boundGroup, observed observedIdentity) (map[string]any, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err := s.authorizeTx(ctx, tx, actor, env, spec.ApplicationID); err != nil {
		return nil, err
	}
	opID, draftID := store.ID(), store.ID()
	manifest, err := newManifest(opID, env, spec.ApplicationID, []draftManifestRule{{DraftID: draftID, Version: 1,
		ProviderFileIdentityDigest: fileIdentityDigest(group.file), ObservedRuleID: spec.ObservedRuleID, ObservedDigest: observed.digest, Spec: spec}})
	if err != nil {
		return nil, err
	}
	result := draftResult(draftID, 1)
	if err := s.insertOperation(ctx, tx, opID, actor, "create", key, digest, env, spec.ApplicationID, manifest, result); err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO v3_alert_rule_drafts
 (id,environment_id,application_id,provider_file,provider_group,alert_name,observed_rule_id,observed_digest,current_version,created_by)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,1,$9)`, draftID, env, spec.ApplicationID, group.file, group.group, spec.Name,
		spec.ObservedRuleID, observed.digest, actor.ID)
	if err != nil {
		return nil, dbWriteProblem(err)
	}
	if err := s.insertRevision(ctx, tx, draftID, 1, opID, actor, spec); err != nil {
		return nil, err
	}
	return result, tx.Commit(ctx)
}

func (s *WriteService) reserveUpdate(ctx context.Context, actor core.User, env, key, digest, id string, input updateRequest,
	group boundGroup, observed observedIdentity) (map[string]any, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err := s.authorizeTx(ctx, tx, actor, env, input.Rule.ApplicationID); err != nil {
		return nil, err
	}
	var storedEnv, storedApp, storedFile, storedGroup, storedName, storedObservedID, storedObservedDigest string
	var version int
	err = tx.QueryRow(ctx, `SELECT environment_id,application_id,provider_file,provider_group,alert_name,
 observed_rule_id,observed_digest,current_version FROM v3_alert_rule_drafts WHERE id=$1 FOR UPDATE`, id).
		Scan(&storedEnv, &storedApp, &storedFile, &storedGroup, &storedName, &storedObservedID, &storedObservedDigest, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, problem(404, "not_found", "Draft not found")
	}
	if err != nil {
		return nil, err
	}
	if storedEnv != env || storedApp != input.Rule.ApplicationID || storedFile != group.file ||
		storedGroup != input.Rule.Group || storedName != input.Rule.Name || version != input.ExpectedVersion {
		return nil, problem(409, "draft_version_conflict", "Draft identity or version changed")
	}
	var previousState string
	if err := tx.QueryRow(ctx, `SELECT o.state FROM v3_alert_rule_draft_revisions v
 JOIN v3_alert_rule_draft_operations o ON o.id=v.operation_id
 WHERE v.draft_id=$1 AND v.version=$2`, id, version).Scan(&previousState); err != nil {
		return nil, err
	}
	if previousState != "pending_review" && previousState != "rejected" && previousState != "stale_remote" {
		return nil, problem(409, "draft_busy", "Previous draft revision has no confirmed pending review MR")
	}
	newObservedID, newObservedDigest := storedObservedID, storedObservedDigest
	if storedObservedID != "" {
		if !observed.exists && previousState != "pending_review" && input.ObservedRule == nil && input.Rule.ObservedRuleID == "" {
			newObservedID, newObservedDigest = "", ""
		} else {
			if input.Rule.ObservedRuleID != storedObservedID || input.ObservedRule == nil || input.ObservedRule.ID != storedObservedID || input.ObservedRule.Group != storedGroup ||
				input.ObservedRule.Name != storedName || !observed.exists || observed.id != storedObservedID ||
				(previousState == "pending_review" && observed.digest != storedObservedDigest) {
				return nil, problem(409, "source_changed", "Observed source rule changed")
			}
			newObservedDigest = observed.digest
		}
	} else if observed.exists {
		if previousState == "pending_review" || input.ObservedRule == nil || input.ObservedRule.ID != observed.id ||
			input.ObservedRule.Group != storedGroup || input.ObservedRule.Name != storedName || input.Rule.ObservedRuleID != observed.id {
			return nil, problem(409, "source_changed", "A rule now occupies the draft identity")
		}
		newObservedID, newObservedDigest = observed.id, observed.digest
	} else if input.ObservedRule != nil || input.Rule.ObservedRuleID != "" {
		return nil, problem(409, "source_changed", "Observed rule identity is unavailable")
	}
	opID, newVersion := store.ID(), version+1
	manifest, err := newManifest(opID, env, storedApp, []draftManifestRule{{DraftID: id, Version: newVersion,
		ProviderFileIdentityDigest: fileIdentityDigest(group.file), ObservedRuleID: newObservedID, ObservedDigest: newObservedDigest, Spec: input.Rule}})
	if err != nil {
		return nil, err
	}
	result := draftResult(id, newVersion)
	if err := s.insertOperation(ctx, tx, opID, actor, "update", key, digest, env, storedApp, manifest, result); err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `UPDATE v3_alert_rule_drafts SET current_version=$2,observed_rule_id=$3,observed_digest=$4,
 updated_at=now() WHERE id=$1`, id, newVersion, newObservedID, newObservedDigest)
	if err != nil {
		return nil, err
	}
	if err := s.insertRevision(ctx, tx, id, newVersion, opID, actor, input.Rule); err != nil {
		return nil, err
	}
	return result, tx.Commit(ctx)
}

func (s *WriteService) reserveImport(ctx context.Context, actor core.User, env, key, digest, app string,
	rules []DraftRuleSpec, groups []boundGroup, observed []observedIdentity) (map[string]any, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err := s.authorizeTx(ctx, tx, actor, env, app); err != nil {
		return nil, err
	}
	opID := store.ID()
	manifestRules := make([]draftManifestRule, 0, len(rules))
	candidates := make([]map[string]any, 0, len(rules))
	seen := map[string]bool{}
	for i, spec := range rules {
		identity := groups[i].file + "\x00" + spec.Group + "\x00" + spec.Name
		if seen[identity] {
			return nil, problem(422, "duplicate_yaml_rule", "The YAML repeats a rule identity")
		}
		seen[identity] = true
		candidate := map[string]any{"group": spec.Group, "name": spec.Name}
		var existing string
		err := tx.QueryRow(ctx, `SELECT id FROM v3_alert_rule_drafts
 WHERE environment_id=$1 AND application_id=$2 AND provider_file=$3 AND provider_group=$4 AND alert_name=$5`,
			env, app, groups[i].file, spec.Group, spec.Name).Scan(&existing)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		if observed[i].exists || err == nil {
			if err == nil {
				var state string
				if err := tx.QueryRow(ctx, `SELECT o.state FROM v3_alert_rule_draft_revisions v
 JOIN v3_alert_rule_draft_operations o ON o.id=v.operation_id
 JOIN v3_alert_rule_drafts d ON d.id=v.draft_id AND d.current_version=v.version
 WHERE d.id=$1`, existing).Scan(&state); err != nil || state != "pending_review" {
					return nil, problem(409, "draft_requires_revision", "Matching draft "+existing+" needs a reviewed revision")
				}
			}
			candidate["outcome"] = "duplicate"
			candidate["status"] = "duplicate_skipped"
			candidates = append(candidates, candidate)
			continue
		}
		draftID := store.ID()
		candidate["outcome"] = "new"
		candidate["status"] = "pending_review"
		candidate["draft_id"] = draftID
		candidate["version"] = 1
		candidates = append(candidates, candidate)
		manifestRules = append(manifestRules, draftManifestRule{DraftID: draftID, Version: 1,
			ProviderFileIdentityDigest: fileIdentityDigest(groups[i].file), Spec: spec})
	}
	if len(manifestRules) == 0 {
		result := map[string]any{"status": "duplicates_only", "candidates": candidates}
		payload, err := asJSON(map[string]any{"schema": "aiops.alert_rule_duplicate_report.v1", "operation_id": opID,
			"environment_id": env, "application_id": app})
		if err != nil {
			return nil, err
		}
		if err := s.insertOperation(ctx, tx, opID, actor, "import", key, digest, env, app, payload, result); err != nil {
			return nil, err
		}
		_, err = tx.Exec(ctx, `UPDATE v3_alert_rule_draft_operations SET state='duplicates_only',updated_at=clock_timestamp() WHERE id=$1`, opID)
		if err != nil {
			return nil, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,application_id,action,target,outcome,changes)
 VALUES($1,$2,'v3.alert_rule_draft.duplicates_only',$3,'no_change',jsonb_build_object(
 'environment_id',$4::text,'candidate_count',$5::integer))`, actor.ID, app, opID, env, len(candidates))
		if err != nil {
			return nil, err
		}
		return result, tx.Commit(ctx)
	}
	manifest, err := newManifest(opID, env, app, manifestRules)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"status": "pending_review", "candidates": candidates}
	if err := s.insertOperation(ctx, tx, opID, actor, "import", key, digest, env, app, manifest, result); err != nil {
		return nil, err
	}
	for _, item := range manifestRules {
		var group boundGroup
		for j, candidate := range candidates {
			if candidate["draft_id"] == item.DraftID {
				group = groups[j]
				break
			}
		}
		_, err = tx.Exec(ctx, `INSERT INTO v3_alert_rule_drafts
 (id,environment_id,application_id,provider_file,provider_group,alert_name,current_version,created_by)
 VALUES($1,$2,$3,$4,$5,$6,1,$7)`, item.DraftID, env, app, group.file, group.group, item.Spec.Name, actor.ID)
		if err != nil {
			return nil, dbWriteProblem(err)
		}
		if err := s.insertRevision(ctx, tx, item.DraftID, 1, opID, actor, item.Spec); err != nil {
			return nil, err
		}
	}
	return result, tx.Commit(ctx)
}

func (s *WriteService) replay(ctx context.Context, actor core.User, operation, key, digest, env, app string) (map[string]any, bool, error) {
	var storedDigest, storedEnv, storedApp string
	err := s.DB.QueryRow(ctx, `SELECT request_digest,environment_id,application_id FROM v3_alert_rule_draft_operations
 WHERE actor_id=$1 AND operation=$2 AND idempotency_key=$3`, actor.ID, operation, key).
		Scan(&storedDigest, &storedEnv, &storedApp)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, err
	}
	if storedDigest != digest || storedEnv != env || storedApp != app {
		return nil, true, problem(409, "idempotency_conflict", "Idempotency key was used for another request")
	}
	result, err := s.export(ctx, actor, operation, key, digest, nil)
	return result, true, err
}

func (s *WriteService) claim(ctx context.Context, actor core.User, operation, key, digest string) (operationRecord, bool, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return operationRecord{}, false, err
	}
	defer tx.Rollback(ctx)
	var record operationRecord
	err = tx.QueryRow(ctx, `SELECT id,environment_id,application_id,branch,path,project_id,payload,response,state,
 COALESCE(mr_iid,0),mr_url,attempt_generation FROM v3_alert_rule_draft_operations
 WHERE actor_id=$1 AND operation=$2 AND idempotency_key=$3 FOR UPDATE`, actor.ID, operation, key).
		Scan(&record.ID, &record.EnvironmentID, &record.ApplicationID, &record.Branch, &record.Path, &record.ProjectID,
			&record.Payload, &record.Response, &record.State, &record.MR, &record.URL, &record.Attempt)
	if errors.Is(err, pgx.ErrNoRows) {
		return record, false, problem(409, "draft_missing", "Reserved draft operation is missing")
	}
	if err != nil {
		return record, false, err
	}
	var storedDigest string
	if err = tx.QueryRow(ctx, `SELECT request_digest FROM v3_alert_rule_draft_operations WHERE id=$1`, record.ID).Scan(&storedDigest); err != nil {
		return record, false, err
	}
	if storedDigest != digest {
		return record, false, problem(409, "idempotency_conflict", "Idempotency key was used for another payload")
	}
	if err = s.authorizeTx(ctx, tx, actor, record.EnvironmentID, record.ApplicationID); err != nil {
		return record, false, err
	}
	if record.ProjectID != s.Repository.ProjectID() || record.Branch != s.Repository.BranchPrefix()+record.ID ||
		record.Path != "ops-config/alert-rule-drafts/"+record.ID+".json" || len(record.Payload) > 256<<10 || !json.Valid(record.Payload) {
		return record, false, problem(503, "draft_ledger_invalid", "Reserved draft identity is invalid")
	}
	if record.State == "pending_review" {
		if record.MR <= 0 || record.URL == "" {
			return record, false, problem(503, "draft_ledger_invalid", "Reviewed draft MR identity is missing")
		}
		return record, true, tx.Commit(ctx)
	}
	if record.State == "duplicates_only" {
		return record, true, tx.Commit(ctx)
	}
	if record.State == "stale_remote" {
		return record, false, problem(409, "draft_requires_review", "Remote MR "+record.URL+" is stale; revise draft IDs "+strings.Join(responseDraftIDs(record.Response), ",")+" and close the MR after review")
	}
	if record.State == "exporting" {
		var active bool
		if err = tx.QueryRow(ctx, `SELECT COALESCE(lease_until>clock_timestamp(),false)
 FROM v3_alert_rule_draft_operations WHERE id=$1`, record.ID).Scan(&active); err != nil {
			return record, false, err
		}
		if active {
			return record, false, problem(409, "draft_busy", "Draft export is already in progress")
		}
	}
	err = tx.QueryRow(ctx, `UPDATE v3_alert_rule_draft_operations SET state='exporting',
 attempt_generation=attempt_generation+1,lease_until=clock_timestamp()+interval '1 minute',updated_at=clock_timestamp()
 WHERE id=$1 RETURNING attempt_generation`, record.ID).Scan(&record.Attempt)
	if err != nil {
		return record, false, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,application_id,action,target,outcome,changes)
 VALUES($1,$2,'v3.alert_rule_draft.export_requested',$3,'pending',jsonb_build_object(
 'environment_id',$4::text,'attempt_generation',$5::bigint))`, actor.ID, record.ApplicationID, record.ID,
		record.EnvironmentID, record.Attempt)
	if err != nil {
		return record, false, err
	}
	return record, false, tx.Commit(ctx)
}

func (s *WriteService) export(ctx context.Context, actor core.User, operation, key, digest string,
	_ map[string]any) (map[string]any, error) {
	record, done, err := s.claim(ctx, actor, operation, key, digest)
	if err != nil {
		return nil, err
	}
	if done {
		return parseResponse(record.Response)
	}
	// These checks are adjacent to the external write. A provider or grant can
	// still change during GitLab I/O; the post-write checks record that race.
	if err := s.authorize(ctx, actor, record.EnvironmentID, record.ApplicationID); err != nil {
		var p *writeProblem
		if errors.As(err, &p) && (p.status == 403 || p.status == 404) {
			if markErr := s.markInvalid(ctx, actor, record, "rejected", nil, "authorization_changed_before_export"); markErr != nil {
				return nil, markErr
			}
		} else if markErr := s.releaseBeforeExport(ctx, actor, record); markErr != nil {
			return nil, markErr
		}
		return nil, err
	}
	if err := s.recheckManifestSources(ctx, record); err != nil {
		var p *writeProblem
		if errors.As(err, &p) && p.status == 409 {
			if markErr := s.markInvalid(ctx, actor, record, "rejected", nil, "source_changed_before_export"); markErr != nil {
				return nil, markErr
			}
		} else {
			if markErr := s.releaseBeforeExport(ctx, actor, record); markErr != nil {
				return nil, markErr
			}
		}
		return nil, err
	}
	mr, err := s.Repository.ExportDraft(ctx, record.Branch, record.Path, record.Payload, "Review alert rule draft "+record.ID)
	if err != nil {
		if markErr := s.markUnknown(ctx, actor, record); markErr != nil {
			return nil, markErr
		}
		if errors.Is(err, ErrDraftRemoteConflict) {
			return nil, problem(409, "gitops_conflict", "GitOps draft branch or content conflicts")
		}
		return nil, problem(503, "gitops_unavailable", "GitOps draft result is uncertain; retry the same key")
	}
	if mr.IID <= 0 || mr.URL == "" || mr.State != "opened" || mr.SourceBranch != record.Branch ||
		mr.SourceProjectID != record.ProjectID || mr.TargetProjectID != record.ProjectID {
		if markErr := s.markUnknown(ctx, actor, record); markErr != nil {
			return nil, markErr
		}
		return nil, problem(503, "gitops_untrusted", "Pending GitOps merge request could not be verified")
	}
	if err := s.recheckManifestSources(ctx, record); err != nil {
		var p *writeProblem
		if errors.As(err, &p) && p.status == 409 {
			if markErr := s.markInvalid(ctx, actor, record, "stale_remote", &mr, "source_changed_after_export"); markErr != nil {
				return nil, markErr
			}
		} else if markErr := s.markUnknown(ctx, actor, record); markErr != nil {
			return nil, markErr
		}
		return nil, err
	}
	result, err := s.finish(ctx, actor, record, mr)
	if err != nil {
		var p *writeProblem
		if errors.As(err, &p) && (p.status == 403 || p.status == 404) {
			if markErr := s.markInvalid(ctx, actor, record, "stale_remote", &mr, "authorization_changed_after_export"); markErr != nil {
				return nil, markErr
			}
		}
		return nil, err
	}
	return result, nil
}

func (s *WriteService) releaseBeforeExport(ctx context.Context, actor core.User, record operationRecord) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	tx, err := s.DB.Begin(cleanup)
	if err != nil {
		return err
	}
	defer tx.Rollback(cleanup)
	tag, err := tx.Exec(cleanup, `UPDATE v3_alert_rule_draft_operations SET state='reserved',lease_until=NULL,
 updated_at=clock_timestamp() WHERE id=$1 AND state='exporting' AND attempt_generation=$2`, record.ID, record.Attempt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return problem(409, "draft_conflict", "Draft export attempt changed")
	}
	_, err = tx.Exec(cleanup, `INSERT INTO management_audit(actor_id,application_id,action,target,outcome,changes)
 VALUES($1,$2,'v3.alert_rule_draft.pre_export_unavailable',$3,'deferred',jsonb_build_object(
 'attempt_generation',$4::bigint))`, actor.ID, record.ApplicationID, record.ID, record.Attempt)
	if err != nil {
		return err
	}
	return tx.Commit(cleanup)
}

func (s *WriteService) markInvalid(ctx context.Context, actor core.User, record operationRecord,
	state string, mr *DraftMR, reason string) error {
	if state != "rejected" && state != "stale_remote" {
		return errors.New("invalid draft terminal state")
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	tx, err := s.DB.Begin(cleanup)
	if err != nil {
		return err
	}
	defer tx.Rollback(cleanup)
	var iid int64
	var url string
	if mr != nil {
		iid, url = mr.IID, mr.URL
	}
	tag, err := tx.Exec(cleanup, `UPDATE v3_alert_rule_draft_operations SET state=$3,mr_iid=NULLIF($4::bigint,0),
 mr_url=$5,lease_until=NULL,updated_at=clock_timestamp()
 WHERE id=$1 AND state='exporting' AND attempt_generation=$2`, record.ID, record.Attempt, state, iid, url)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return problem(409, "draft_conflict", "Draft export attempt changed")
	}
	_, err = tx.Exec(cleanup, `INSERT INTO management_audit(actor_id,application_id,action,target,outcome,changes)
 VALUES($1,$2,'v3.alert_rule_draft.rejected',$3,$4,jsonb_build_object(
 'reason',$5::text,'attempt_generation',$6::bigint,'project_id',$7::bigint,'mr_iid',$8::bigint))`,
		actor.ID, record.ApplicationID, record.ID, state, reason, record.Attempt, record.ProjectID, iid)
	if err != nil {
		return err
	}
	return tx.Commit(cleanup)
}

// A provider read can race both reservation and remote GitLab writes. A draft
// with changed source identity must never be reported as a confirmed review.
func (s *WriteService) recheckManifestSources(ctx context.Context, record operationRecord) error {
	var manifest draftManifest
	if err := json.Unmarshal(record.Payload, &manifest); err != nil || manifest.OperationID != record.ID ||
		manifest.EnvironmentID != record.EnvironmentID || manifest.ApplicationID != record.ApplicationID ||
		manifest.Deployable || len(manifest.Rules) == 0 || len(manifest.Rules) > 20 {
		return problem(503, "draft_ledger_invalid", "Reserved draft content is invalid")
	}
	specs := make([]DraftRuleSpec, len(manifest.Rules))
	for i, item := range manifest.Rules {
		specs[i] = item.Spec
	}
	groups, observed, err := s.inspectCandidates(ctx, record.EnvironmentID, record.ApplicationID, specs)
	if err != nil {
		return err
	}
	for i, item := range manifest.Rules {
		if item.ProviderFileIdentityDigest != fileIdentityDigest(groups[i].file) || item.ObservedRuleID != observed[i].id ||
			item.ObservedDigest != observed[i].digest {
			return problem(409, "source_changed", "Bound rule source changed during draft export")
		}
	}
	return nil
}

func (s *WriteService) markUnknown(ctx context.Context, actor core.User, record operationRecord) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	tx, err := s.DB.Begin(cleanup)
	if err != nil {
		return err
	}
	defer tx.Rollback(cleanup)
	tag, err := tx.Exec(cleanup, `UPDATE v3_alert_rule_draft_operations SET state='uncertain',lease_until=NULL,
 updated_at=clock_timestamp() WHERE id=$1 AND state='exporting' AND attempt_generation=$2`, record.ID, record.Attempt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return problem(409, "draft_conflict", "Draft export attempt changed")
	}
	_, err = tx.Exec(cleanup, `INSERT INTO management_audit(actor_id,application_id,action,target,outcome,changes)
 VALUES($1,$2,'v3.alert_rule_draft.result_unknown',$3,'unknown',jsonb_build_object('attempt_generation',$4::bigint))`,
		actor.ID, record.ApplicationID, record.ID, record.Attempt)
	if err != nil {
		return err
	}
	return tx.Commit(cleanup)
}

func (s *WriteService) finish(ctx context.Context, actor core.User, record operationRecord, mr DraftMR) (map[string]any, error) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	tx, err := s.DB.Begin(cleanup)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(cleanup)
	var state string
	var attempt int64
	err = tx.QueryRow(cleanup, `SELECT state,attempt_generation FROM v3_alert_rule_draft_operations WHERE id=$1 FOR UPDATE`, record.ID).
		Scan(&state, &attempt)
	if err != nil {
		return nil, err
	}
	if state != "exporting" || attempt != record.Attempt {
		return nil, problem(409, "draft_conflict", "Draft export attempt changed")
	}
	if err = s.authorizeTx(cleanup, tx, actor, record.EnvironmentID, record.ApplicationID); err != nil {
		return nil, err
	}
	result, err := parseResponse(record.Response)
	if err != nil {
		return nil, err
	}
	result["mr"] = projectMR(record.ProjectID, mr)
	encoded, err := asJSON(result)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(cleanup, `UPDATE v3_alert_rule_draft_operations SET state='pending_review',response=$2::jsonb,
 mr_iid=$3,mr_url=$4,lease_until=NULL,updated_at=clock_timestamp() WHERE id=$1 AND attempt_generation=$5`,
		record.ID, encoded, mr.IID, mr.URL, record.Attempt)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(cleanup, `INSERT INTO management_audit(actor_id,application_id,action,target,outcome,changes)
 VALUES($1,$2,'v3.alert_rule_draft.pending_review',$3,'success',jsonb_build_object(
 'project_id',$4::bigint,'mr_iid',$5::bigint,'attempt_generation',$6::bigint))`,
		actor.ID, record.ApplicationID, record.ID, record.ProjectID, mr.IID, record.Attempt)
	if err != nil {
		return nil, err
	}
	return result, tx.Commit(cleanup)
}
