package v3topology

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
)

type accessFunc func(rbac.V3Decision) error

func (f accessFunc) Authorize(_ context.Context, _ core.User, d rbac.V3Decision) error {
	return f(d)
}

func TestAuthorizedNodesRequireExactPersistedScope(t *testing.T) {
	decisions := []rbac.V3Decision{}
	s := Service{Access: accessFunc(func(d rbac.V3Decision) error {
		decisions = append(decisions, d)
		if d.Module != "resources" || d.Action != "view" || d.EnvironmentID != "test" || d.ApplicationID != "app-1" {
			return store.ErrForbidden
		}
		return nil
	})}
	rows := []candidate{
		{ID: "authorized", Name: "checkout", Links: []appLink{{ApplicationID: "app-1", EnvironmentID: "test"}, {ApplicationID: "app-2", EnvironmentID: "prod"}}},
		{ID: "cross-env", Name: "private", Links: []appLink{{ApplicationID: "app-1", EnvironmentID: "prod"}}},
		{ID: "unlinked", Name: "node", EnvironmentID: "test", TeamID: "team-2"},
	}
	nodes, err := s.authorizedNodes(context.Background(), core.User{ID: "user-1"}, rows, []string{"test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || !reflect.DeepEqual(nodes["authorized"].EnvironmentIDs, []string{"test"}) {
		t.Fatalf("resource policy or selected environment leaked: %+v", nodes)
	}
	for _, d := range decisions {
		if d.EnvironmentID != "test" {
			t.Fatalf("outside environment consulted: %+v", d)
		}
	}
}

func TestAuthorizedNodesFailClosedOnPolicyError(t *testing.T) {
	unavailable := errors.New("policy database unavailable")
	s := Service{Access: accessFunc(func(rbac.V3Decision) error { return unavailable })}
	_, err := s.authorizedNodes(context.Background(), core.User{ID: "user-1"}, []candidate{{ID: "r", EnvironmentID: "dev", FallbackEligible: true}}, []string{"dev"})
	if !errors.Is(err, unavailable) {
		t.Fatalf("policy error was suppressed: %v", err)
	}
}

func TestCollectorFallbackCannotOverrideApplicationLink(t *testing.T) {
	s := Service{Access: accessFunc(func(rbac.V3Decision) error { return nil })}
	rows := []candidate{
		{ID: "contradictory", EnvironmentID: "test", FallbackEligible: true,
			Links: []appLink{{ApplicationID: "app-prod", EnvironmentID: "prod"}}},
		{ID: "manual", EnvironmentID: "test"},
		{ID: "collector", EnvironmentID: "test", FallbackEligible: true},
	}
	nodes, err := s.authorizedNodes(context.Background(), core.User{ID: "user-1"}, rows, []string{"test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes["collector"].ID != "collector" {
		t.Fatalf("untrusted or contradictory environment exposed: %+v", nodes)
	}
}

func TestLinkedResourceCannotUseUnrelatedResourceOwnerTeam(t *testing.T) {
	decisions := []rbac.V3Decision{}
	s := Service{Access: accessFunc(func(d rbac.V3Decision) error {
		decisions = append(decisions, d)
		if d.TeamID == "resource-owner" {
			return nil
		}
		return store.ErrForbidden
	})}
	linked := candidate{ID: "linked", EnvironmentID: "test", TeamID: "resource-owner",
		Links: []appLink{{ApplicationID: "app-1", EnvironmentID: "test", TeamID: "app-owner"}}}
	allowed, err := s.canView(context.Background(), core.User{ID: "user-1"}, linked, "test")
	if err != nil || allowed {
		t.Fatalf("unrelated resource team granted linked application: allowed=%t err=%v", allowed, err)
	}
	for _, d := range decisions {
		if d.TeamID == "resource-owner" {
			t.Fatalf("consulted unrelated resource team: %+v", decisions)
		}
	}
	allowed, err = s.canView(context.Background(), core.User{ID: "user-1"}, candidate{
		ID: "unlinked", EnvironmentID: "test", TeamID: "resource-owner", FallbackEligible: true,
	}, "test")
	if err != nil || !allowed {
		t.Fatalf("unlinked collector team grant lost: allowed=%t err=%v", allowed, err)
	}
}

func TestHiddenAndAbsentCandidatesHaveIdenticalEmptyResponse(t *testing.T) {
	s := Service{Access: accessFunc(func(rbac.V3Decision) error { return store.ErrForbidden })}
	var expected string
	for _, candidates := range [][]candidate{
		nil,
		{{ID: "hidden", EnvironmentID: "test", FallbackEligible: true}},
	} {
		nodes, err := s.authorizedNodes(context.Background(), core.User{ID: "user-1"}, candidates, []string{"test"})
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		if !writeEmptyIfNoVisible(w, nodes, []collectionStatus{}) || w.Code != 200 {
			t.Fatalf("expected an empty 200 response, got %d", w.Code)
		}
		var body projection
		if err = json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.CompleteV3DTO || len(body.Nodes) != 0 || len(body.Edges) != 0 {
			t.Fatalf("empty scope disclosed records: %+v", body)
		}
		if len(nodes) != 0 {
			t.Fatalf("unexpected visible nodes: %+v", nodes)
		}
		if expected == "" {
			expected = w.Body.String()
		} else if w.Body.String() != expected {
			t.Fatalf("hidden and absent candidates gave different responses")
		}
	}
}

func TestNeighborhoodAndEnvironmentIntersection(t *testing.T) {
	nodes := map[string]node{
		"a": {ID: "a", EnvironmentIDs: []string{"dev", "test"}},
		"b": {ID: "b", EnvironmentIDs: []string{"test"}},
		"c": {ID: "c", EnvironmentIDs: []string{"dev"}},
		"d": {ID: "d", EnvironmentIDs: []string{"test"}},
	}
	if got := intersection(nodes["b"].EnvironmentIDs, nodes["c"].EnvironmentIDs); len(got) != 0 {
		t.Fatalf("cross-environment relationship leaked: %v", got)
	}
	links := []edge{{From: "a", To: "b"}, {From: "b", To: "d"}, {From: "a", To: "c"}}
	near, kept := neighborhood(nodes, links, "a", 1)
	if len(near) != 3 || len(kept) != 2 || near["d"].ID != "" {
		t.Fatalf("one-hop neighborhood incorrect: nodes=%+v edges=%+v", near, kept)
	}
	near, kept = neighborhood(nodes, links, "a", 2)
	if len(near) != 4 || len(kept) != 3 {
		t.Fatalf("two-hop neighborhood incorrect: nodes=%+v edges=%+v", near, kept)
	}
}
