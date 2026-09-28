package v3knowledge

import (
	"errors"
	"net/http"
	"strings"

	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/knowledge"
	"github.com/wans/aiops-canvas/internal/store"
)

// A template is a real, versioned-in-code outline. Other sources can be
// validated, but transforming private evidence into operational steps needs a
// separately wired, bounded draft generator and human review.
func (s *Service) draftHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.ready(w) {
			return
		}
		source := strings.TrimSpace(r.URL.Query().Get("source"))
		from := strings.TrimSpace(r.URL.Query().Get("from"))
		if source == "" || source == "blank" || len(from) > 200 || from == "" {
			httpapi.Error(w, 422, "invalid_source", "Select a nonblank Runbook source and reference")
			return
		}
		in := createInput{Title: "Source outline", Source: source, From: from}
		if err := validateCreate(in); err != nil {
			httpapi.Error(w, 422, "invalid_source", err.Error())
			return
		}
		if source == "template" {
			for _, template := range knowledge.Templates() {
				if template.ID == from && template.ID != "blank" {
					httpapi.WriteJSON(w, 200, map[string]any{
						"title": template.Title, "applies_to": []string{}, "trigger": "",
						"steps": []Step{}, "sources": []string{"template:" + template.ID},
						"outline": template.Body, "draft_mode": "template_outline",
						"missing": []string{"service_scope", "trigger", "validated_steps"},
					})
					return
				}
			}
			httpapi.Error(w, 422, "invalid_source", "Runbook template does not exist")
			return
		}
		if len(scopeIDs(r)) != 1 {
			httpapi.Error(w, 409, "env_view_only", "Select one environment for a source draft")
			return
		}
		tx, err := s.DB.Begin(r.Context())
		if err != nil {
			knowledgeError(w, err)
			return
		}
		defer tx.Rollback(r.Context())
		_, err = s.validateSource(r, tx, in)
		if errors.Is(err, store.ErrForbidden) || errors.Is(err, store.ErrNotFound) || errors.Is(err, errSourceEmpty) {
			httpapi.Error(w, 422, "invalid_source", "Source was not found or is not accessible in this environment")
			return
		}
		if err != nil {
			httpapi.Error(w, 503, "source_unavailable", "Source validation failed")
			return
		}
		httpapi.Error(w, 501, "draft_generator_unavailable", "A source-backed Runbook draft generator is not configured")
	}
}
