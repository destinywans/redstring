package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/destinywans/redstring/internal/v3alertmanager"
	"github.com/destinywans/redstring/internal/v3configsync"
)

func draftLoaderPromotion(t *testing.T) (*v3configsync.Service, *v3configsync.GitLabClient) {
	t.Helper()
	tokenPath := filepath.Join(t.TempDir(), "gitlab-token")
	if err := os.WriteFile(tokenPath, []byte("synthetic-constructor-token"), 0600); err != nil {
		t.Fatal(err)
	}
	client, err := v3configsync.NewGitLabClient(v3configsync.Config{
		BaseURL: "https://gitlab.example.invalid", ProjectID: 42,
		TargetBranch: "main", BranchPrefix: "aiops/promotions/", TokenFile: tokenPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &v3configsync.Service{Repository: client}, client
}

func TestV3AlertRuleDraftRepositoriesOptional(t *testing.T) {
	promotion, base := draftLoaderPromotion(t)
	for _, scenario := range []struct {
		name              string
		route, inhibition bool
	}{
		{"route_absent_inhibition_absent", false, false},
		{"route_present_inhibition_absent", true, false},
		{"route_absent_inhibition_present", false, true},
		{"route_present_inhibition_present", true, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			// These concrete pointers are exactly what configureV3 passes when
			// either independent draft capability is unconfigured.
			var route, inhibition *v3alertmanager.RouteGitLabRepository
			var err error
			if scenario.route {
				route, err = loadV3NotificationRouteRepository(promotion, "route-review", "aiops-route-drafts/")
				if err != nil {
					t.Fatal(err)
				}
			}
			if scenario.inhibition {
				inhibition, err = v3alertmanager.NewInhibitionGitLabRepositoryFromPromotion(base, "inhibition-review", "aiops-inhibition-drafts/")
				if err != nil {
					t.Fatal(err)
				}
			}
			repository, err := loadV3AlertRuleDraftRepository(promotion, route, inhibition, "alert-review", alertRuleDraftPrefix)
			if err != nil || repository == nil {
				t.Fatalf("independent alert-rule drafts must configure: repository=%T error=%v", repository, err)
			}
			configured, ok := repository.(*alertRuleDraftGitLabRepository)
			if !ok || configured.ProjectID() != 42 || configured.BranchPrefix() != alertRuleDraftPrefix || configured.client.TargetBranch() != "alert-review" {
				t.Fatal("configured draft repository lost exact reviewed project/namespace")
			}
			if base.TargetBranch() != "main" || base.BranchPrefix() != "aiops/promotions/" {
				t.Fatal("draft construction changed promotion namespace")
			}
			service := &v3alertmanager.Service{
				RouteRepository:      optionalV3AlertmanagerRepository(route),
				InhibitionRepository: optionalV3AlertmanagerRepository(inhibition),
			}
			if (service.RouteRepository != nil) != scenario.route || (service.InhibitionRepository != nil) != scenario.inhibition {
				t.Fatal("optional Service interfaces must reflect actual configured capabilities")
			}
			if scenario.route && (service.RouteRepository != route || service.RouteRepository.TargetBranch() != "route-review") {
				t.Fatal("notification draft binding changed at Service boundary")
			}
			if scenario.inhibition && (service.InhibitionRepository != inhibition || service.InhibitionRepository.TargetBranch() != "inhibition-review") {
				t.Fatal("inhibition draft binding changed at Service boundary")
			}
		})
	}
}

func TestV3AlertRuleDraftRepositoryRejectsConflictingNamespaces(t *testing.T) {
	promotion, base := draftLoaderPromotion(t)
	for _, scenario := range []struct {
		name, target, prefix                                         string
		routeTarget, routePrefix, inhibitionTarget, inhibitionPrefix string
	}{
		{name: "promotion_target", target: "main", prefix: alertRuleDraftPrefix},
		{name: "missing_target", prefix: alertRuleDraftPrefix},
		{name: "missing_prefix", target: "alert-review"},
		{name: "wrong_prefix", target: "alert-review", prefix: "aiops/promotions/"},
		{name: "route_target", target: "alert-review", prefix: alertRuleDraftPrefix, routeTarget: "alert-review", routePrefix: "aiops-route-drafts/"},
		{name: "route_prefix", target: "alert-review", prefix: alertRuleDraftPrefix, routeTarget: "route-review", routePrefix: alertRuleDraftPrefix + "nested/"},
		{name: "inhibition_target", target: "alert-review", prefix: alertRuleDraftPrefix, inhibitionTarget: "alert-review", inhibitionPrefix: "aiops-inhibition-drafts/"},
		{name: "inhibition_prefix", target: "alert-review", prefix: alertRuleDraftPrefix, inhibitionTarget: "inhibition-review", inhibitionPrefix: alertRuleDraftPrefix},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var route, inhibition *v3alertmanager.RouteGitLabRepository
			var err error
			if scenario.routeTarget != "" {
				route, err = v3alertmanager.NewRouteGitLabRepositoryFromPromotion(base, scenario.routeTarget, scenario.routePrefix)
				if err != nil {
					t.Fatal(err)
				}
			}
			if scenario.inhibitionTarget != "" {
				inhibition, err = v3alertmanager.NewInhibitionGitLabRepositoryFromPromotion(base, scenario.inhibitionTarget, scenario.inhibitionPrefix)
				if err != nil {
					t.Fatal(err)
				}
			}
			if repository, err := loadV3AlertRuleDraftRepository(promotion, route, inhibition, scenario.target, scenario.prefix); err == nil || repository != nil {
				t.Fatal("invalid/matching draft namespace must be rejected")
			}
		})
	}
	if repository, err := loadV3AlertRuleDraftRepository(nil, nil, nil, "", ""); err != nil || repository != nil {
		t.Fatal("unconfigured write capability must stay disconnected")
	}
	for _, invalid := range []*v3configsync.Service{nil, {}, {Repository: (*v3configsync.GitLabClient)(nil)}} {
		if repository, err := loadV3AlertRuleDraftRepository(invalid, nil, nil, "alert-review", alertRuleDraftPrefix); err == nil || repository != nil {
			t.Fatal("configured draft requires an actual promotion client")
		}
	}
}

func TestV3InhibitionDraftRepositoryWithoutNotificationRoutes(t *testing.T) {
	promotion, _ := draftLoaderPromotion(t)
	var unconfiguredRoute *v3alertmanager.RouteGitLabRepository
	repository, err := loadV3InhibitionRepository(promotion, unconfiguredRoute, "inhibition-review", "aiops-inhibition-drafts/")
	if err != nil || repository == nil || repository.TargetBranch() != "inhibition-review" || repository.BranchPrefix() != "aiops-inhibition-drafts/" {
		t.Fatalf("independent inhibition capability must configure: repository=%T error=%v", repository, err)
	}
}

func TestV3InhibitionDraftRepositoryChecksPresentRoute(t *testing.T) {
	promotion, _ := draftLoaderPromotion(t)
	route, err := loadV3NotificationRouteRepository(promotion, "route-review", "aiops-route-drafts/")
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct {
		name, target, prefix string
		rejected             bool
	}{
		{"distinct", "inhibition-review", "aiops-inhibition-drafts/", false},
		{"route_target", "route-review", "aiops-inhibition-drafts/", true},
		{"route_prefix", "inhibition-review", "aiops-route-drafts/nested/", true},
		{"promotion_target", "main", "aiops-inhibition-drafts/", true},
		{"promotion_prefix", "inhibition-review", "aiops/promotions/nested/", true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			repository, err := loadV3InhibitionRepository(promotion, route, scenario.target, scenario.prefix)
			if scenario.rejected {
				if err == nil || repository != nil {
					t.Fatal("matching target or overlapping prefix must be rejected")
				}
			} else if err != nil || repository == nil {
				t.Fatalf("distinct namespaces must configure: %v", err)
			}
		})
	}
}
