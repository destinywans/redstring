package v3memory

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

func scoped(r *http.Request, env string) bool {
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok {
		return false
	}
	for _, id := range scope.EnvironmentIDs {
		if id == env {
			return true
		}
	}
	return false
}
func unavailable(w http.ResponseWriter) {
	httpapi.Error(w, 503, "memory_unavailable", "Memory storage or authorization is unavailable")
}
func failure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errMemorySession):
		httpapi.Error(w, 401, "unauthorized", "Session expired")
	case errors.Is(err, store.ErrForbidden):
		httpapi.Error(w, 403, "forbidden", "Memory access denied")
	case errors.Is(err, store.ErrNotFound):
		httpapi.Error(w, 404, "not_found", "Memory record not found")
	case errors.Is(err, store.ErrConflict):
		httpapi.Error(w, 409, "conflict", "Memory state changed")
	case errors.Is(err, ErrInvalid):
		httpapi.Error(w, 422, "invalid_memory_input", "Invalid memory operation")
	default:
		unavailable(w)
	}
}

var errMemorySession = errors.New("memory session expired")

// Lock the active account/session and all physical grant sources before the
// transactional policy check. A revoke that commits first is observed; one
// that starts later waits until this short governance write commits.
func lockWriteAuthority(ctx context.Context, tx pgx.Tx, r *http.Request) error {
	cookie, err := r.Cookie("aiops_session")
	if err != nil {
		return errMemorySession
	}
	var actor string
	err = tx.QueryRow(ctx, `SELECT u.id FROM users u JOIN sessions s ON s.user_id=u.id
 WHERE s.token_hash=$1 AND u.id=$2 AND s.expires_at>clock_timestamp()
 AND NOT u.disabled AND u.deleted_at IS NULL AND u.source<>'settings_demo'
 FOR SHARE OF u,s`, store.Hash(cookie.Value), httpapi.CurrentUser(r).ID).Scan(&actor)
	if errors.Is(err, pgx.ErrNoRows) {
		return errMemorySession
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `LOCK TABLE rbac_user_roles,rbac_department_roles,v3_role_grants IN SHARE MODE`)
	return err
}

// Row locks cannot stop wall-clock expiry. Check it again immediately before
// commit while still holding the session row and authorization-source locks.
func currentWriteSession(ctx context.Context, tx pgx.Tx, r *http.Request) error {
	cookie, err := r.Cookie("aiops_session")
	if err != nil {
		return errMemorySession
	}
	var valid bool
	err = tx.QueryRow(ctx, `SELECT expires_at>clock_timestamp() FROM sessions
 WHERE token_hash=$1 AND user_id=$2`, store.Hash(cookie.Value), httpapi.CurrentUser(r).ID).Scan(&valid)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !valid {
		return errMemorySession
	}
	return err
}
func scanFact(row pgx.Row) (Fact, error) {
	var f Fact
	var confirmed *time.Time
	var current bool
	err := row.Scan(&f.ID, &f.Fact, &f.Source, &f.SourceVersion, &f.State, &f.EnvironmentID, &f.ApplicationID, &f.TeamID, &confirmed, &f.CreatedAt, &f.Revision, &current)
	f.Confirmed = confirmed
	f.SourceCurrent = &current
	return f, err
}

// lockCurrentSource pins the knowledge row until the caller commits. A stale
// version, removed excerpt, archive, or changed application/team/environment
// attribution is not a current source for confirmation or conflict resolution.
func lockCurrentSource(ctx context.Context, tx pgx.Tx, factID string) (bool, error) {
	var factVersion, sourceVersion int
	var factHash, sourceHash, lifecycle, factApp, sourceApp, factTeam, env string
	var excerptPresent bool
	var metadata []byte
	err := tx.QueryRow(ctx, `SELECT f.source_version,d.version,f.source_content_hash,d.content_hash,d.lifecycle,
 f.application_id,COALESCE(d.application_id,''),f.team_id,f.environment_id,
 position(f.fact in d.body)>0,d.metadata
 FROM v3_memory_facts f JOIN knowledge_documents d ON d.id=f.source_document_id
 WHERE f.id=$1 AND d.source NOT LIKE 'runbook://v3/%'
 AND NOT EXISTS (SELECT 1 FROM v3_runbooks vr WHERE vr.document_id=d.id)
 FOR SHARE OF d`, factID).
		Scan(&factVersion, &sourceVersion, &factHash, &sourceHash, &lifecycle,
			&factApp, &sourceApp, &factTeam, &env, &excerptPresent, &metadata)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	meta, valid := parseSourceAttribution(metadata)
	if lifecycle != "published" || factVersion != sourceVersion || factHash != sourceHash ||
		!valid || factApp != sourceApp || factTeam != meta.TeamID || !has(meta.EnvironmentIDs, env) ||
		(factApp == "" && meta.Visibility != "workspace") ||
		!excerptPresent {
		return false, nil
	}
	return lockCurrentBinding(ctx, tx, factApp, factTeam, env)
}

const factSelect = `SELECT f.id,f.fact,f.source_document_id,f.source_version,f.state,f.environment_id,f.application_id,f.team_id,
 f.confirmed_at,f.created_at,f.revision,
 (d.id IS NOT NULL AND d.lifecycle='published' AND d.version=f.source_version AND d.content_hash=f.source_content_hash)
 FROM v3_memory_facts`

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.readSnapshot(r, true, false)
	if err != nil {
		readFailure(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"items": snapshot.Facts, "partial": true,
		"unavailable_fields": []string{"confidence", "recalls", "expires", "layer_counts", "policy"}})
}

func (s *Service) decide(w http.ResponseWriter, r *http.Request) {
	if !s.ready() {
		unavailable(w)
		return
	}
	action := r.PathValue("action")
	if action != "confirm" && action != "reject" && action != "expire" {
		httpapi.Error(w, 404, "not_found", "Unknown memory action")
		return
	}
	id := r.PathValue("id")
	if id == "" || len(id) > 128 {
		failure(w, ErrInvalid)
		return
	}
	user := httpapi.CurrentUser(r)
	if user.ID == "" {
		unavailable(w)
		return
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		unavailable(w)
		return
	}
	defer tx.Rollback(r.Context())
	var f Fact
	var hash string
	err = tx.QueryRow(r.Context(), `SELECT id,fact,source_document_id,source_version,source_content_hash,state,environment_id,
 application_id,team_id,created_at,revision FROM v3_memory_facts WHERE id=$1 FOR UPDATE`, id).
		Scan(&f.ID, &f.Fact, &f.Source, &f.SourceVersion, &hash, &f.State, &f.EnvironmentID, &f.ApplicationID, &f.TeamID, &f.CreatedAt, &f.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		failure(w, store.ErrNotFound)
		return
	}
	if err != nil {
		unavailable(w)
		return
	}
	if !scoped(r, f.EnvironmentID) {
		failure(w, store.ErrNotFound)
		return
	}
	bound, err := lockCurrentBinding(r.Context(), tx, f.ApplicationID, f.TeamID, f.EnvironmentID)
	if err != nil {
		unavailable(w)
		return
	}
	// Acquire the catalog row lock before the grant-source locks, but do not
	// reveal its state until current authority over the fact is established.
	if err = lockWriteAuthority(r.Context(), tx, r); err != nil {
		failure(w, err)
		return
	}
	if err = s.authorizeTx(r.Context(), tx, user, f.EnvironmentID, f.ApplicationID, f.TeamID, "manage"); err != nil {
		failure(w, err)
		return
	}
	if !bound {
		failure(w, store.ErrConflict)
		return
	}
	state, allowed := transition(f.State, action)
	if !allowed {
		failure(w, store.ErrConflict)
		return
	}
	if action == "confirm" {
		var current bool
		current, err = lockCurrentSource(r.Context(), tx, f.ID)
		if err != nil {
			unavailable(w)
			return
		}
		if !current {
			failure(w, store.ErrConflict)
			return
		}
		f.SourceCurrent = &current
	}
	previous := f.State
	err = tx.QueryRow(r.Context(), `UPDATE v3_memory_facts SET state=$2,decided_by=$3,decided_at=now(),revision=revision+1,
 confirmed_by=CASE WHEN $2='confirmed' THEN $3 ELSE confirmed_by END,
 confirmed_at=CASE WHEN $2='confirmed' THEN now() ELSE confirmed_at END
 WHERE id=$1 RETURNING confirmed_at,revision`, id, state, user.ID).Scan(&f.Confirmed, &f.Revision)
	if err != nil {
		unavailable(w)
		return
	}
	f.State = state
	if err = audit(r.Context(), tx, user.ID, f.ApplicationID, "memory.fact."+action, id, map[string]any{"environment_id": f.EnvironmentID, "from": previous, "to": state, "source_document_id": f.Source, "source_version": f.SourceVersion}); err != nil {
		unavailable(w)
		return
	}
	if err = currentWriteSession(r.Context(), tx, r); err != nil {
		failure(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		unavailable(w)
		return
	}
	if action != "confirm" {
		// Historical cleanup stays available under the original scope. Its
		// response must not reveal copied text or old source identity after a
		// source has been reclassified, revised, or archived.
		httpapi.WriteJSON(w, 200, map[string]any{
			"fact":          map[string]any{"id": f.ID, "state": f.State, "revision": f.Revision, "confirmed": f.Confirmed},
			"fact_redacted": true, "partial": true,
			"unavailable_fields": []string{"confidence", "recalls", "expires"},
		})
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"fact": f, "fact_redacted": false, "partial": true, "unavailable_fields": []string{"confidence", "recalls", "expires"}})
}

func (s *Service) resolve(w http.ResponseWriter, r *http.Request) {
	if !s.ready() {
		unavailable(w)
		return
	}
	var in struct {
		Keep string `json:"keep"`
		Drop string `json:"drop"`
	}
	if !httpapi.Decode(w, r, &in) {
		return
	}
	if in.Keep == "" || in.Drop == "" || in.Keep == in.Drop || len(in.Keep) > 128 || len(in.Drop) > 128 ||
		strings.TrimSpace(in.Keep) != in.Keep || strings.TrimSpace(in.Drop) != in.Drop {
		failure(w, ErrInvalid)
		return
	}
	user := httpapi.CurrentUser(r)
	if user.ID == "" {
		unavailable(w)
		return
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		unavailable(w)
		return
	}
	defer tx.Rollback(r.Context())
	// Lock facts in a stable order, matching RecordConflict, before locking the
	// conflict. This avoids an inverse lock order during concurrent governance.
	rows, err := tx.Query(r.Context(), `SELECT id,environment_id,application_id,team_id,state FROM v3_memory_facts
 WHERE id=ANY($1::text[]) ORDER BY id FOR UPDATE`, []string{in.Keep, in.Drop})
	if err != nil {
		unavailable(w)
		return
	}
	type side struct{ id, env, app, team, state string }
	sides := []side{}
	for rows.Next() {
		var v side
		if err = rows.Scan(&v.id, &v.env, &v.app, &v.team, &v.state); err != nil {
			break
		}
		sides = append(sides, v)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		unavailable(w)
		return
	}
	if len(sides) != 2 {
		failure(w, store.ErrNotFound)
		return
	}
	if !scoped(r, sides[0].env) || !scoped(r, sides[1].env) {
		failure(w, store.ErrNotFound)
		return
	}
	bindingsCurrent := true
	for _, side := range sides {
		bound, bindingErr := lockCurrentBinding(r.Context(), tx, side.app, side.team, side.env)
		if bindingErr != nil {
			unavailable(w)
			return
		}
		bindingsCurrent = bindingsCurrent && bound
	}
	// Defer the catalog-state response until both objects' current grants have
	// been checked. An actor with no manage grant cannot probe archive or owner
	// changes by comparing 403 with 409.
	if err = lockWriteAuthority(r.Context(), tx, r); err != nil {
		failure(w, err)
		return
	}
	for _, side := range sides {
		if err = s.authorizeTx(r.Context(), tx, user, side.env, side.app, side.team, "manage"); err != nil {
			failure(w, err)
			return
		}
	}
	if !bindingsCurrent {
		failure(w, store.ErrConflict)
		return
	}
	if sides[0].env != sides[1].env || sides[0].app != sides[1].app || sides[0].team != sides[1].team ||
		!active(sides[0].state) || !active(sides[1].state) {
		failure(w, store.ErrConflict)
		return
	}
	var conflictID, conflictEnv, conflictApp, conflictTeam string
	err = tx.QueryRow(r.Context(), `SELECT id,environment_id,application_id,team_id FROM v3_memory_conflicts WHERE state='open' AND
 ((fact_a_id=$1 AND fact_b_id=$2) OR (fact_a_id=$2 AND fact_b_id=$1)) FOR UPDATE`, in.Keep, in.Drop).
		Scan(&conflictID, &conflictEnv, &conflictApp, &conflictTeam)
	if errors.Is(err, pgx.ErrNoRows) {
		failure(w, store.ErrNotFound)
		return
	}
	if err != nil {
		unavailable(w)
		return
	}
	if conflictEnv != sides[0].env || conflictApp != sides[0].app || conflictTeam != sides[0].team {
		failure(w, store.ErrConflict)
		return
	}
	// Both sides must still come from the exact published versions that were
	// reviewed when the conflict was recorded. Lock in fact-ID order so two
	// reversed resolution requests cannot deadlock on distinct sources.
	for _, side := range sides {
		current, sourceErr := lockCurrentSource(r.Context(), tx, side.id)
		if sourceErr != nil {
			unavailable(w)
			return
		}
		if !current {
			failure(w, store.ErrConflict)
			return
		}
	}
	_, err = tx.Exec(r.Context(), `UPDATE v3_memory_facts SET state=CASE WHEN id=$1 THEN 'confirmed' ELSE 'rejected' END,
 decided_by=$3,decided_at=now(),revision=revision+1,
 confirmed_by=CASE WHEN id=$1 THEN COALESCE(confirmed_by,$3) ELSE confirmed_by END,
 confirmed_at=CASE WHEN id=$1 THEN COALESCE(confirmed_at,now()) ELSE confirmed_at END
 WHERE id=ANY($2::text[])`, in.Keep, []string{in.Keep, in.Drop}, user.ID)
	if err != nil {
		unavailable(w)
		return
	}
	_, err = tx.Exec(r.Context(), `UPDATE v3_memory_conflicts SET state='resolved',keep_fact_id=$2,drop_fact_id=$3,
 resolved_by=$4,resolved_at=now() WHERE id=$1`, conflictID, in.Keep, in.Drop, user.ID)
	if err != nil {
		unavailable(w)
		return
	}
	if err = audit(r.Context(), tx, user.ID, sides[0].app, "memory.conflict.resolved", conflictID, map[string]any{"environment_id": sides[0].env, "keep_fact_id": in.Keep, "drop_fact_id": in.Drop}); err != nil {
		unavailable(w)
		return
	}
	if err = currentWriteSession(r.Context(), tx, r); err != nil {
		failure(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		unavailable(w)
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"id": conflictID, "state": "resolved", "keep": in.Keep, "drop": in.Drop})
}
