package v3agents

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

type createAgentInput struct {
	ID            string `json:"id"`
	ApplicationID string `json:"application_id"`
	Name          string `json:"name"`
	Summary       string `json:"summary"`
}

// CreateAgent creates a paused L0 catalog identity in the selected environment.
// A version, immutable execution config and explicit release remain separate.
func (s *Service) CreateAgent(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.DB == nil || s.Access == nil {
		unavailable(w)
		return
	}
	envs, ok := requestScope(w, r, true)
	if !ok {
		return
	}
	var in createAgentInput
	if !httpapi.Decode(w, r, &in) {
		return
	}
	if !safeAuditCode(in.ID, 128) || !bounded(in.ApplicationID, 128) || !bounded(in.Name, 160) || len([]rune(in.Summary)) > 1000 || strings.TrimSpace(in.Summary) != in.Summary {
		invalid(w, "Stable Agent ID, application, name and bounded summary required")
		return
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		unavailable(w)
		return
	}
	defer tx.Rollback(r.Context())
	a := agent{ID: in.ID, App: in.ApplicationID, Env: envs[0], Name: in.Name, Summary: in.Summary}
	if err = lockActiveEvalScope(r.Context(), tx, a); errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	} else if err != nil {
		unavailable(w)
		return
	}
	team, err := evalOwnerTeam(r.Context(), tx, a)
	if err != nil {
		unavailable(w)
		return
	}
	if err = lockEvalHumanSession(r.Context(), tx, r); err == nil {
		err = lockEvalGrantSources(r.Context(), tx, httpapi.CurrentUser(r).ID)
	}
	if err != nil {
		lifecycleAuthorityError(w, err)
		return
	}
	if err = s.authorizeEvalTx(r.Context(), tx, r, a, team, "view"); errors.Is(err, store.ErrForbidden) {
		notFound(w)
		return
	} else if err != nil {
		unavailable(w)
		return
	}
	if err = s.authorizeEvalTx(r.Context(), tx, r, a, team, "manage"); err != nil {
		lifecycleAuthorityError(w, err)
		return
	}
	tag, err := tx.Exec(r.Context(), `INSERT INTO v3_agents(id,application_id,environment_id,name,summary,owner_id)
 VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(id) DO NOTHING`, a.ID, a.App, a.Env, a.Name, a.Summary, httpapi.CurrentUser(r).ID)
	if err != nil {
		unavailable(w)
		return
	}
	created := tag.RowsAffected() == 1
	if !created {
		existing, e := scanAgent(tx.QueryRow(r.Context(), `SELECT `+agentColumns+` FROM v3_agents WHERE id=$1 FOR SHARE`, a.ID))
		if e != nil {
			unavailable(w)
			return
		}
		if existing.App != a.App || existing.Env != a.Env {
			notFound(w)
			return
		}
		if existing.Name != a.Name || existing.Summary != a.Summary {
			httpapi.Error(w, 409, "agent_registration_conflict", "Agent ID already has different catalog content")
			return
		}
	} else if err = audit(r.Context(), tx, a, httpapi.CurrentUser(r).ID, "v3.agent.created", a.ID, map[string]any{"environment_id": a.Env, "level": "L0", "paused": true}); err != nil {
		unavailable(w)
		return
	}
	if !s.finishLifecycleWrite(w, r, tx) {
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpapi.WriteJSON(w, status, map[string]any{"id": a.ID, "created": created, "application_id": a.App, "environment_id": a.Env})
}

func (s *Service) CreateAgentVersion(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Version     string `json:"version"`
		ArtifactRef string `json:"artifact_ref"`
	}
	if !httpapi.Decode(w, r, &in) {
		return
	}
	if !safeCode(in.Version, 80) || !safeCode(in.ArtifactRef, 500) {
		invalid(w, "Version and non-secret immutable artifact reference required")
		return
	}
	tx, a, ok := s.beginLifecycleWrite(w, r)
	if !ok {
		return
	}
	defer tx.Rollback(r.Context())
	tag, err := tx.Exec(r.Context(), `INSERT INTO v3_agent_versions(agent_id,version,artifact_ref,created_by)
 VALUES($1,$2,$3,$4) ON CONFLICT(agent_id,version) DO NOTHING`, a.ID, in.Version, in.ArtifactRef, httpapi.CurrentUser(r).ID)
	if err != nil {
		unavailable(w)
		return
	}
	created := tag.RowsAffected() == 1
	if !created {
		var ref string
		if err = tx.QueryRow(r.Context(), `SELECT artifact_ref FROM v3_agent_versions WHERE agent_id=$1 AND version=$2 FOR SHARE`, a.ID, in.Version).Scan(&ref); err != nil {
			unavailable(w)
			return
		}
		if ref != in.ArtifactRef {
			httpapi.Error(w, 409, "agent_version_immutable", "Register a new version for a different artifact")
			return
		}
	} else if err = audit(r.Context(), tx, a.agent, httpapi.CurrentUser(r).ID, "v3.agent.version.registered", a.ID, map[string]any{"environment_id": a.Env, "version": in.Version}); err != nil {
		unavailable(w)
		return
	}
	if !s.finishLifecycleWrite(w, r, tx) {
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpapi.WriteJSON(w, status, map[string]any{"agent_id": a.ID, "version": in.Version, "created": created})
}
