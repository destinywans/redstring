package actions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
)

var ErrInvalid = errors.New("invalid action input")

type Report struct {
	InvestigationID string                  `json:"investigation_id"`
	ApplicationID   string                  `json:"application_id"`
	ExecutionID     string                  `json:"execution_id"`
	PlanID          string                  `json:"plan_id"`
	Summary         string                  `json:"summary"`
	ClosedBy        string                  `json:"closed_by"`
	ClosedAt        time.Time               `json:"closed_at"`
	ApprovedBy      string                  `json:"approved_by"`
	Proposal        connectors.RollbackPlan `json:"proposal"`
	Recovery        json.RawMessage         `json:"recovery"`
	Evidence        []core.Evidence         `json:"evidence"`
}

func (s *Service) Decide(ctx context.Context, id, digest, decision, reason, user string) (Plan, error) {
	if (decision != "rejected" && decision != "revoked") || strings.TrimSpace(reason) == "" || len(reason) > 2000 {
		return Plan{}, ErrInvalid
	}
	if s.Authorize != nil || s.AuthorizePlan != nil {
		p, e := s.Plan(ctx, id)
		if e != nil {
			return Plan{}, e
		}
		if e = s.authorizePlan(ctx, user, p, "approve"); e != nil {
			return Plan{}, e
		}
	}
	return s.Repository.Decide(ctx, id, digest, decision, reason, user)
}
func (s *Service) CloseInvestigation(ctx context.Context, inv, execution, summary, user string) (Report, error) {
	if execution == "" || len(summary) > 8000 {
		return Report{}, ErrInvalid
	}
	return s.Repository.CloseInvestigation(ctx, inv, execution, summary, user)
}
func (s *Service) Report(ctx context.Context, inv string) (Report, error) {
	return s.Repository.Report(ctx, inv)
}
func (s *Service) registerReview(api *httpapi.Server) {
	decisionHandler := func(decision string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var v struct {
				Digest string `json:"digest"`
				Reason string `json:"reason"`
			}
			if !httpapi.Decode(w, r, &v) {
				return
			}
			p, e := s.Decide(r.Context(), r.PathValue("id"), v.Digest, decision, v.Reason, httpapi.CurrentUser(r).ID)
			respond(w, p, e)
		}
	}
	api.Register("POST /api/v1/action-plans/{id}/reject", "operator", decisionHandler("rejected"))
	api.Register("POST /api/v1/action-plans/{id}/revoke", "operator", decisionHandler("revoked"))

	api.Register("POST /api/v1/investigations/{id}/close", "operator", func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			ExecutionID string `json:"execution_id"`
			Summary     string `json:"summary"`
		}
		if !httpapi.Decode(w, r, &v) {
			return
		}
		report, e := s.CloseInvestigation(r.Context(), r.PathValue("id"), v.ExecutionID, v.Summary, httpapi.CurrentUser(r).ID)
		respond(w, report, e)
	})
	api.Register("GET /api/v1/investigations/{id}/report", "viewer", func(w http.ResponseWriter, r *http.Request) {
		report, e := s.Report(r.Context(), r.PathValue("id"))
		respond(w, report, e)
	})
}
