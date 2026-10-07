package v3agents

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

func (s *Service) EnvironmentExecutionLimit(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.DB == nil || s.Access == nil {
		unavailable(w)
		return
	}
	envs, ok := requestScope(w, r, true)
	if !ok {
		return
	}
	var body struct {
		MaxLevel         string `json:"max_level"`
		ExpectedMaxLevel string `json:"expected_max_level"`
	}
	if r.Method == http.MethodPut {
		if !httpapi.Decode(w, r, &body) {
			return
		}
		if levelNumber(body.MaxLevel) < 0 || levelNumber(body.ExpectedMaxLevel) < 0 {
			invalid(w, "Known max_level and expected_max_level required")
			return
		}
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		unavailable(w)
		return
	}
	defer tx.Rollback(r.Context())
	if err = authorizeEnvironmentGovernance(r.Context(), tx, r, envs[0]); err != nil {
		lifecycleAuthorityError(w, err)
		return
	}
	// Lock also represents the implicit L0 row and serializes two first writes.
	_, err = tx.Exec(r.Context(), `LOCK TABLE v3_agent_environment_limits IN EXCLUSIVE MODE`)
	if err != nil {
		unavailable(w)
		return
	}
	var current string
	err = tx.QueryRow(r.Context(), `SELECT COALESCE((SELECT max_level FROM v3_agent_environment_limits WHERE environment_id=$1),'L0')`, envs[0]).Scan(&current)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		unavailable(w)
		return
	}
	if r.Method == http.MethodPut {
		if body.ExpectedMaxLevel != current {
			httpapi.Error(w, 409, "version_conflict", "Environment execution limit changed")
			return
		}
		if body.MaxLevel != current {
			_, err = tx.Exec(r.Context(), `INSERT INTO v3_agent_environment_limits(environment_id,max_level,updated_by) VALUES($1,$2,$3) ON CONFLICT(environment_id) DO UPDATE SET max_level=excluded.max_level,updated_by=excluded.updated_by,updated_at=clock_timestamp()`, envs[0], body.MaxLevel, httpapi.CurrentUser(r).ID)
			if err == nil {
				_, err = tx.Exec(r.Context(), `INSERT INTO management_audit(actor_id,actor_kind,application_id,action,target,outcome,changes) VALUES($1,'user','','v3.agent.environment_limit.changed',$2,'recorded',jsonb_build_object('environment_id',$2::text,'from',$3::text,'to',$4::text))`, httpapi.CurrentUser(r).ID, envs[0], current, body.MaxLevel)
			}
			if err != nil {
				unavailable(w)
				return
			}
		}
		current = body.MaxLevel
	}
	if !s.finishLifecycleWrite(w, r, tx) {
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"environment_id": envs[0], "max_level": current})
}

func authorizeEnvironmentGovernance(ctx context.Context, tx pgx.Tx, r *http.Request, env string) error {
	if err := lifecycleSession(ctx, tx, r); err != nil {
		return err
	}
	if err := lockEvalGrantSources(ctx, tx, httpapi.CurrentUser(r).ID); err != nil {
		return err
	}
	// The platform-administrator view is stable under the user row and
	// physical assignment/grant locks already held above: disabling/deleting
	// the actor or changing its department waits on the user row, and removing
	// direct/inherited admin membership waits on rbac_user_roles or
	// rbac_department_roles. Their role FKs prevent removing the referenced
	// admin role while those memberships remain. Do not LOCK the view itself:
	// PostgreSQL recursively locks rbac_roles in SHARE mode, reversing
	// CreateRole's rbac_roles INSERT -> v3_role_grants INSERT lock order.
	// The SELECT below needs only ACCESS SHARE, compatible with role inserts.
	var admin bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users u WHERE u.id=$1 AND u.v3_identity_kind='human'
 AND NOT u.disabled AND u.deleted_at IS NULL AND u.source<>'settings_demo'
 AND EXISTS(SELECT 1 FROM rbac_platform_administrators pa WHERE pa.user_id=u.id)
 AND EXISTS(SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=u.id AND g.module='agents' AND g.action='manage' AND g.scope_kind='global' AND g.scope_id='*')
 AND EXISTS(SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=u.id AND g.module='environment' AND g.action='view' AND ((g.scope_kind='global' AND g.scope_id='*') OR(g.scope_kind='environment' AND g.scope_id=$2))))`, httpapi.CurrentUser(r).ID, env).Scan(&admin); err != nil {
		return err
	}
	if !admin {
		return store.ErrForbidden
	}
	return nil
}
