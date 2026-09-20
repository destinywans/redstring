package httpapi

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/store"
)

// ConfigureContextBudget freezes the actually assembled model input per Attempt.
// It does not mutate branch messages or replace canonical product history.
func (s *Server) ConfigureContextBudget(w *Worker, db *store.Store) {
	prior := w.Prepare
	w.Prepare = func(ctx context.Context, c store.Claim, req *inference.Request) error {
		if prior != nil {
			if e := prior(ctx, c, req); e != nil {
				return e
			}
		}
		history := make([]inference.ContextMessage, 0, len(c.Messages))
		for _, m := range c.Messages {
			history = append(history, inference.ContextMessage{ID: m.ID, Role: m.Role, Content: m.Content})
		}
		ids := []string{}
		for _, e := range c.FrozenEvidence {
			ids = append(ids, e.ID)
		}
		retry, e := db.RetryEvidence(ctx, c)
		if e != nil {
			return e
		}
		for _, v := range retry {
			ids = append(ids, v.ID)
		}
		manifest, e := inference.PrepareContext(ctx, w.Runtime, *req, history, c.ContextRevision, ids)
		if e != nil {
			return e
		}
		raw, e := json.Marshal(manifest)
		if e != nil {
			return e
		}
		if e = db.SaveRunContext(ctx, c, raw); e != nil {
			return e
		}
		req.Messages = manifest.Messages
		if req.MaxCalls > 0 {
			req.MaxCalls -= manifest.SummaryCalls
		}
		return nil
	}
	s.Register("GET /api/v1/runs/{id}/context", "viewer", func(w http.ResponseWriter, r *http.Request) {
		// Register's resource guard applies the run's investigation application ACL.
		raw, e := db.RunContext(r.Context(), r.PathValue("id"))
		if e != nil {
			fail(w, e)
			return
		}
		WriteJSON(w, 200, raw)
	})
}
