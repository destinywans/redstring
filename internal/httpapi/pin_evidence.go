package httpapi

import (
	"net/http"
	"strings"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/store"
)

func (s *Server) RegisterPinnedEvidence(db *store.Store) {
	s.Register("POST /api/v1/investigations/{id}/evidence", "operator", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			BranchID      string `json:"branch_id"`
			EnvironmentID string `json:"environment_id"`
			Kind          string `json:"kind"`
			Title         string `json:"title"`
			Source        string `json:"source"`
			Content       string `json:"content"`
		}
		if !Decode(w, r, &input) {
			return
		}
		if input.BranchID == "" || input.EnvironmentID == "" || (input.Kind != "metrics" && input.Kind != "logs") || strings.TrimSpace(input.Title) == "" || len(input.Title) > 300 || len(input.Source) > 2000 || len(input.Content) == 0 || len(input.Content) > 256<<10 {
			Error(w, 422, "invalid_evidence", "Select an investigation branch in the same environment and a bounded query snapshot")
			return
		}
		v, e := db.PinEvidence(r.Context(), r.PathValue("id"), input.EnvironmentID, input.BranchID, core.Evidence{Kind: input.Kind, Title: input.Title, Content: input.Content, Source: input.Source})
		if e != nil {
			fail(w, e)
			return
		}
		WriteJSON(w, 201, v)
	})
}
