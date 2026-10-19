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

// A profile title is user-maintained directory data, not an RBAC role.
// Current duty and total workload have no authorized, complete peek source.
type personPeekResponse struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Role   *string `json:"role"`
	Oncall *string `json:"oncall"`
	Load   *string `json:"load"`
}

func (s *Service) personPeek(w http.ResponseWriter, r *http.Request) {
	envs, ok := s.ready(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if strings.TrimSpace(name) == "" || len(name) > 800 || utf8.RuneCountInString(name) > 200 {
		httpapi.Error(w, 422, "invalid_person_name", "Invalid person name")
		return
	}

	// The same directory predicate as mentions limits both target visibility
	// and the caller's selected-environment members.view scope. Exact names
	// may belong to different accounts; never choose one by row order.
	rows, err := s.DB.Query(r.Context(), personSource, envs, httpapi.CurrentUser(r).ID, "", "", 101, name)
	if err != nil {
		httpapi.Error(w, 503, "person_peek_unavailable", "Person preview unavailable")
		return
	}
	// Authorize after closing rows: policy evaluation can query the same pool.
	candidates := make([]record, 0, 2)
	for rows.Next() {
		var item record
		item.Kind = "person"
		if err = rows.Scan(&item.ID, &item.Title, &item.EnvironmentID); err != nil {
			break
		}
		candidates = append(candidates, item)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		httpapi.Error(w, 503, "person_peek_unavailable", "Person preview unavailable")
		return
	}
	var found record
	visible := 0
	for _, item := range candidates {
		err = s.authorize(r, item)
		if err == nil {
			found = item
			visible++
			if visible > 1 {
				break
			}
		} else if !errors.Is(err, store.ErrForbidden) {
			break
		}
		err = nil
	}
	if err != nil {
		httpapi.Error(w, 503, "person_peek_unavailable", "Person preview unavailable")
		return
	}
	if visible == 0 {
		httpapi.Error(w, 404, "not_found", "Person not found")
		return
	}
	if visible > 1 || len(candidates) == 101 {
		httpapi.Error(w, 409, "ambiguous_person_name", "Person name is not unique in this scope")
		return
	}

	var jobTitle string
	err = s.DB.QueryRow(r.Context(), `SELECT job_title FROM users
WHERE id=$1 AND name=$2 AND deleted_at IS NULL AND NOT disabled
 AND source<>'settings_demo' AND v3_identity_kind='human'`, found.ID, name).Scan(&jobTitle)
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, 404, "not_found", "Person not found")
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "person_peek_unavailable", "Person preview unavailable")
		return
	}
	// Reuse the object read before replying, including current directory scope
	// and policy. A changed name no longer satisfies this exact-name URL.
	current, err := s.recordByID(r, envs, "person", found.ID)
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, store.ErrForbidden) || (err == nil && current.Title != name) {
		httpapi.Error(w, 404, "not_found", "Person not found")
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "person_peek_unavailable", "Person preview unavailable")
		return
	}
	var role *string
	if title := strings.TrimSpace(jobTitle); title != "" {
		role = &title
	}
	httpapi.WriteJSON(w, 200, personPeekResponse{ID: current.ID, Name: current.Title, Role: role})
}
