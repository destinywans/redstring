package v3discovery

import (
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
)

type servicePeekResult struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Tier   *string `json:"tier"`
	Team   *string `json:"team"`
	Env    string  `json:"env"`
	Status *string `json:"status"`
	KPIs   []any   `json:"kpis"`
	Owner  *string `json:"owner"`
	Oncall *string `json:"oncall"`
	Change *string `json:"change"`
}

// servicePeek accepts the stable environment-qualified service ID, the
// application ID emitted by real alert groups, and the key used by mock-era
// links. Multiple visible matches are ambiguous across apps or environments.
func (s *Service) servicePeek(w http.ResponseWriter, r *http.Request) {
	envs, ok := s.ready(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if id == "" || len(id) > 1024 {
		httpapi.Error(w, 422, "invalid_reference", "Invalid service ID")
		return
	}

	item, err := s.serviceByApplicationReference(r, envs, id)
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, store.ErrForbidden) {
		httpapi.Error(w, 404, "not_found", "Service not found")
		return
	}
	if errors.Is(err, errAmbiguousServiceReference) {
		httpapi.Error(w, 409, "ambiguous_service", "Service reference is not unique in this scope")
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "service_peek_unavailable", "Service preview unavailable")
		return
	}

	var teamID, teamName, ownerLabel string
	err = s.DB.QueryRow(r.Context(), `SELECT COALESCE(a.owner_team_id,''),COALESCE(t.name,''),COALESCE(p.owner_label,'')
FROM applications a
JOIN v3_application_environments ve ON ve.application_id=a.id
JOIN application_environments e ON e.application_id=a.id AND e.key=ve.environment_id AND NOT e.archived
LEFT JOIN teams t ON t.id=a.owner_team_id
LEFT JOIN v3_service_environment_profiles p ON p.application_id=a.id AND p.environment_id=ve.environment_id
WHERE a.id=$1 AND NOT a.archived AND ve.environment_id=$2 AND ve.environment_id=ANY($3::text[])`,
		item.ApplicationID, item.EnvironmentID, envs).Scan(&teamID, &teamName, &ownerLabel)
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, 404, "not_found", "Service not found")
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "service_peek_unavailable", "Service preview unavailable")
		return
	}
	// The profile's oncall_user_id is a configured assignment, not evidence of
	// who is on call now. No scoped live health, KPI, or change source is wired.
	// Recheck the object policy after loading its metadata before serialization.
	item, err = s.recordByID(r, envs, "service", item.ID)
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, store.ErrForbidden) {
		httpapi.Error(w, 404, "not_found", "Service not found")
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "service_peek_unavailable", "Service preview unavailable")
		return
	}
	if item.TeamID != teamID {
		// Ownership changed during the read; do not emit metadata loaded under
		// a different team grant.
		httpapi.Error(w, 503, "service_peek_unavailable", "Service preview unavailable")
		return
	}
	httpapi.WriteJSON(w, 200, servicePeekResult{
		ID: item.ID, Name: item.Title, Tier: servicePeekText(item.Tier),
		Team: servicePeekText(teamName), Env: item.EnvironmentID,
		KPIs: []any{}, Owner: servicePeekText(ownerLabel),
	})
}

var errAmbiguousServiceReference = errors.New("service reference resolves to multiple visible services")

func (s *Service) serviceByApplicationReference(r *http.Request, envs []string, reference string) (record, error) {
	decodedAppID, decodedEnv, _ := parseServiceRecordID(reference)
	if !utf8.ValidString(decodedAppID) || !utf8.ValidString(decodedEnv) {
		decodedAppID, decodedEnv = "", ""
	}
	rows, err := s.DB.Query(r.Context(), `SELECT a.id,ve.environment_id,COALESCE(a.owner_team_id,'')
FROM applications a
JOIN v3_application_environments ve ON ve.application_id=a.id
JOIN application_environments e ON e.application_id=a.id AND e.key=ve.environment_id AND NOT e.archived
WHERE (a.key=$1 OR a.id=$1 OR (a.id=$4 AND ve.environment_id=$5))
 AND NOT a.archived AND ve.environment_id=ANY($2::text[])
AND EXISTS (SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=$3
 AND g.module='services' AND g.action='view' AND (
  (g.scope_kind='global' AND g.scope_id='*') OR
  (g.scope_kind='environment' AND g.scope_id=ve.environment_id) OR
  (g.scope_kind='application' AND g.scope_id=a.id) OR
  (g.scope_kind='team' AND g.scope_id=a.owner_team_id)))
ORDER BY ve.environment_id,a.id LIMIT 101`, reference, envs, httpapi.CurrentUser(r).ID, decodedAppID, decodedEnv)
	if err != nil {
		return record{}, err
	}
	// Drain the result before policy calls, which may themselves need a pool
	// connection. This also works with a single-connection pool.
	candidates := make([]record, 0, 2)
	for rows.Next() {
		var appID, env, teamID string
		if err = rows.Scan(&appID, &env, &teamID); err != nil {
			rows.Close()
			return record{}, err
		}
		candidates = append(candidates, record{Kind: "service", ID: serviceRecordID(appID, env), ApplicationID: appID, EnvironmentID: env, TeamID: teamID})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return record{}, err
	}
	var chosen record
	for _, candidate := range candidates {
		if err = s.authorize(r, candidate); errors.Is(err, store.ErrForbidden) {
			continue
		} else if err != nil {
			return record{}, err
		}
		if chosen.ID != "" {
			return record{}, errAmbiguousServiceReference
		}
		chosen = candidate
	}
	if chosen.ID == "" {
		return record{}, pgx.ErrNoRows
	}
	if len(candidates) == 101 {
		return record{}, errAmbiguousServiceReference
	}
	return s.recordByID(r, envs, "service", chosen.ID)
}

func servicePeekText(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}
