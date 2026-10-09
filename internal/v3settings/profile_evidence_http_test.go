package v3settings

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

type profileEvidenceSubstitute struct {
	err   error
	calls int
}

// Snapshot answers exactly the requested account, contacts and sessions.
func (p *profileEvidenceSubstitute) Snapshot(_ context.Context, req ProfileEvidenceRequest) (ProfileEvidenceSnapshot, error) {
	p.calls++
	if p.err != nil {
		return ProfileEvidenceSnapshot{}, p.err
	}
	now := time.Now()
	answer := ProfileEvidenceSnapshot{InstanceID: req.InstanceID, UserID: req.UserID, EnvironmentID: req.EnvironmentID,
		Complete: true, ObservedAt: now, ValidUntil: now.Add(time.Minute), Contacts: []ProfileVerifiedContact{},
		OrgRule: "MFA required for administrators", Factors: []ProfileFactorEvidence{}, Sessions: []ProfileSessionEvidence{}}
	for _, contact := range req.Contacts {
		answer.Contacts = append(answer.Contacts, ProfileVerifiedContact{Kind: contact.Kind, Value: contact.Value, Verified: true})
	}
	for _, id := range req.SessionIDs {
		answer.Sessions = append(answer.Sessions, ProfileSessionEvidence{ID: id, Device: "Laptop", LoginMethod: "password"})
	}
	return answer, nil
}

func profileTestAPI(t *testing.T, service *Service, token string) func(method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	api := httpapi.New(service.Store, nil, true)
	service.Register(api)
	api.MarkV3Route("GET /api/v1/me/profile", "", "")
	api.MarkV3Route("PUT /api/v1/me/profile", "", "")
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}}, allowPreferencesEnvironment{}, service.Store.Session, nil); err != nil {
		t.Fatal(err)
	}
	return func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("X-AIOps-Environment", "dev")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
}

func TestProfileServesKnownFieldsWithoutIdentityEvidence(t *testing.T) {
	s := isolatedPreferencesStore(t)
	ctx := t.Context()
	password := "isolated-profile-evidence-password"
	if _, err := s.DB.Exec(ctx, `UPDATE users SET password_hash=$1 WHERE id='one'`, store.PasswordHash(password)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('viewer','environment','view','environment','dev')`); err != nil {
		t.Fatal(err)
	}
	_, token, err := s.Login(ctx, "one@example.invalid", password)
	if err != nil {
		t.Fatal(err)
	}
	request := profileTestAPI(t, New(s, nil), token)
	w := request(http.MethodGet, "/api/v1/me/profile", "")
	var view profileView
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &view) != nil {
		t.Fatalf("profile without evidence: status=%d body=%s", w.Code, w.Body.String())
	}
	if view.Name != "One" || len(view.Contacts) != 1 || view.Contacts[0].Value != "one@example.invalid" || view.Contacts[0].Verified != nil ||
		view.Security.MFAEnabled != nil || view.Security.OrgRule != nil || view.Security.Factors != nil ||
		len(view.Security.Sessions) != 1 || !view.Security.Sessions[0].Current || view.Security.Sessions[0].Device != nil {
		t.Fatalf("profile fabricated identity evidence: %s", w.Body.String())
	}
	for _, field := range []string{"contacts[].verified", "contacts.feishu", "security.org_rule", "security.factors", "security.sessions[].device", "security.mfa_enabled"} {
		if view.Unavailable[field] != "not_configured" {
			t.Fatalf("unknown %s lacks its reason: %v", field, view.Unavailable)
		}
	}
	if !strings.Contains(w.Body.String(), `"verified":null`) || !strings.Contains(w.Body.String(), `"factors":null`) ||
		strings.Contains(w.Body.String(), "设备信息未记录") {
		t.Fatalf("unknown values must be explicit nulls: %s", w.Body.String())
	}

	// The GET body round-trips: editable fields change, unknown ones stay null.
	var edited map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &edited); err != nil {
		t.Fatal(err)
	}
	edited["name"] = "Changed"
	edited["notifications"] = []any{map[string]any{"event": "incident", "label": "Incident",
		"channels": map[string]any{"feishu": false, "email": true, "sms": false, "phone": false}}}
	body, _ := json.Marshal(edited)
	w = request(http.MethodPut, "/api/v1/me/profile", string(body))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"name":"Changed"`) || !strings.Contains(w.Body.String(), `"mfa_enabled":null`) {
		t.Fatalf("profile PUT without evidence: status=%d body=%s", w.Code, w.Body.String())
	}
	var name string
	var audits int
	if err := s.DB.QueryRow(ctx, `SELECT name,(SELECT count(*) FROM management_audit WHERE actor_id='one' AND action='v3.profile.updated') FROM users WHERE id='one'`).Scan(&name, &audits); err != nil || name != "Changed" || audits != 1 {
		t.Fatalf("profile PUT not persisted and audited: name=%q audits=%d err=%v", name, audits, err)
	}
	// Claiming an unknown read-only value is rejected without a write.
	for _, mutate := range []func(map[string]any){
		func(v map[string]any) { v["security"].(map[string]any)["mfa_enabled"] = false },
		func(v map[string]any) { v["contacts"].([]any)[0].(map[string]any)["verified"] = true },
		func(v map[string]any) { v["security"].(map[string]any)["factors"] = []any{} },
		func(v map[string]any) {
			v["security"].(map[string]any)["sessions"].([]any)[0].(map[string]any)["device"] = "Laptop"
		},
	} {
		var changed map[string]any
		_ = json.Unmarshal(body, &changed)
		changed["name"] = "Rejected"
		mutate(changed)
		raw, _ := json.Marshal(changed)
		if w := request(http.MethodPut, "/api/v1/me/profile", string(raw)); w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("read-only change accepted: status=%d body=%s", w.Code, w.Body.String())
		}
	}
	if err := s.DB.QueryRow(ctx, `SELECT name,(SELECT count(*) FROM management_audit WHERE actor_id='one' AND action='v3.profile.updated') FROM users WHERE id='one'`).Scan(&name, &audits); err != nil || name != "Changed" || audits != 1 {
		t.Fatalf("rejected PUT modified the profile: name=%q audits=%d err=%v", name, audits, err)
	}
}

func TestProfileUsesCurrentEvidenceAndFallsBackWhenItFails(t *testing.T) {
	s := isolatedPreferencesStore(t)
	ctx := t.Context()
	password := "isolated-profile-evidence-password"
	if _, err := s.DB.Exec(ctx, `UPDATE users SET password_hash=$1 WHERE id='one'`, store.PasswordHash(password)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('viewer','environment','view','environment','dev')`); err != nil {
		t.Fatal(err)
	}
	_, token, err := s.Login(ctx, "one@example.invalid", password)
	if err != nil {
		t.Fatal(err)
	}
	evidence := &profileEvidenceSubstitute{}
	service := New(s, nil)
	service.MeEvidence = MeEvidence{MFAInstanceID: "i-test", PasskeyRPID: "example.invalid"}
	service.ProfileInstanceID, service.ProfileEvidence = "i-test", evidence
	request := profileTestAPI(t, service, token)
	w := request(http.MethodGet, "/api/v1/me/profile", "")
	var view profileView
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &view) != nil || len(view.Unavailable) != 0 ||
		view.Security.MFAEnabled == nil || *view.Security.MFAEnabled || view.Security.OrgRule == nil ||
		view.Security.Factors == nil || len(view.Contacts) != 1 || view.Contacts[0].Verified == nil || !*view.Contacts[0].Verified ||
		view.Security.Sessions[0].Device == nil || *view.Security.Sessions[0].Device != "Laptop" {
		t.Fatalf("profile with current evidence: status=%d body=%s", w.Code, w.Body.String())
	}
	// A failing source leaves only its fields unknown; local MFA enrollment
	// still answers mfa_enabled.
	evidence.err = errors.New("identity source down")
	w = request(http.MethodGet, "/api/v1/me/profile", "")
	view = profileView{}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &view) != nil ||
		view.Security.MFAEnabled == nil || *view.Security.MFAEnabled || view.Contacts[0].Verified != nil ||
		view.Unavailable["security.factors"] != "unavailable" || view.Unavailable["contacts[].verified"] != "unavailable" ||
		view.Unavailable["security.mfa_enabled"] != "" {
		t.Fatalf("profile with failing evidence: status=%d body=%s", w.Code, w.Body.String())
	}
	if evidence.calls != 2 {
		t.Fatalf("evidence source calls=%d", evidence.calls)
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
