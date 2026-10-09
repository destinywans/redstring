package v3settings

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

func TestProfileWithoutIdentityAuthorityServesOnlyLocalFacts(t *testing.T) {
	s := isolatedPreferencesStore(t)
	ctx := t.Context()
	password := "isolated-profile-evidence-password"
	if _, err := s.DB.Exec(ctx, `UPDATE users SET password_hash=$1 WHERE id='one'`, store.PasswordHash(password)); err != nil {
		t.Fatal(err)
	}
	// The built-in viewer carries no environment; grant the one environment this test reads.
	if _, err := s.DB.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('viewer','environment','view','environment','dev') ON CONFLICT DO NOTHING`); err != nil {
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
		r.Header.Set("Accept-Language", "en-US")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}

	// Without an instance boundary the local factors cannot be read: still unavailable.
	if w := request(http.MethodGet, "/api/v1/me/profile", ""); w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), `"code":"identity_evidence_unavailable"`) {
		t.Fatalf("unbound profile: status=%d body=%s", w.Code, w.Body.String())
	}

	service.ProfileInstanceID = "instance-a"
	service.MeEvidence.MFAInstanceID = "instance-a"
	service.MeEvidence.PasskeyRPID = "prod.example.invalid"
	w := request(http.MethodGet, "/api/v1/me/profile", "")
	if w.Code != http.StatusOK {
		t.Fatalf("local profile: status=%d body=%s", w.Code, w.Body.String())
	}
	var got profileView
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "One" || !got.Partial || len(got.Contacts) != 1 || got.Contacts[0].Verified != nil || got.Security.OrgRule != nil {
		t.Fatalf("local profile invented identity evidence: %+v", got)
	}
	if got.Security.MFAEnabled == nil || *got.Security.MFAEnabled || len(got.Security.Factors) != 0 {
		t.Fatalf("local MFA state must follow this instance's enrollments: %+v", got.Security)
	}
	if len(got.Security.Sessions) != 1 || !got.Security.Sessions[0].Current || got.Security.Sessions[0].Device != nil {
		t.Fatalf("local sessions must leave the device unknown: %+v", got.Security.Sessions)
	}
	for _, field := range []string{"contacts.verified", "security.org_rule", "security.sessions.device"} {
		if !strings.Contains(strings.Join(got.UnavailableFields, ","), field) {
			t.Fatalf("unavailable_fields misses %s: %v", field, got.UnavailableFields)
		}
	}

	// Saving edits only the account's own fields; the unknowns go back as they came.
	got.Name, got.Title = "One Renamed", "SRE"
	body, _ := json.Marshal(got)
	if w := request(http.MethodPut, "/api/v1/me/profile", string(body)); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"name":"One Renamed"`) {
		t.Fatalf("local profile save: status=%d body=%s", w.Code, w.Body.String())
	}
	var auditCount int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE actor_id='one' AND action='v3.profile.updated'`).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("profile update audit count=%d err=%v", auditCount, err)
	}

	// Claiming a verification the instance cannot know is rejected.
	verified := true
	got.Contacts[0].Verified = &verified
	body, _ = json.Marshal(got)
	if w := request(http.MethodPut, "/api/v1/me/profile", string(body)); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("forged verification: status=%d body=%s", w.Code, w.Body.String())
	}
	var name string
	if err := s.DB.QueryRow(ctx, `SELECT name FROM users WHERE id='one'`).Scan(&name); err != nil || name != "One Renamed" {
		t.Fatalf("rejected PUT changed the account: name=%q err=%v", name, err)
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
