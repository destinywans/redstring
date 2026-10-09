package v3settings

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

func TestProfileRoutesKeepUnknownIdentityEvidenceUnavailable(t *testing.T) {
	s := isolatedPreferencesStore(t)
	ctx := t.Context()
	password := "isolated-profile-evidence-password"
	if _, err := s.DB.Exec(ctx, `UPDATE users SET password_hash=$1 WHERE id='one'`, store.PasswordHash(password)); err != nil {
		t.Fatal(err)
	}
	_, token, err := s.Login(ctx, "one@example.invalid", password)
	if err != nil {
		t.Fatal(err)
	}

	api := httpapi.New(s, nil, true)
	service := New(s, nil)
	service.Register(api)
	api.MarkV3Route("GET /api/v1/me/profile", "", "")
	api.MarkV3Route("PUT /api/v1/me/profile", "", "")
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}}, allowPreferencesEnvironment{}, s.Session, nil); err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("X-AIOps-Environment", "dev")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	for _, tc := range []struct {
		method string
		body   string
	}{
		{http.MethodGet, ""},
		{http.MethodPut, `{"name":"Changed","display_name":"Changed","department":"","title":"","timezone":"UTC","contacts":[],"notifications":[]}`},
	} {
		w := request(tc.method, "/api/v1/me/profile", tc.body)
		if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), `"code":"identity_evidence_unavailable"`) {
			t.Fatalf("%s profile: status=%d body=%s", tc.method, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), `"mfa_enabled"`) || strings.Contains(w.Body.String(), `"verified"`) {
			t.Fatalf("%s profile exposed unsupported identity state: %s", tc.method, w.Body.String())
		}
	}
	var name string
	if err := s.DB.QueryRow(ctx, `SELECT name FROM users WHERE id='one'`).Scan(&name); err != nil || name != "One" {
		t.Fatalf("unavailable PUT modified user: name=%q err=%v", name, err)
	}
	var auditCount int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE actor_id='one' AND action='v3.profile.updated'`).Scan(&auditCount); err != nil || auditCount != 0 {
		t.Fatalf("unavailable PUT recorded a profile update: count=%d err=%v", auditCount, err)
	}

	// Exercise the private projection through authenticated middleware. It may
	// gather known account fields, but a caller must receive an error until the
	// missing evidence can satisfy the frozen ProfileView contract.
	var partial profileView
	var projectionErr error
	api.Register("GET /__profile_evidence_probe", "viewer", func(w http.ResponseWriter, r *http.Request) {
		partial, projectionErr = service.getProfile(r)
		respond(w, partial, projectionErr)
	})
	api.MarkV3Route("GET /__profile_evidence_probe", "", "")
	w := request(http.MethodGet, "/__profile_evidence_probe", "")
	if w.Code != http.StatusServiceUnavailable || !errors.Is(projectionErr, errIdentityUnavailable) {
		t.Fatalf("private projection: status=%d err=%v body=%s", w.Code, projectionErr, w.Body.String())
	}
	if partial.Name != "One" || partial.Security.MFAEnabled != nil || partial.Security.Factors != nil || len(partial.Contacts) != 1 || partial.Contacts[0].Verified != nil {
		t.Fatalf("private projection fabricated identity evidence: %+v", partial)
	}
	if len(partial.Security.Sessions) != 1 || !partial.Security.Sessions[0].Current || partial.Security.Sessions[0].Device != "设备信息未记录" {
		t.Fatalf("private projection fabricated session device: %+v", partial.Security.Sessions)
	}
}

func TestEqualContactsComparesVerificationValue(t *testing.T) {
	verifiedA, verifiedB, unverified := true, true, false
	contact := profileContact{Kind: "email", Label: "Email", Value: "one@example.invalid", Verified: &verifiedA}
	if !equalContacts([]profileContact{contact}, []profileContact{{Kind: contact.Kind, Label: contact.Label, Value: contact.Value, Verified: &verifiedB}}) {
		t.Fatal("identical verification values with different addresses should match")
	}
	if equalContacts([]profileContact{contact}, []profileContact{{Kind: contact.Kind, Label: contact.Label, Value: contact.Value, Verified: &unverified}}) {
		t.Fatal("changed verification value should be rejected")
	}
	if equalContacts([]profileContact{{Kind: contact.Kind, Label: contact.Label, Value: contact.Value}}, []profileContact{{Kind: contact.Kind, Label: contact.Label, Value: contact.Value, Verified: &unverified}}) {
		t.Fatal("unknown verification must not equal unverified")
	}
}
