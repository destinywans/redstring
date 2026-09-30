package v3knowledge

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

func (s *Service) draftHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.ready(w) {
			return
		}
		if len(scopeIDs(r)) != 1 {
			httpapi.Error(w, 409, "env_view_only", "Select one environment for a Runbook draft")
			return
		}
		source := strings.TrimSpace(r.URL.Query().Get("source"))
		from := strings.TrimSpace(r.URL.Query().Get("from"))
		in := createInput{Title: "Source outline", Source: source, From: from}
		if source == "blank" || source == "" || validateCreate(in) != nil {
			httpapi.Error(w, 422, "invalid_source", "Select a nonblank Runbook source and reference")
			return
		}
		tx, err := s.DB.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
		if err != nil {
			knowledgeError(w, err)
			return
		}
		defer tx.Rollback(r.Context())
		binding, err := s.loadDraftSource(r, tx, in)
		switch {
		case errors.Is(err, errSourceTooLarge):
			httpapi.Error(w, 413, "source_too_large", "Source exceeds the bounded Runbook draft limit")
			return
		case errors.Is(err, store.ErrForbidden), errors.Is(err, store.ErrNotFound), errors.Is(err, pgx.ErrNoRows), errors.Is(err, errSourceEmpty):
			httpapi.Error(w, 422, "invalid_source", "Source was not found or is not accessible in this environment")
			return
		case err != nil:
			httpapi.Error(w, 503, "source_unavailable", "Runbook source could not be read")
			return
		}
		cookie, cookieErr := r.Cookie("aiops_session")
		if cookieErr != nil {
			httpapi.Error(w, 401, "unauthorized", "Login required")
			return
		}
		if err = store.LockActiveSession(r.Context(), tx, cookie.Value, httpapi.CurrentUser(r).ID); err != nil {
			if errors.Is(err, store.ErrSessionRevoked) {
				httpapi.Error(w, 401, "unauthorized", "Session expired")
			} else {
				httpapi.Error(w, 503, "authorization_unavailable", "Session could not be checked")
			}
			return
		}
		if err = lockRunbookGrantSources(r.Context(), tx, httpapi.CurrentUser(r).ID); err != nil {
			httpapi.Error(w, 503, "authorization_unavailable", "Role grants could not be checked")
			return
		}
		binding, err = s.loadDraftSource(r, tx, in)
		switch {
		case errors.Is(err, errSourceTooLarge):
			httpapi.Error(w, 413, "source_too_large", "Source exceeds the bounded Runbook draft limit")
			return
		case errors.Is(err, store.ErrForbidden), errors.Is(err, store.ErrNotFound), errors.Is(err, pgx.ErrNoRows), errors.Is(err, errSourceEmpty):
			httpapi.Error(w, 422, "invalid_source", "Source was not found or is not accessible in this environment")
			return
		case err != nil:
			httpapi.Error(w, 503, "source_unavailable", "Runbook source could not be read")
			return
		}
		if err = store.LockActiveSession(r.Context(), tx, cookie.Value, httpapi.CurrentUser(r).ID); err != nil {
			if errors.Is(err, store.ErrSessionRevoked) {
				httpapi.Error(w, 401, "unauthorized", "Session expired")
			} else {
				httpapi.Error(w, 503, "authorization_unavailable", "Session could not be checked")
			}
			return
		}
		if err = tx.Commit(r.Context()); err != nil {
			knowledgeError(w, err)
			return
		}
		mode := "source_outline"
		if source == "template" {
			mode = "template_outline"
		}
		httpapi.WriteJSON(w, 200, map[string]any{
			"title": binding.Title, "applies_to": []string{}, "trigger": "",
			"steps": binding.Steps, "sources": []string{source + ":" + binding.SourceID},
			"source_sha256": binding.ContentHash,
			"outline":       binding.Outline, "draft_mode": mode,
			"missing": []string{"service_scope", "trigger", "validated_steps"},
		})
	}
}
