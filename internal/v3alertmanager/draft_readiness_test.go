package v3alertmanager

import (
	"context"
	"testing"

	"github.com/wans/aiops-canvas/internal/v3configsync"
)

type fakeDraftRepository struct {
	project        int64
	prefix, target string
}

func (r fakeDraftRepository) ProjectID() int64     { return r.project }
func (r fakeDraftRepository) BranchPrefix() string { return r.prefix }
func (r fakeDraftRepository) TargetBranch() string { return r.target }
func (fakeDraftRepository) Export(context.Context, string, string, []byte, string) (v3configsync.MR, error) {
	return v3configsync.MR{}, nil
}

type fakeDraftSource struct{ fixedRouteTree }

func (fakeDraftSource) ObserveInhibitions(context.Context, string) (InhibitionObservation, error) {
	return InhibitionObservation{}, nil
}

// PA-123, PA-141: the list reads say a draft can't be exported exactly when the
// create routes would answer 501 not_connected.
func TestDraftReadinessMatchesTheCreateRoutes(t *testing.T) {
	ready := func() *Service {
		return &Service{
			Source:                      fakeDraftSource{},
			RouteRepository:             fakeDraftRepository{project: 7, prefix: "routes/", target: "routes-main"},
			InhibitionRepository:        fakeDraftRepository{project: 7, prefix: "inhibitions/", target: "inhibitions-main"},
			ConfigPromotionProjectID:    7,
			ConfigPromotionBranchPrefix: "promote/",
			ConfigPromotionTargetBranch: "main",
			EnvironmentLabels:           map[string]string{"prod": "redstring_env"},
		}
	}
	if s := ready(); !s.routeDraftsReady("prod") || !s.inhibitionDraftsReady("prod") {
		t.Fatalf("configured service: routes=%t inhibitions=%t, want both ready", s.routeDraftsReady("prod"), s.inhibitionDraftsReady("prod"))
	}
	for name, change := range map[string]func(*Service){
		"no repositories":        func(s *Service) { s.RouteRepository, s.InhibitionRepository = nil, nil },
		"another project":        func(s *Service) { s.ConfigPromotionProjectID = 8 },
		"no environment label":   func(s *Service) { s.EnvironmentLabels = map[string]string{} },
		"application label":      func(s *Service) { s.EnvironmentLabels["prod"] = "application" },
		"no Alertmanager source": func(s *Service) { s.Source = nil },
		"promotion branch reuse": func(s *Service) { s.ConfigPromotionTargetBranch = "routes-main" },
	} {
		s := ready()
		change(s)
		if name == "promotion branch reuse" {
			if s.routeDraftsReady("prod") {
				t.Errorf("%s: route drafts ready", name)
			}
			continue
		}
		if s.routeDraftsReady("prod") || s.inhibitionDraftsReady("prod") {
			t.Errorf("%s: routes=%t inhibitions=%t, want neither", name, s.routeDraftsReady("prod"), s.inhibitionDraftsReady("prod"))
		}
	}
	if s := ready(); s.routeDraftsReady("test") || s.inhibitionDraftsReady("test") {
		t.Error("an environment without a label must not be ready")
	}
}
