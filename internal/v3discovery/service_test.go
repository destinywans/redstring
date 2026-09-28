package v3discovery

import (
	"context"
	"encoding/json"
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

func TestMentionPreviewUsesOnlyPersistedRecordFields(t *testing.T) {
	for _, tc := range []struct {
		name, lang, state, wantStatus, wantLabel string
	}{
		{"open zh", "zh-CN", "open", "warn", "待处理"},
		{"investigating en", "en-US", "investigating", "warn", "Investigating"},
		{"resolved zh", "zh-CN", "resolved", "ok", "已解决"},
		{"unknown en", "en-US", "unknown", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/api/v1/mentions/incident/inc-1/preview", nil)
			r.Header.Set("Accept-Language", tc.lang)
			v := previewOf(r, record{Kind: "incident", ID: "inc-1", Title: "High errors", EnvironmentID: "test", State: tc.state})
			if v.Status != tc.wantStatus || v.StatusLabel != tc.wantLabel || v.Subtitle == "" || v.Href != "/incidents/inc-1" {
				t.Fatalf("preview = %+v", v)
			}
			b, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			var payload map[string]any
			if err := json.Unmarshal(b, &payload); err != nil {
				t.Fatal(err)
			}
			if metrics, ok := payload["metrics"].([]any); !ok || len(metrics) != 0 {
				t.Fatalf("unverified metrics must be an empty array: %s", b)
			}
			if related, ok := payload["related"].([]any); !ok || len(related) != 0 {
				t.Fatalf("unverified relationships must be an empty array: %s", b)
			}
			if _, ok := payload["change"]; ok {
				t.Fatalf("unverified change must be absent: %s", b)
			}
			if tc.wantStatus == "" {
				if _, ok := payload["status_label"]; ok {
					t.Fatalf("unknown state must not be labeled: %s", b)
				}
			}
		})
	}

	r := httptest.NewRequest("GET", "/api/v1/mentions/service/id/preview", nil)
	v := previewOf(r, record{Kind: "service", ID: serviceRecordID("checkout", "test"), Title: "checkout", EnvironmentID: "test", Tier: "T1"})
	if v.Tier != "T1" || v.Status != "" || v.StatusLabel != "" {
		t.Fatalf("service preview invented health status: %+v", v)
	}
}
