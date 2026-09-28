package v3memory

import (
	"context"
	"encoding/json"
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
func (s *Service) view(w http.ResponseWriter, r *http.Request) {
	// MemoryView requires complete layer, recall, confidence and expiry
	// measurements. The existing sources do not record them. A partial endpoint
	// below provides the facts that can be established from persisted records.
	httpapi.WriteJSON(w, 503, map[string]any{"error": map[string]any{
		"code":             "memory_view_unavailable",
		"message":          "The complete MemoryView cannot be derived from persisted sources",
		"partial_endpoint": "/api/v1/memory/facts",
	}})
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
	var factHash, sourceHash, lifecycle, factApp, sourceApp, factTeam, env, excerpt, body string
	var metadata []byte
	err := tx.QueryRow(ctx, `SELECT f.source_version,d.version,f.source_content_hash,d.content_hash,d.lifecycle,
 f.application_id,COALESCE(d.application_id,''),f.team_id,f.environment_id,f.fact,d.body,d.metadata
 FROM v3_memory_facts f JOIN knowledge_documents d ON d.id=f.source_document_id
 WHERE f.id=$1 FOR SHARE OF d`, factID).
		Scan(&factVersion, &sourceVersion, &factHash, &sourceHash, &lifecycle,
			&factApp, &sourceApp, &factTeam, &env, &excerpt, &body, &metadata)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var meta struct {
		EnvironmentIDs []string `json:"environment_ids"`
		TeamID         string   `json:"team_id"`
	}
	if err = json.Unmarshal(metadata, &meta); err != nil {
		return false, err
	}
	if lifecycle != "published" || factVersion != sourceVersion || factHash != sourceHash ||
		factApp != sourceApp || factTeam != meta.TeamID || !has(meta.EnvironmentIDs, env) ||
		!strings.Contains(body, excerpt) {
		return false, nil
	}
	if factApp != "" {
		var mapped bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_application_environments
 WHERE application_id=$1 AND environment_id=$2)`, factApp, env).Scan(&mapped)
		if err != nil {
			return false, err
		}
		if !mapped {
			return false, nil
		}
	}
	return true, nil
}

const factSelect = `SELECT f.id,f.fact,f.source_document_id,f.source_version,f.state,f.environment_id,f.application_id,f.team_id,
 f.confirmed_at,f.created_at,f.revision,
 (d.id IS NOT NULL AND d.lifecycle='published' AND d.version=f.source_version AND d.content_hash=f.source_content_hash)
 FROM v3_memory_facts`

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	if !s.ready() {
		unavailable(w)
		return
	}
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	user := httpapi.CurrentUser(r)
	if !ok || len(scope.EnvironmentIDs) == 0 || user.ID == "" {
		unavailable(w)
		return
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		unavailable(w)
		return
	}
	defer tx.Rollback(r.Context())
	rows, err := tx.Query(r.Context(), factSelect+` f JOIN knowledge_documents d ON d.id=f.source_document_id
 WHERE f.environment_id=ANY($1::text[])
 AND d.lifecycle='published' AND d.version=f.source_version AND d.content_hash=f.source_content_hash
 AND COALESCE(d.application_id,'')=f.application_id
 AND COALESCE(d.metadata->>'team_id','')=f.team_id
 AND jsonb_typeof(d.metadata->'environment_ids')='array'
 AND d.metadata->'environment_ids' ? f.environment_id
 AND position(f.fact in d.body)>0
 AND (f.application_id='' OR EXISTS (SELECT 1 FROM v3_application_environments ae
   WHERE ae.application_id=f.application_id AND ae.environment_id=f.environment_id))
 AND (f.application_id='' OR EXISTS (
   SELECT 1 FROM v3_effective_role_grants g
   WHERE g.user_id=$2 AND g.module='knowledge' AND g.action='view'
   AND ((g.scope_kind='global' AND g.scope_id='*')
      OR (g.scope_kind='environment' AND g.scope_id=f.environment_id)
      OR (f.application_id<>'' AND g.scope_kind='application' AND g.scope_id=f.application_id))
 ))
 AND (f.team_id='' OR EXISTS (
   SELECT 1 FROM v3_effective_role_grants g
   WHERE g.user_id=$2 AND g.module='knowledge' AND g.action='view'
   AND ((g.scope_kind='global' AND g.scope_id='*')
      OR (g.scope_kind='environment' AND g.scope_id=f.environment_id)
      OR (g.scope_kind='team' AND g.scope_id=f.team_id))
 ))
 AND (f.application_id<>'' OR f.team_id<>'' OR EXISTS (
   SELECT 1 FROM v3_effective_role_grants g
   WHERE g.user_id=$2 AND g.module='knowledge' AND g.action='view'
   AND ((g.scope_kind='global' AND g.scope_id='*')
      OR (g.scope_kind='environment' AND g.scope_id=f.environment_id))
 ))
 ORDER BY f.created_at DESC,f.id DESC LIMIT 501 FOR SHARE OF d`, scope.EnvironmentIDs, user.ID)
	if err != nil {
		unavailable(w)
		return
	}
	candidates := []Fact{}
	for rows.Next() {
		f, e := scanFact(rows)
		if e != nil {
			err = e
			break
		}
		candidates = append(candidates, f)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		unavailable(w)
		return
	}
	if len(candidates) > 500 {
		httpapi.Error(w, 413, "scope_too_large", "Memory facts exceed the bounded view")
		return
	}
	items := []Fact{}
	for _, f := range candidates {
		err = s.authorizeTx(r.Context(), tx, user, f.EnvironmentID, f.ApplicationID, f.TeamID, "view")
		if errors.Is(err, store.ErrForbidden) {
			continue
		}
		if err != nil {
			unavailable(w)
			return
		}
		items = append(items, f)
	}
	if err = tx.Commit(r.Context()); err != nil {
		unavailable(w)
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"items": items, "partial": true, "unavailable_fields": []string{"confidence", "recalls", "expires", "layer_counts", "policy"}})
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
	if err = s.authorizeTx(r.Context(), tx, user, f.EnvironmentID, f.ApplicationID, f.TeamID, "manage"); err != nil {
		failure(w, err)
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
	if !scoped(r, sides[0].env) {
		failure(w, store.ErrNotFound)
		return
	}
	if sides[0].env != sides[1].env || sides[0].app != sides[1].app || sides[0].team != sides[1].team ||
		!active(sides[0].state) || !active(sides[1].state) {
		failure(w, store.ErrConflict)
		return
	}
	if err = s.authorizeTx(r.Context(), tx, user, sides[0].env, sides[0].app, sides[0].team, "manage"); err != nil {
		failure(w, err)
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
	// Resolving in favor of a fact also confirms it. A revised or archived
	// source cannot be silently promoted by the conflict path.
	var sourceCurrent bool
	sourceCurrent, err = lockCurrentSource(r.Context(), tx, in.Keep)
	if err != nil {
		unavailable(w)
		return
	}
	if !sourceCurrent {
		failure(w, store.ErrConflict)
		return
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
	if err = tx.Commit(r.Context()); err != nil {
		unavailable(w)
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"id": conflictID, "state": "resolved", "keep": in.Keep, "drop": in.Drop})
}
