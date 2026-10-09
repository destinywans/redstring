package v3catalog

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

// ServiceEvidence is a deliberately small, read-only profile for an Agent.
// Configured ownership is not evidence of live service health. No connector
// binding, secret, SLO measurement, or unverified runtime status is exposed.
type ServiceEvidence struct {
	ID          string `json:"id"`
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Environment string `json:"environment"`
	Team        string `json:"team,omitempty"`
	Tier        string `json:"tier,omitempty"`
	Owner       string `json:"owner,omitempty"`
	Health      string `json:"health"`
}

type serviceEvidenceRow struct {
	appID, key, name, description, environment, teamID, team, tier, owner string
}

func (r serviceEvidenceRow) evidence() ServiceEvidence {
	return ServiceEvidence{
		ID: recordID(r.appID, r.environment), Key: r.key, Name: r.name,
		Description: r.description, Environment: r.environment, Team: r.team,
		Tier: r.tier, Owner: r.owner, Health: "unavailable",
	}
}

// LookupServiceEvidence resolves exactly one persisted service key in exactly
// one environment. The caller supplies the current user, never an old request
// snapshot or a client-provided context reference. Hidden and absent services
// have the same ErrNotFound result. Operational policy/DB failures propagate.
func (s *Service) LookupServiceEvidence(ctx context.Context, user core.User, environmentID, key string) (ServiceEvidence, error) {
	if s == nil || s.Store == nil || s.Store.DB == nil || s.Access == nil {
		return ServiceEvidence{}, errors.New("service catalog unavailable")
	}
	if user.ID == "" || user.Disabled || environmentID == "" || environmentID == "all" || key == "" || len(key) > 256 || strings.TrimSpace(key) != key || !s.instanceEnvironment(environmentID) {
		return ServiceEvidence{}, store.ErrNotFound
	}
	const query = `SELECT a.id,a.key,a.name,a.description,ve.environment_id,
 COALESCE(a.owner_team_id,''),COALESCE(t.name,''),COALESCE(ep.tier,''),COALESCE(ep.owner_label,'')
 FROM applications a
 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id=$1
 JOIN application_environments e ON e.application_id=a.id AND e.key=ve.environment_id AND NOT e.archived
 LEFT JOIN teams t ON t.id=a.owner_team_id
 LEFT JOIN v3_service_environment_profiles ep ON ep.application_id=a.id AND ep.environment_id=ve.environment_id
 WHERE a.key=$2 AND NOT a.archived`
	var row serviceEvidenceRow
	err := s.Store.DB.QueryRow(ctx, query, environmentID, key).Scan(
		&row.appID, &row.key, &row.name, &row.description, &row.environment,
		&row.teamID, &row.team, &row.tier, &row.owner,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ServiceEvidence{}, store.ErrNotFound
	}
	if err != nil {
		return ServiceEvidence{}, err
	}
	if err := s.authorizeServiceEvidence(ctx, user, row); err != nil {
		return ServiceEvidence{}, err
	}
	return row.evidence(), nil
}

func (s *Service) authorizeServiceEvidence(ctx context.Context, user core.User, row serviceEvidenceRow) error {
	if s == nil || s.Access == nil || row.appID == "" || row.environment == "" || user.ID == "" || user.Disabled {
		return store.ErrNotFound
	}
	err := s.Access.Authorize(ctx, user, rbac.V3Decision{Module: "services", Action: "view", EnvironmentID: row.environment, ApplicationID: row.appID})
	if err == nil {
		return nil
	}
	if !errors.Is(err, store.ErrForbidden) {
		return err
	}
	if row.teamID == "" {
		return store.ErrNotFound
	}
	err = s.Access.Authorize(ctx, user, rbac.V3Decision{Module: "services", Action: "view", EnvironmentID: row.environment, TeamID: row.teamID})
	if errors.Is(err, store.ErrForbidden) {
		return store.ErrNotFound
	}
	return err
}

// ListServiceEvidence lists the services the caller may view in exactly one
// environment, by key, with the same authorization as LookupServiceEvidence
// (PA-50: the model needs the exact keys it may read). At most limit services
// are returned; truncated reports that more are visible. Hidden services are
// skipped without a trace.
func (s *Service) ListServiceEvidence(ctx context.Context, user core.User, environmentID string, limit int) ([]ServiceEvidence, bool, error) {
	if s == nil || s.Store == nil || s.Store.DB == nil || s.Access == nil {
		return nil, false, errors.New("service catalog unavailable")
	}
	if user.ID == "" || user.Disabled || environmentID == "" || environmentID == "all" || !s.instanceEnvironment(environmentID) || limit < 1 {
		return []ServiceEvidence{}, false, nil
	}
	const scanLimit = 2000
	rows, err := s.Store.DB.Query(ctx, `SELECT a.id,a.key,a.name,a.description,ve.environment_id,
 COALESCE(a.owner_team_id,''),COALESCE(t.name,''),COALESCE(ep.tier,''),COALESCE(ep.owner_label,'')
 FROM applications a
 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id=$1
 JOIN application_environments e ON e.application_id=a.id AND e.key=ve.environment_id AND NOT e.archived
 LEFT JOIN teams t ON t.id=a.owner_team_id
 LEFT JOIN v3_service_environment_profiles ep ON ep.application_id=a.id AND ep.environment_id=ve.environment_id
 WHERE NOT a.archived AND a.key<>'' ORDER BY a.key LIMIT $2`, environmentID, scanLimit+1)
	if err != nil {
		return nil, false, err
	}
	var candidates []serviceEvidenceRow
	for rows.Next() {
		var row serviceEvidenceRow
		if err = rows.Scan(&row.appID, &row.key, &row.name, &row.description, &row.environment,
			&row.teamID, &row.team, &row.tier, &row.owner); err != nil {
			break
		}
		candidates = append(candidates, row)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, false, err
	}
	if len(candidates) > scanLimit {
		return nil, false, errors.New("too many services to list")
	}
	out := make([]ServiceEvidence, 0, min(limit, len(candidates)))
	for _, row := range candidates {
		err := s.authorizeServiceEvidence(ctx, user, row)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, false, err
		}
		if len(out) == limit {
			return out, true, nil
		}
		out = append(out, row.evidence())
	}
	return out, false, nil
}
