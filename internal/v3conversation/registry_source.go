package v3conversation

import (
	"context"
	"errors"

	"github.com/destinywans/redstring/internal/v3telemetry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// registeredSource resolves the one registry source serving surface for a
// service key in env (PA-19 P3 stage 2): the service's application, its
// owner team and the source. A key with no application in the environment
// is v3telemetry.ErrNotConfigured; ErrAmbiguous passes through.
func registeredSource(ctx context.Context, db *pgxpool.Pool, registry *v3telemetry.Resolver, service, env string,
	surface v3telemetry.Surface) (appID, teamID string, source v3telemetry.Source, err error) {
	err = db.QueryRow(ctx, `SELECT a.id,COALESCE(a.owner_team_id,'')
 FROM applications a
 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id=$2
 JOIN application_environments e ON e.application_id=a.id AND e.key=ve.environment_id AND NOT e.archived
 WHERE a.key=$1 AND NOT a.archived`, service, env).Scan(&appID, &teamID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", source, v3telemetry.ErrNotConfigured
	}
	if err != nil {
		return "", "", source, err
	}
	source, err = registry.Resolve(ctx, db, appID, env, surface)
	return appID, teamID, source, err
}
