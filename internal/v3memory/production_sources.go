package v3memory

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

type sourceInput struct {
	RequestKey    string `json:"request_key"`
	ApplicationID string `json:"application_id"`
	Title         string `json:"title"`
	Body          string `json:"body"`
}
type sourceVersionInput struct {
	ExpectedVersion     int    `json:"expected_version"`
	ExpectedContentHash string `json:"expected_content_hash"`
}
type sourceResult struct {
	DocumentID    string `json:"document_id"`
	Version       int    `json:"version"`
	ContentHash   string `json:"content_hash"`
	EnvironmentID string `json:"environment_id"`
	ApplicationID string `json:"application_id"`
	TeamID        string `json:"team_id,omitempty"`
	Lifecycle     string `json:"lifecycle"`
	Created       bool   `json:"created"`
}

func validSourceInput(in sourceInput) bool {
	return len(in.RequestKey) >= 8 && len(in.RequestKey) <= 128 && strings.TrimSpace(in.RequestKey) == in.RequestKey &&
		in.ApplicationID != "" && len(in.ApplicationID) <= 128 && strings.TrimSpace(in.ApplicationID) == in.ApplicationID &&
		strings.TrimSpace(in.Title) != "" && utf8.RuneCountInString(in.Title) <= 200 && strings.TrimSpace(in.Title) == in.Title && !strings.ContainsRune(in.Title, 0) &&
		strings.TrimSpace(in.Body) != "" && len(in.Body) <= 16384 && utf8.ValidString(in.Body) && !strings.ContainsRune(in.Body, 0)
}
func (s *Service) publishSource(w http.ResponseWriter, r *http.Request) {
	if !s.ready() {
		unavailable(w)
		return
	}
	env, ok := productionEnvironment(r)
	if !ok {
		failure(w, ErrInvalid)
		return
	}
	var in sourceInput
	if decodeProduction(w, r, &in) != nil || !validSourceInput(in) {
		failure(w, ErrInvalid)
		return
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		unavailable(w)
		return
	}
	defer tx.Rollback(r.Context())
	var team string
	err = tx.QueryRow(r.Context(), `SELECT COALESCE(a.owner_team_id,'') FROM applications a JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id=$2 JOIN application_environments e ON e.application_id=a.id AND e.key=$2 WHERE a.id=$1 AND NOT a.archived AND NOT e.archived FOR SHARE OF a,ve,e`, in.ApplicationID, env).Scan(&team)
	if errors.Is(err, pgx.ErrNoRows) {
		failure(w, store.ErrNotFound)
		return
	}
	if err != nil {
		unavailable(w)
		return
	}
	if err = productionAuthority(r, tx); err != nil {
		failure(w, err)
		return
	}
	user := httpapi.CurrentUser(r)
	for _, action := range []string{"view", "manage"} {
		if err = s.authorizeTx(r.Context(), tx, user, env, in.ApplicationID, team, action); err != nil {
			failure(w, err)
			return
		}
	}
	key, _ := json.Marshal([]string{user.ID, env, in.RequestKey})
	id := "memory-source-" + store.Hash(string(key))
	metadata := map[string]any{"origin": "human_memory_source", "actor_id": user.ID, "maintainer_id": user.ID, "visibility": "application", "environment_ids": []string{env}, "publication": "explicit_human"}
	if team != "" {
		metadata["team_id"] = team
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		unavailable(w)
		return
	}
	hash := store.Hash(in.Body)
	tag, err := tx.Exec(r.Context(), `INSERT INTO knowledge_documents(id,application_id,title,body,source,version,content_hash,created_by,lifecycle,metadata) VALUES($1,$2,$3,$4,$5,1,$6,$7,'published',$8) ON CONFLICT(id) DO NOTHING`, id, in.ApplicationID, in.Title, in.Body, "memory://manual/"+id, hash, user.ID, raw)
	if err != nil {
		unavailable(w)
		return
	}
	created := tag.RowsAffected() == 1
	var same bool
	if err = tx.QueryRow(r.Context(), `SELECT application_id=$2 AND title=$3 AND body=$4 AND source=$5 AND version=1 AND content_hash=$6 AND created_by=$7 AND lifecycle='published' AND metadata=$8::jsonb FROM knowledge_documents WHERE id=$1 FOR SHARE`, id, in.ApplicationID, in.Title, in.Body, "memory://manual/"+id, hash, user.ID, raw).Scan(&same); err != nil {
		unavailable(w)
		return
	}
	if !same {
		failure(w, store.ErrConflict)
		return
	}
	if created {
		// Record the governed document's version history, independently of vectors.
		if _, err = tx.Exec(r.Context(), `INSERT INTO knowledge_revisions(document_id,version,snapshot,created_by) SELECT id,version,jsonb_build_object('id',id,'application_id',application_id,'title',title,'body',body,'source',source,'version',version,'content_hash',content_hash,'created_at',created_at,'lifecycle',lifecycle,'metadata',metadata),created_by FROM knowledge_documents WHERE id=$1`, id); err != nil {
			unavailable(w)
			return
		}
		if _, err = tx.Exec(r.Context(), `INSERT INTO knowledge_history(document_id,version,actor_id,operation) VALUES($1,1,$2,'memory.source.published')`, id, user.ID); err != nil {
			unavailable(w)
			return
		}
		if err = audit(r.Context(), tx, user.ID, in.ApplicationID, "memory.source.published", id, map[string]any{"environment_id": env, "content_hash": hash, "version": 1, "publication": "explicit_human"}); err != nil {
			unavailable(w)
			return
		}
	}
	if err = currentWriteSession(r.Context(), tx, r); err != nil {
		failure(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		unavailable(w)
		return
	}
	status := 200
	if created {
		status = 201
	}
	w.Header().Set("Cache-Control", "private, no-store")
	httpapi.WriteJSON(w, status, sourceResult{DocumentID: id, Version: 1, ContentHash: hash, EnvironmentID: env, ApplicationID: in.ApplicationID, TeamID: team, Lifecycle: "published", Created: created})
}

func (s *Service) withdrawSource(w http.ResponseWriter, r *http.Request) {
	if !s.ready() {
		unavailable(w)
		return
	}
	env, ok := productionEnvironment(r)
	if !ok {
		failure(w, ErrInvalid)
		return
	}
	var in sourceVersionInput
	if decodeProduction(w, r, &in) != nil || in.ExpectedVersion < 1 || len(in.ExpectedContentHash) != 64 {
		failure(w, ErrInvalid)
		return
	}
	id := r.PathValue("id")
	if len(id) > 128 || !strings.HasPrefix(id, "memory-source-") {
		failure(w, ErrInvalid)
		return
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		unavailable(w)
		return
	}
	defer tx.Rollback(r.Context())
	// Serialize publication-derived facts with withdrawal before either obtains
	// source/fact rows. Otherwise an idempotent INSERT could wait on a fact while
	// holding the source SHARE lock needed by withdrawal's archive operation.
	if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "v3-memory-source:"+id); err != nil {
		unavailable(w)
		return
	}
	// Match governance's fact -> catalog -> authority -> source order. The first
	// source read is only discovery; all security fields are rechecked under lock.
	var app string
	var raw []byte
	err = tx.QueryRow(r.Context(), `SELECT COALESCE(application_id,''),metadata FROM knowledge_documents WHERE id=$1 AND source=$2`, id, "memory://manual/"+id).Scan(&app, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		failure(w, store.ErrNotFound)
		return
	}
	if err != nil {
		unavailable(w)
		return
	}
	meta, valid := parseSourceAttribution(raw)
	if !valid || !has(meta.EnvironmentIDs, env) || app == "" {
		failure(w, store.ErrNotFound)
		return
	}
	rows, err := tx.Query(r.Context(), `SELECT id FROM v3_memory_facts WHERE source_document_id=$1 AND environment_id=$2 AND application_id=$3 AND team_id=$4 AND state IN ('candidate','confirmed') ORDER BY id LIMIT 501 FOR UPDATE`, id, env, app, meta.TeamID)
	if err != nil {
		unavailable(w)
		return
	}
	ids := []string{}
	for rows.Next() {
		var factID string
		if err = rows.Scan(&factID); err != nil {
			break
		}
		ids = append(ids, factID)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		unavailable(w)
		return
	}
	if len(ids) > 500 {
		httpapi.Error(w, 413, "scope_too_large", "Withdraw facts individually before withdrawing this source")
		return
	}
	bound, err := lockCurrentBinding(r.Context(), tx, app, meta.TeamID, env)
	if err != nil {
		unavailable(w)
		return
	}
	if err = productionAuthority(r, tx); err != nil {
		failure(w, err)
		return
	}
	user := httpapi.CurrentUser(r)
	for _, action := range []string{"view", "manage"} {
		if err = s.authorizeTx(r.Context(), tx, user, env, app, meta.TeamID, action); err != nil {
			failure(w, err)
			return
		}
	}
	var version int
	var hash, lifecycle, currentApp, origin string
	var currentRaw []byte
	err = tx.QueryRow(r.Context(), `SELECT version,content_hash,lifecycle,COALESCE(application_id,''),metadata,metadata->>'origin' FROM knowledge_documents WHERE id=$1 AND source=$2 FOR UPDATE`, id, "memory://manual/"+id).Scan(&version, &hash, &lifecycle, &currentApp, &currentRaw, &origin)
	if err != nil {
		unavailable(w)
		return
	}
	currentMeta, currentValid := parseSourceAttribution(currentRaw)
	if !bound || !currentValid || currentApp != app || currentMeta.TeamID != meta.TeamID || len(currentMeta.EnvironmentIDs) != 1 || currentMeta.EnvironmentIDs[0] != env || origin != "human_memory_source" || version != in.ExpectedVersion || hash != in.ExpectedContentHash || (lifecycle != "published" && lifecycle != "archived") {
		failure(w, store.ErrConflict)
		return
	}
	// A publisher may have committed another candidate after our first fact scan.
	// Holding the source exclusive now excludes further production; decline this
	// attempt if an unreviewed active fact appeared instead of silently missing it.
	var unseen bool
	if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM v3_memory_facts WHERE source_document_id=$1 AND state IN ('candidate','confirmed') AND NOT(id=ANY($2::text[])))`, id, ids).Scan(&unseen); err != nil {
		unavailable(w)
		return
	}
	if unseen {
		failure(w, store.ErrConflict)
		return
	}
	changed := lifecycle == "published" || len(ids) > 0
	if changed {
		if _, err = tx.Exec(r.Context(), `UPDATE knowledge_documents SET lifecycle='archived',active_generation=NULL WHERE id=$1`, id); err != nil {
			unavailable(w)
			return
		}
		if _, err = tx.Exec(r.Context(), `UPDATE v3_memory_facts SET state='expired',decided_by=$2,decided_at=now(),revision=revision+1 WHERE id=ANY($1::text[])`, ids, user.ID); err != nil {
			unavailable(w)
			return
		}
		if _, err = tx.Exec(r.Context(), `INSERT INTO knowledge_history(document_id,version,actor_id,operation) VALUES($1,$2,$3,'memory.source.withdrawn')`, id, version, user.ID); err != nil {
			unavailable(w)
			return
		}
		if err = audit(r.Context(), tx, user.ID, app, "memory.source.withdrawn", id, map[string]any{"environment_id": env, "version": version, "content_hash": hash, "expired_fact_ids": ids}); err != nil {
			unavailable(w)
			return
		}
	}
	if err = currentWriteSession(r.Context(), tx, r); err != nil {
		failure(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		unavailable(w)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	httpapi.WriteJSON(w, 200, map[string]any{"document_id": id, "lifecycle": "archived", "changed": changed, "expired_facts": len(ids)})
}
