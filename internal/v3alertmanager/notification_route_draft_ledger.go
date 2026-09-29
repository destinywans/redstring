package v3alertmanager

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3configsync"
)

func routeDraftPath(id string) string { return "ops-config/notification-routes/" + id + ".json" }

// claimRouteDraft locks current application ownership, session, and grants in
// the same transaction as the intent/attempt audit. A nil payload only claims
// a previously reserved identity; it never creates an unverified draft.
func (s *Service) claimRouteDraft(r *http.Request, db *pgxpool.Pool, access routeTxAccess,
	env string, app Application, providerApp, key, requestHash, revision string, payload []byte, id string) (routeDraftRecord, bool, error) {
	ctx := r.Context()
	tx, err := db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return routeDraftRecord{}, false, err
	}
	defer tx.Rollback(ctx)
	if err = lockRouteDraftAuthority(r, tx, access, env, app); err != nil {
		return routeDraftRecord{}, false, err
	}
	actor := httpapi.CurrentUser(r)
	if payload != nil {
		branch := s.RouteRepository.BranchPrefix() + id
		path := routeDraftPath(id)
		if len(branch) > 128 || len(branch) == 0 {
			return routeDraftRecord{}, false, errRouteDraftUntrusted
		}
		_, err = tx.Exec(ctx, `INSERT INTO v3_notification_route_drafts
 (id,actor_id,environment_id,application_id,source_revision,idempotency_key,request_hash,
 payload,branch,target_branch,path,project_id,state)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9,$10,$11,$12,'reserved')
 ON CONFLICT(actor_id,idempotency_key) DO NOTHING`, id, actor.ID, env, app.ID, revision,
			key, requestHash, payload, branch, s.RouteRepository.TargetBranch(), path, s.RouteRepository.ProjectID())
		if err != nil {
			return routeDraftRecord{}, false, err
		}
	}
	var record routeDraftRecord
	var storedHash, storedEnv, storedApp string
	err = tx.QueryRow(ctx, `SELECT id,environment_id,application_id,source_revision,request_hash,
 branch,target_branch,path,project_id,payload,state,COALESCE(mr_iid,0),mr_url,attempt_generation
 FROM v3_notification_route_drafts WHERE actor_id=$1 AND idempotency_key=$2 FOR UPDATE`, actor.ID, key).
		Scan(&record.ID, &storedEnv, &storedApp, &record.SourceRevision, &storedHash,
			&record.Branch, &record.TargetBranch, &record.Path, &record.ProjectID, &record.Payload, &record.State,
			&record.MR, &record.URL, &record.Attempt)
	if errors.Is(err, pgx.ErrNoRows) && payload == nil {
		if err = tx.Commit(ctx); err != nil {
			return routeDraftRecord{}, false, err
		}
		return routeDraftRecord{}, false, nil
	}
	if err != nil {
		return routeDraftRecord{}, false, err
	}
	if storedHash != requestHash || storedEnv != env || storedApp != app.ID ||
		record.SourceRevision != revision || record.ProjectID != s.RouteRepository.ProjectID() ||
		record.Branch != s.RouteRepository.BranchPrefix()+record.ID ||
		record.TargetBranch != s.RouteRepository.TargetBranch() || record.Path != routeDraftPath(record.ID) ||
		len(record.Payload) == 0 || len(record.Payload) > 64<<10 {
		return routeDraftRecord{}, false, store.ErrConflict
	}
	var saved routeDraftManifest
	if json.Unmarshal(record.Payload, &saved) != nil || saved.Schema != "aiops.notification-route-draft/v1" ||
		saved.DraftID != record.ID || saved.EnvironmentID != env || saved.ApplicationID != app.ID ||
		saved.ApplicationLabel != providerApp || saved.SourceRevision != revision ||
		saved.Status != "pending_review" || saved.ParentRef != "route-root" ||
		saved.Placement != "manual_order_review_required" ||
		saved.ReceiverResolution != "environment_tree_unique_preorder_v1" ||
		routeDraftRequestHash(env, revision, saved.Route) != storedHash ||
		len(saved.EffectiveMatchers) != len(saved.Route.Matchers)+2 ||
		!reflect.DeepEqual(saved.EffectiveMatchers[0], routeDraftMatcher{K: s.EnvironmentLabels[env], Op: "=", V: env}) ||
		!reflect.DeepEqual(saved.EffectiveMatchers[1], routeDraftMatcher{K: "application", Op: "=", V: providerApp}) ||
		!reflect.DeepEqual(saved.EffectiveMatchers[2:], saved.Route.Matchers) {
		return routeDraftRecord{}, false, store.ErrConflict
	}
	if record.State == "exported" {
		if record.MR <= 0 || record.URL == "" {
			return routeDraftRecord{}, false, errRouteDraftUntrusted
		}
		if err = tx.Commit(ctx); err != nil {
			return routeDraftRecord{}, false, err
		}
		return record, true, nil
	}
	if record.State == "exporting" {
		var busy bool
		if err = tx.QueryRow(ctx, `SELECT COALESCE(lease_until>clock_timestamp(),false)
 FROM v3_notification_route_drafts WHERE id=$1`, record.ID).Scan(&busy); err != nil {
			return routeDraftRecord{}, false, err
		}
		if busy {
			return routeDraftRecord{}, false, errRouteDraftBusy
		}
	}
	err = tx.QueryRow(ctx, `UPDATE v3_notification_route_drafts SET state='exporting',
 attempt_generation=attempt_generation+1,lease_until=clock_timestamp()+interval '1 minute',
 updated_at=clock_timestamp() WHERE id=$1 RETURNING attempt_generation`, record.ID).Scan(&record.Attempt)
	if err != nil {
		return routeDraftRecord{}, false, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes)
 VALUES($1,'v3.notification_route_draft.requested',$2,'pending',jsonb_build_object(
 'environment_id',$3::text,'application_id',$4::text,'source_revision',$5::text,
 'project_id',$6::bigint,'target_branch',$7::text,'attempt_generation',$8::bigint))`, actor.ID, record.ID, env,
		app.ID, revision, record.ProjectID, record.TargetBranch, record.Attempt)
	if err != nil {
		return routeDraftRecord{}, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return routeDraftRecord{}, false, err
	}
	return record, true, nil
}

func lockRouteDraftAuthority(r *http.Request, tx pgx.Tx, access routeTxAccess, env string, expected Application) error {
	ctx := r.Context()
	var current Application
	err := tx.QueryRow(ctx, `SELECT a.id,ve.environment_id,COALESCE(a.owner_team_id,'')
 FROM applications a JOIN v3_application_environments ve ON ve.application_id=a.id
 JOIN application_environments e ON e.application_id=a.id AND e.key=ve.environment_id AND NOT e.archived
 WHERE a.id=$1 AND ve.environment_id=$2 AND NOT a.archived FOR SHARE OF a,ve,e`, expected.ID, env).
		Scan(&current.ID, &current.EnvironmentID, &current.OwnerTeamID)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && current != expected {
		return store.ErrNotFound
	}
	if err != nil {
		return err
	}
	cookie, err := r.Cookie("aiops_session")
	if err != nil {
		return store.ErrSessionRevoked
	}
	user := httpapi.CurrentUser(r)
	if err = store.LockActiveSession(ctx, tx, cookie.Value, user.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `LOCK TABLE rbac_user_roles,rbac_department_roles,rbac_roles,v3_role_grants IN SHARE MODE`); err != nil {
		return err
	}
	for _, action := range []string{"view", "manage"} {
		if err = access.AuthorizeWithTx(ctx, tx, user, rbac.V3Decision{Module: "alert_rules", Action: action, EnvironmentID: env}); err != nil {
			return err
		}
	}
	var unexpired bool
	err = tx.QueryRow(ctx, `SELECT expires_at>clock_timestamp() FROM sessions WHERE token_hash=$1 AND user_id=$2`,
		store.Hash(cookie.Value), user.ID).Scan(&unexpired)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !unexpired {
		return store.ErrSessionRevoked
	}
	return err
}

func (s *Service) exportRouteDraft(w http.ResponseWriter, r *http.Request, record routeDraftRecord,
	db *pgxpool.Pool, access routeTxAccess, env string, app Application) {
	actor := httpapi.CurrentUser(r)
	if record.State != "exported" {
		mr, attempted, err := s.exportRouteDraftAuthorized(r, record, db, access, env, app)
		if !attempted {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
			defer cancel()
			if cleanupErr := s.releaseUnattemptedRouteDraft(cleanupCtx, db, actor, record); cleanupErr != nil {
				httpapi.Error(w, http.StatusServiceUnavailable, "route_draft_unavailable", "Draft export did not start, but its retry state could not be confirmed")
				return
			}
			writeRouteDraftError(w, err)
			return
		}
		if err != nil || mr.IID <= 0 || mr.URL == "" || mr.State != "opened" ||
			mr.SourceBranch != record.Branch || mr.SourceProjectID != record.ProjectID ||
			mr.TargetProjectID != record.ProjectID ||
			mr.TargetBranch != record.TargetBranch {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
			defer cancel()
			if markErr := s.markRouteDraftUnknown(cleanupCtx, db, actor, record); markErr != nil {
				writeRouteDraftError(w, markErr)
				return
			}
			if errors.Is(err, v3configsync.ErrConflict) {
				httpapi.Error(w, http.StatusConflict, "route_draft_conflict", "The GitOps draft identity conflicts with remote content")
			} else {
				httpapi.Error(w, http.StatusServiceUnavailable, "route_export_unknown", "Draft review export could not be confirmed; retry with the same idempotency key")
			}
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
		defer cancel()
		if err = s.finishRouteDraft(cleanupCtx, db, actor, record, mr); err != nil {
			writeRouteDraftError(w, err)
			return
		}
		record.MR, record.URL, record.State = mr.IID, mr.URL, "exported"
	}
	if err := routeTestFinalAuthority(r, db, access, env, app); err != nil {
		writeRouteDraftError(w, err)
		return
	}
	response := routeDraftResponse{ID: record.ID, Status: "pending_review", StatusBasis: "local_submission",
		Effect: "no_runtime_change_by_this_endpoint", SourceRevision: record.SourceRevision}
	response.Review.ProjectID, response.Review.MergeRequestIID, response.Review.URL = record.ProjectID, record.MR, record.URL
	httpapi.WriteJSON(w, http.StatusAccepted, response)
}

// A failed preflight has made no GitLab request. Release only this attempt's
// lease so a newly authorized session can retry the same idempotency key.
func (s *Service) releaseUnattemptedRouteDraft(ctx context.Context, db *pgxpool.Pool, actor core.User, record routeDraftRecord) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	result, err := tx.Exec(ctx, `UPDATE v3_notification_route_drafts SET state='reserved',lease_until=NULL,
 updated_at=clock_timestamp() WHERE id=$1 AND actor_id=$2 AND state='exporting' AND attempt_generation=$3`,
		record.ID, actor.ID, record.Attempt)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return store.ErrConflict
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes)
 VALUES($1,'v3.notification_route_draft.not_exported',$2,'failure',jsonb_build_object(
 'attempt_generation',$3::bigint,'external_request_started',false))`, actor.ID, record.ID, record.Attempt)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// The GitLab write is bounded and ordered before any concurrent grant revoke
// or explicit logout. The draft row also prevents an expired lease attempt
// from exporting after another request has claimed the same identity.
func (s *Service) exportRouteDraftAuthorized(r *http.Request, record routeDraftRecord,
	db *pgxpool.Pool, access routeTxAccess, env string, app Application) (v3configsync.MR, bool, error) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return v3configsync.MR{}, false, err
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
		defer cleanupCancel()
		_ = tx.Rollback(cleanupCtx)
	}()
	if err = lockRouteDraftAuthority(r.WithContext(ctx), tx, access, env, app); err != nil {
		return v3configsync.MR{}, false, err
	}
	var state, branch, target, path string
	var attempt, projectID int64
	var leaseActive, payloadMatches bool
	err = tx.QueryRow(ctx, `SELECT state,attempt_generation,branch,target_branch,path,project_id,
 COALESCE(lease_until>clock_timestamp(),false),payload=$3::jsonb
 FROM v3_notification_route_drafts WHERE id=$1 AND actor_id=$2 FOR UPDATE`,
		record.ID, httpapi.CurrentUser(r).ID, string(record.Payload)).
		Scan(&state, &attempt, &branch, &target, &path, &projectID, &leaseActive, &payloadMatches)
	if err != nil {
		return v3configsync.MR{}, false, err
	}
	if state != "exporting" || attempt != record.Attempt || !leaseActive || !payloadMatches ||
		branch != record.Branch || target != record.TargetBranch || path != record.Path || projectID != record.ProjectID {
		return v3configsync.MR{}, false, store.ErrConflict
	}
	// Export can involve several HTTP calls. Require the Cookie to outlive the
	// entire 30-second operation plus a margin, since row locks cannot prevent
	// natural session expiry while the remote write is in flight.
	cookie, _ := r.Cookie("aiops_session")
	var sessionLongEnough bool
	err = tx.QueryRow(ctx, `SELECT expires_at>clock_timestamp()+interval '31 seconds'
 FROM sessions WHERE token_hash=$1 AND user_id=$2`,
		store.Hash(cookie.Value), httpapi.CurrentUser(r).ID).Scan(&sessionLongEnough)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !sessionLongEnough) {
		return v3configsync.MR{}, false, store.ErrSessionRevoked
	}
	if err != nil {
		return v3configsync.MR{}, false, err
	}
	mr, exportErr := s.RouteRepository.Export(ctx, record.Branch, record.Path, record.Payload,
		"Review notification route draft "+record.ID)
	var sessionStillActive bool
	checkErr := tx.QueryRow(ctx, `SELECT expires_at>clock_timestamp() FROM sessions
 WHERE token_hash=$1 AND user_id=$2`, store.Hash(cookie.Value), httpapi.CurrentUser(r).ID).Scan(&sessionStillActive)
	if checkErr != nil || !sessionStillActive {
		if checkErr == nil || errors.Is(checkErr, pgx.ErrNoRows) {
			checkErr = store.ErrSessionRevoked
		}
		return mr, true, checkErr
	}
	if err = tx.Commit(ctx); err != nil {
		return mr, true, err
	}
	return mr, true, exportErr
}

func (s *Service) markRouteDraftUnknown(ctx context.Context, db *pgxpool.Pool, actor core.User, record routeDraftRecord) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	result, err := tx.Exec(ctx, `UPDATE v3_notification_route_drafts SET state='uncertain',lease_until=NULL,
 updated_at=clock_timestamp() WHERE id=$1 AND state='exporting' AND attempt_generation=$2`, record.ID, record.Attempt)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return store.ErrConflict
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes)
 VALUES($1,'v3.notification_route_draft.result_unknown',$2,'unknown',jsonb_build_object(
 'attempt_generation',$3::bigint))`, actor.ID, record.ID, record.Attempt)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) finishRouteDraft(ctx context.Context, db *pgxpool.Pool, actor core.User, record routeDraftRecord, mr v3configsync.MR) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	result, err := tx.Exec(ctx, `UPDATE v3_notification_route_drafts SET state='exported',mr_iid=$3,mr_url=$4,
 lease_until=NULL,updated_at=clock_timestamp() WHERE id=$1 AND state='exporting' AND attempt_generation=$2`,
		record.ID, record.Attempt, mr.IID, mr.URL)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return store.ErrConflict
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes)
 VALUES($1,'v3.notification_route_draft.review_created',$2,'success',jsonb_build_object(
 'project_id',$3::bigint,'mr_iid',$4::bigint,'attempt_generation',$5::bigint))`,
		actor.ID, record.ID, record.ProjectID, mr.IID, record.Attempt)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func writeRouteDraftError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrSessionRevoked):
		httpapi.Error(w, http.StatusUnauthorized, "unauthorized", "Session expired")
	case errors.Is(err, store.ErrForbidden):
		httpapi.Error(w, http.StatusForbidden, "forbidden", "Routing access changed")
	case errors.Is(err, store.ErrNotFound):
		httpapi.Error(w, http.StatusNotFound, "application_not_found", "Bound application is unavailable")
	case errors.Is(err, store.ErrConflict):
		httpapi.Error(w, http.StatusConflict, "route_draft_conflict", "Draft identity or ownership changed")
	case errors.Is(err, errRouteDraftBusy):
		httpapi.Error(w, http.StatusConflict, "route_draft_busy", "This draft is already being exported")
	default:
		httpapi.Error(w, http.StatusServiceUnavailable, "route_draft_unavailable", "Route draft could not be saved or confirmed")
	}
}
