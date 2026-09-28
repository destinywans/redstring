package v3discovery

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type accessFunc func(rbac.V3Decision) error

func (f accessFunc) Authorize(_ context.Context, _ core.User, d rbac.V3Decision) error {
	return f(d)
}

func TestServiceReferenceUsesCatalogIdentity(t *testing.T) {
	id := serviceRecordID("checkout-api", "staging")
	app, env, ok := parseServiceRecordID(id)
	if !ok || app != "checkout-api" || env != "staging" {
		t.Fatalf("round trip: %q %q %t", app, env, ok)
	}
	if href(record{Kind: "service", ID: id}) != "/services/"+id {
		t.Fatal("service link must use catalog identity")
	}
	for _, invalid := range []string{"", "%%%", serviceRecordID("", "staging"), serviceRecordID("checkout-api", "")} {
		if _, _, ok := parseServiceRecordID(invalid); ok {
			t.Fatalf("accepted invalid service reference %q", invalid)
		}
	}
	if supportedTier("T3") != "" || supportedTier("T1") != "T1" {
		t.Fatal("frontend tier must remain within the typed range")
	}
}

func TestServiceSearchUsesPersistedOwnerTeamFallback(t *testing.T) {
	var decisions []rbac.V3Decision
	s := New(nil, accessFunc(func(d rbac.V3Decision) error {
		decisions = append(decisions, d)
		if d.TeamID == "platform" {
			return nil
		}
		return store.ErrForbidden
	}))
	r := httptest.NewRequest("GET", "/api/v1/search", nil)
	err := s.authorize(r, record{Kind: "service", ApplicationID: "checkout-api", EnvironmentID: "staging", TeamID: "platform"})
	if err != nil || len(decisions) != 2 || decisions[0].ApplicationID != "checkout-api" || decisions[1].TeamID != "platform" || decisions[1].ApplicationID != "" {
		t.Fatalf("owner-team fallback: %v %+v", err, decisions)
	}
	decisions = nil
	err = s.authorize(r, record{Kind: "service", ApplicationID: "checkout-api", EnvironmentID: "staging"})
	if !errors.Is(err, store.ErrForbidden) || len(decisions) != 1 {
		t.Fatalf("missing team must not broaden access: %v %+v", err, decisions)
	}
}

func TestRepeatedSearchQueryIsRejected(t *testing.T) {
	w := httptest.NewRecorder()
	_, ok := parseQuery(w, httptest.NewRequest("GET", "/api/v1/search?q=alpha&q=beta", nil))
	if ok || w.Code != 422 || !strings.Contains(w.Body.String(), "invalid_query") {
		t.Fatalf("ambiguous query accepted: status=%d body=%s", w.Code, w.Body.String())
	}
}
