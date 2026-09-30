package v3alertmanager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3configsync"
)

func inhibitionDraftPath(id string) string { return "ops-config/inhibitions/" + id + ".json" }

// claimInhibitionDraft locks current application ownership, session, and grants in
// the same transaction as the intent/attempt audit. A nil payload only claims
// a previously reserved identity; it never creates an unverified draft.
func (s *Service) claimInhibitionDraft(r *http.Request, db *pgxpool.Pool, access routeTxAccess,
	env string, app Application, providerApp, key, requestHash, revision string, payload []byte, id string) (inhibitionDraftRecord, bool, error) {
	ctx := r.Context()
	tx, err := db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return inhibitionDraftRecord{}, false, err
	}
	defer tx.Rollback(ctx)
	if err = lockInhibitionAuthority(r, tx, access, env, app); err != nil {
		return inhibitionDraftRecord{}, false, err
	}
	actor := httpapi.CurrentUser(r)
	if payload != nil {
		branch := s.InhibitionRepository.BranchPrefix() + id
		path := inhibitionDraftPath(id)
		if len(branch) > 128 || len(branch) == 0 {
			return inhibitionDraftRecord{}, false, errInhibitionDraftUntrusted
		}
		_, err = tx.Exec(ctx, `INSERT INTO v3_inhibition_drafts
 (id,actor_id,environment_id,application_id,source_revision,idempotency_key,request_hash,
 payload,branch,target_branch,path,project_id,state)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9,$10,$11,$12,'reserved')
 ON CONFLICT(actor_id,idempotency_key) DO NOTHING`, id, actor.ID, env, app.ID, revision,
			key, requestHash, payload, branch, s.InhibitionRepository.TargetBranch(), path, s.InhibitionRepository.ProjectID())
		if err != nil {
			return inhibitionDraftRecord{}, false, err
		}
	}
	var record inhibitionDraftRecord
	var storedHash, storedEnv, storedApp string
	err = tx.QueryRow(ctx, `SELECT id,environment_id,application_id,source_revision,request_hash,
 branch,target_branch,path,project_id,payload,state,COALESCE(mr_iid,0),mr_url,attempt_generation
 FROM v3_inhibition_drafts WHERE actor_id=$1 AND idempotency_key=$2 FOR UPDATE`, actor.ID, key).
		Scan(&record.ID, &storedEnv, &storedApp, &record.SourceRevision, &storedHash,
			&record.Branch, &record.TargetBranch, &record.Path, &record.ProjectID, &record.Payload, &record.State,
			&record.MR, &record.URL, &record.Attempt)
	if errors.Is(err, pgx.ErrNoRows) && payload == nil {
		if err = tx.Commit(ctx); err != nil {
			return inhibitionDraftRecord{}, false, err
		}
		return inhibitionDraftRecord{}, false, nil
	}
	if err != nil {
		return inhibitionDraftRecord{}, false, err
	}
	if storedHash != requestHash || storedEnv != env || storedApp != app.ID ||
		record.SourceRevision != revision || record.ProjectID != s.InhibitionRepository.ProjectID() ||
		record.Branch != s.InhibitionRepository.BranchPrefix()+record.ID ||
		record.TargetBranch != s.InhibitionRepository.TargetBranch() || record.Path != inhibitionDraftPath(record.ID) ||
		len(record.Payload) == 0 || len(record.Payload) > 64<<10 {
		return inhibitionDraftRecord{}, false, store.ErrConflict
	}
	var saved inhibitionDraftManifest
	if decodeStrictInhibitionManifest(record.Payload, &saved) != nil {
		return inhibitionDraftRecord{}, false, store.ErrConflict
	}
	compiled, validationErr := compileInhibitionDraft(saved.Input, s.EnvironmentLabels[env], env, providerApp)
	if saved.Schema != "aiops.inhibition-draft/v1" ||
		saved.DraftID != record.ID || saved.EnvironmentID != env || saved.ApplicationID != app.ID ||
		saved.ApplicationLabel != providerApp || saved.SourceRevision != revision ||
		saved.Status != "pending_review" || saved.Promotion != "manual_review_and_separate_activation_required" ||
		validationErr != nil || inhibitionDraftRequestHash(env, revision, saved.Input) != storedHash ||
		!reflect.DeepEqual(saved.EffectiveSource, compiled.effectiveSource) ||
		!reflect.DeepEqual(saved.EffectiveTarget, compiled.effectiveTarget) {
		return inhibitionDraftRecord{}, false, store.ErrConflict
	}
	if record.State == "exported" {
		if record.MR <= 0 || record.URL == "" {
			return inhibitionDraftRecord{}, false, errInhibitionDraftUntrusted
		}
		if err = tx.Commit(ctx); err != nil {
			return inhibitionDraftRecord{}, false, err
		}
		return record, true, nil
	}
	if record.State == "exporting" {
		var busy bool
		if err = tx.QueryRow(ctx, `SELECT COALESCE(lease_until>clock_timestamp(),false)
 FROM v3_inhibition_drafts WHERE id=$1`, record.ID).Scan(&busy); err != nil {
			return inhibitionDraftRecord{}, false, err
		}
		if busy {
			return inhibitionDraftRecord{}, false, errInhibitionDraftBusy
		}
	}
	err = tx.QueryRow(ctx, `UPDATE v3_inhibition_drafts SET state='exporting',
 attempt_generation=attempt_generation+1,lease_until=clock_timestamp()+interval '1 minute',
 updated_at=clock_timestamp() WHERE id=$1 RETURNING attempt_generation`, record.ID).Scan(&record.Attempt)
	if err != nil {
		return inhibitionDraftRecord{}, false, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes)
 VALUES($1,'v3.inhibition_draft.requested',$2,'pending',jsonb_build_object(
 'environment_id',$3::text,'application_id',$4::text,'source_revision',$5::text,
 'project_id',$6::bigint,'target_branch',$7::text,'attempt_generation',$8::bigint))`, actor.ID, record.ID, env,
		app.ID, revision, record.ProjectID, record.TargetBranch, record.Attempt)
	if err != nil {
		return inhibitionDraftRecord{}, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return inhibitionDraftRecord{}, false, err
	}
	return record, true, nil
}

func decodeStrictInhibitionManifest(payload []byte, out *inhibitionDraftManifest) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return store.ErrConflict
	}
	return nil
}

func (s *Service) exportInhibitionDraft(w http.ResponseWriter, r *http.Request, record inhibitionDraftRecord,
	db *pgxpool.Pool, access routeTxAccess, env string, app Application) {
	actor := httpapi.CurrentUser(r)
	if record.State != "exported" {
		mr, attempted, err := s.exportInhibitionDraftAuthorized(r, record, db, access, env, app)
		if !attempted {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
			defer cancel()
			if cleanupErr := s.markInhibitionDraftNotSent(cleanupCtx, db, actor, record); cleanupErr != nil {
				httpapi.Error(w, http.StatusServiceUnavailable, "inhibition_draft_unavailable", "Draft export did not start, but its retry state could not be confirmed")
				return
			}
			writeInhibitionDraftError(w, err)
			return
		}
		if err != nil || mr.IID <= 0 || mr.URL == "" || mr.State != "opened" ||
			mr.SourceBranch != record.Branch || mr.SourceProjectID != record.ProjectID ||
			mr.TargetProjectID != record.ProjectID ||
			mr.TargetBranch != record.TargetBranch {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
			defer cancel()
			if markErr := s.markInhibitionDraftUnknown(cleanupCtx, db, actor, record); markErr != nil {
				writeInhibitionDraftError(w, markErr)
				return
			}
			if errors.Is(err, v3configsync.ErrConflict) {
				httpapi.Error(w, http.StatusConflict, "inhibition_draft_conflict", "The GitOps draft identity conflicts with remote content")
			} else {
				httpapi.Error(w, http.StatusServiceUnavailable, "inhibition_export_unknown", "Draft review export could not be confirmed; retry with the same idempotency key")
			}
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
		defer cancel()
		if err = s.finishInhibitionDraft(cleanupCtx, db, actor, record, mr); err != nil {
			writeInhibitionDraftError(w, err)
			return
		}
		record.MR, record.URL, record.State = mr.IID, mr.URL, "exported"
	}
	if err := inhibitionFinalAuthority(r, s, env, app); err != nil {
		writeInhibitionDraftError(w, err)
		return
	}
	response := inhibitionDraftResponse{ID: record.ID, Status: "pending_review", StatusBasis: "local_submission",
		Effect: "no_runtime_change_by_this_endpoint", SourceRevision: record.SourceRevision}
	response.Review.ProjectID, response.Review.MergeRequestIID, response.Review.URL = record.ProjectID, record.MR, record.URL
	httpapi.WriteJSON(w, http.StatusAccepted, response)
}

// A failed preflight made no GitLab request. Release only this actor's attempt
// so an authorized session can retry with the same idempotency key.
func (s *Service) markInhibitionDraftNotSent(ctx context.Context, db *pgxpool.Pool, actor core.User, record inhibitionDraftRecord) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	result, err := tx.Exec(ctx, `UPDATE v3_inhibition_drafts SET state='reserved',lease_until=NULL,
 updated_at=clock_timestamp() WHERE id=$1 AND actor_id=$2 AND state='exporting' AND attempt_generation=$3`,
		record.ID, actor.ID, record.Attempt)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return store.ErrConflict
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes)
 VALUES($1,'v3.inhibition_draft.export_not_sent',$2,'failure',jsonb_build_object(
 'attempt_generation',$3::bigint,'external_request_started',false))`, actor.ID, record.ID, record.Attempt)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Keep the authorization locks through the bounded GitLab write. The draft
// row also prevents an expired attempt from exporting after another request
// has reclaimed the same idempotency key.
func (s *Service) exportInhibitionDraftAuthorized(r *http.Request, record inhibitionDraftRecord,
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
	if err = lockInhibitionAuthority(r.WithContext(ctx), tx, access, env, app); err != nil {
		return v3configsync.MR{}, false, err
	}
	var state, branch, target, path string
	var attempt, projectID int64
	var leaseActive, payloadMatches bool
	err = tx.QueryRow(ctx, `SELECT state,attempt_generation,branch,target_branch,path,project_id,
 COALESCE(lease_until>clock_timestamp(),false),payload=$3::jsonb
 FROM v3_inhibition_drafts WHERE id=$1 AND actor_id=$2 FOR UPDATE`,
		record.ID, httpapi.CurrentUser(r).ID, string(record.Payload)).
		Scan(&state, &attempt, &branch, &target, &path, &projectID, &leaseActive, &payloadMatches)
	if err != nil {
		return v3configsync.MR{}, false, err
	}
	if state != "exporting" || attempt != record.Attempt || !leaseActive || !payloadMatches ||
		branch != record.Branch || target != record.TargetBranch || path != record.Path || projectID != record.ProjectID {
		return v3configsync.MR{}, false, store.ErrConflict
	}
	// The session must outlive the 30-second operation with a one-second margin.
	// Row locks prevent logout but cannot prevent natural expiry.
	cookie, _ := r.Cookie("aiops_session")
	var sessionLongEnough bool
	err = tx.QueryRow(ctx, `SELECT expires_at>clock_timestamp()+interval '31 seconds'
 FROM sessions WHERE token_hash=$1 AND user_id=$2`,
		store.Hash(cookie.Value), httpapi.CurrentUser(r).ID).Scan(&sessionLongEnough)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !sessionLongEnough {
		return v3configsync.MR{}, false, store.ErrSessionRevoked
	}
	if err != nil {
		return v3configsync.MR{}, false, err
	}
	mr, exportErr := s.InhibitionRepository.Export(ctx, record.Branch, record.Path, record.Payload,
		"Review inhibition rule draft "+record.ID)
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

func (s *Service) markInhibitionDraftUnknown(ctx context.Context, db *pgxpool.Pool, actor core.User, record inhibitionDraftRecord) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	result, err := tx.Exec(ctx, `UPDATE v3_inhibition_drafts SET state='uncertain',lease_until=NULL,
 updated_at=clock_timestamp() WHERE id=$1 AND state='exporting' AND attempt_generation=$2`, record.ID, record.Attempt)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return store.ErrConflict
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes)
 VALUES($1,'v3.inhibition_draft.result_unknown',$2,'unknown',jsonb_build_object(
 'attempt_generation',$3::bigint))`, actor.ID, record.ID, record.Attempt)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) finishInhibitionDraft(ctx context.Context, db *pgxpool.Pool, actor core.User, record inhibitionDraftRecord, mr v3configsync.MR) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	result, err := tx.Exec(ctx, `UPDATE v3_inhibition_drafts SET state='exported',mr_iid=$3,mr_url=$4,
 lease_until=NULL,updated_at=clock_timestamp() WHERE id=$1 AND state='exporting' AND attempt_generation=$2`,
		record.ID, record.Attempt, mr.IID, mr.URL)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return store.ErrConflict
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes)
 VALUES($1,'v3.inhibition_draft.review_created',$2,'success',jsonb_build_object(
 'project_id',$3::bigint,'mr_iid',$4::bigint,'attempt_generation',$5::bigint))`,
		actor.ID, record.ID, record.ProjectID, mr.IID, record.Attempt)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func writeInhibitionDraftError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrSessionRevoked):
		httpapi.Error(w, http.StatusUnauthorized, "unauthorized", "Session expired")
	case errors.Is(err, store.ErrForbidden):
		httpapi.Error(w, http.StatusForbidden, "forbidden", "Inhibition access changed")
	case errors.Is(err, store.ErrNotFound):
		httpapi.Error(w, http.StatusNotFound, "application_not_found", "Bound application is unavailable")
	case errors.Is(err, store.ErrConflict):
		httpapi.Error(w, http.StatusConflict, "inhibition_draft_conflict", "Draft identity or ownership changed")
	case errors.Is(err, errInhibitionDraftBusy):
		httpapi.Error(w, http.StatusConflict, "inhibition_draft_busy", "This draft is already being exported")
	default:
		httpapi.Error(w, http.StatusServiceUnavailable, "inhibition_draft_unavailable", "Inhibition draft could not be saved or confirmed")
	}
}
