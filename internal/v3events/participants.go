package v3events

import (
	"errors"
	"net/http"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
)

func incidentParticipantNames(names []string) ([]string, bool) {
	if len(names) > 50 {
		return nil, false
	}
	return uniqueIncidentParticipantNames(names)
}

// The merged list may contain more than 50 inputs, while its distinct final
// participants remain within the limit. Request size is checked separately.
func uniqueIncidentParticipantNames(names []string) ([]string, bool) {
	out := make([]string, 0, len(names))
	seen := map[string]bool{}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 100 || len(name) > 400 {
			return nil, false
		}
		for _, ch := range name {
			if unicode.IsControl(ch) || unicode.Is(unicode.Cf, ch) {
				return nil, false
			}
		}
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	if len(out) > 50 {
		return nil, false
	}
	sort.Strings(out)
	return out, true
}

func readIncidentParticipants(r *http.Request, tx pgx.Tx, id string) ([]string, error) {
	rows, err := tx.Query(r.Context(), `SELECT name FROM v3_incident_participants WHERE incident_id=$1 ORDER BY name LIMIT 51`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := []string{}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	if len(names) > 50 {
		return nil, store.ErrInvalidReference
	}
	return names, rows.Err()
}

func saveIncidentParticipants(r *http.Request, tx pgx.Tx, id string, names []string) error {
	for _, name := range names {
		if _, err := tx.Exec(r.Context(), `INSERT INTO v3_incident_participants(incident_id,name,actor_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, id, name, httpapi.CurrentUser(r).ID); err != nil {
			return err
		}
	}
	return nil
}

// AddParticipants appends names under the incident lock. It never changes ACLs.
func (s *Service) AddParticipants(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Names []string `json:"names"`
	}
	if !httpapi.Decode(w, r, &in) {
		return
	}
	names, ok := incidentParticipantNames(in.Names)
	if !ok || len(names) == 0 {
		httpapi.Error(w, 422, "invalid_participants", "Supply 1–50 participant names")
		return
	}
	if s.DB == nil {
		respondErr(w, errors.New("storage unavailable"))
		return
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		respondErr(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	id := r.PathValue("id")
	c, err := scanCase(tx.QueryRow(r.Context(), caseSelect+` WHERE c.id=$1 FOR UPDATE OF c`, id))
	v := incidentRecord{ApplicationID: c.ApplicationID, EnvironmentID: c.EnvironmentID, EnvironmentKey: c.EnvironmentKey}
	if errors.Is(err, pgx.ErrNoRows) {
		v, err = scanIncident(tx.QueryRow(r.Context(), incidentSelect+` WHERE i.id=$1 FOR UPDATE OF i`, id))
	}
	if err != nil {
		respondIncidentCreateError(w, err)
		return
	}
	for _, action := range []string{"change", "view"} {
		if err = s.authorizeAlertTx(r, tx, v, action); err != nil {
			respondAlertAuthorityError(w, err)
			return
		}
	}
	previous, err := readIncidentParticipants(r, tx, id)
	if err != nil {
		respondErr(w, err)
		return
	}
	merged, valid := uniqueIncidentParticipantNames(append(previous, names...))
	if !valid {
		httpapi.Error(w, 409, "participants_limit", "At most 50 participants per incident")
		return
	}
	if err = saveIncidentParticipants(r, tx, id, names); err != nil {
		respondErr(w, err)
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO management_audit(actor_id,actor_kind,application_id,action,target,outcome,changes) VALUES($1,'user',$2,'incident.participants.add',$3,'succeeded',jsonb_build_object('environment_id',$4::text,'participant_count',$5::int))`, httpapi.CurrentUser(r).ID, v.ApplicationID, id, v.EnvironmentKey, len(merged)); err != nil {
		respondErr(w, err)
		return
	}
	if err = activeAlertReadSession(r.Context(), tx, r); err != nil {
		respondAlertAuthorityError(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		respondErr(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, 200, map[string]any{"participants": merged})
}
