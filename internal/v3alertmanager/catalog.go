package v3alertmanager

import (
	"context"
	"errors"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Access interface {
	Authorize(context.Context, core.User, rbac.V3Decision) error
}

// Catalog establishes current ownership from persisted, enabled applications.
// Provider labels alone cannot prove that an application is active or who owns
// it. A mutable/reused key cannot prove historical ownership of an old silence.
type Catalog interface {
	Applications(context.Context, []string) ([]Application, error)
	EnvironmentAdmin(context.Context, core.User, string) (bool, error)
}

type Application struct {
	ID, EnvironmentID, OwnerTeamID string
}

type DatabaseCatalog struct{ DB *pgxpool.Pool }

func (c DatabaseCatalog) Applications(ctx context.Context, environments []string) ([]Application, error) {
	if c.DB == nil {
		return nil, errors.New("application catalog unavailable")
	}
	rows, err := c.DB.Query(ctx, `SELECT a.id,ve.environment_id,COALESCE(a.owner_team_id,'')
 FROM applications a JOIN v3_application_environments ve ON ve.application_id=a.id
 JOIN application_environments e ON e.application_id=a.id AND e.key=ve.environment_id AND NOT e.archived
 WHERE NOT a.archived AND ve.environment_id=ANY($1::text[])
 ORDER BY ve.environment_id,a.id LIMIT 501`, environments)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	apps := []Application{}
	for rows.Next() {
		if len(apps) == 500 {
			return nil, errors.New("application catalog exceeds 500-row bound")
		}
		var app Application
		if err := rows.Scan(&app.ID, &app.EnvironmentID, &app.OwnerTeamID); err != nil {
			return nil, err
		}
		apps = append(apps, app)
	}
	return apps, rows.Err()
}

// UserNames maps platform user IDs to display names. Only IDs of existing
// users are returned; anything else stays the caller's raw value.
func (c DatabaseCatalog) UserNames(ctx context.Context, ids []string) (map[string]string, error) {
	names := map[string]string{}
	if c.DB == nil || len(ids) == 0 {
		return names, nil
	}
	rows, err := c.DB.Query(ctx, `SELECT id,COALESCE(NULLIF(v3_display_name,''),name) FROM users WHERE id=ANY($1::text[])`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		names[id] = name
	}
	return names, rows.Err()
}

// A broad silence needs an environment-wide administrative grant. An app or
// team scoped manage grant never promotes its holder to environment admin.
func (c DatabaseCatalog) EnvironmentAdmin(ctx context.Context, user core.User, env string) (bool, error) {
	if c.DB == nil || user.ID == "" || user.Disabled || env == "" || env == "all" {
		return false, errors.New("silence administrator catalog unavailable")
	}
	var allowed bool
	err := c.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM v3_effective_role_grants g
 WHERE g.user_id=$1 AND g.module='silences' AND g.action='manage'
 AND ((g.scope_kind='global' AND g.scope_id='*') OR
      (g.scope_kind='environment' AND g.scope_id=$2)))`, user.ID, env).Scan(&allowed)
	return allowed, err
}
