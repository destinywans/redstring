package v3settings

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

func TestNotificationSettingsGETReadsOnlyCurrentUsersPersistedPreferences(t *testing.T) {
	st := isolatedPreferencesStore(t)
	ctx := t.Context()
	password := "isolated-notifications-password"
	for _, id := range []string{"one", "two"} {
		if _, err := st.DB.Exec(ctx, `UPDATE users SET password_hash=$1 WHERE id=$2`, store.PasswordHash(password), id); err != nil {
			t.Fatal(err)
		}
	}
	_, oneToken, err := st.Login(ctx, "one@example.invalid", password)
	if err != nil {
		t.Fatal(err)
	}
	_, twoToken, err := st.Login(ctx, "two@example.invalid", password)
	if err != nil {
		t.Fatal(err)
	}
	api := httpapi.New(st, nil, true)
	New(st, nil).Register(api)
	api.MarkV3Route("GET /api/v1/me/notification-settings", "", "")
	if err = api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}},
		allowPreferencesEnvironment{}, st.Session, nil); err != nil {
		t.Fatal(err)
	}
	request := func(token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/me/notification-settings", nil)
		r.Header.Set("X-AIOps-Environment", "dev")
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	read := func(token string) notificationSettings {
		t.Helper()
		w := request(token)
		if w.Code != http.StatusOK {
			t.Fatalf("GET status=%d body=%s", w.Code, w.Body.String())
		}
		var got notificationSettings
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := request(""); got.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET status=%d body=%s", got.Code, got.Body.String())
	}
	defaultValue := read(twoToken)
	if defaultValue.Quiet != (quietHours{From: "22:00", To: "08:00"}) || defaultValue.Digest != "daily" || defaultValue.Subscriptions == nil || len(defaultValue.Subscriptions) != 0 {
		t.Fatalf("default preferences misrepresented: %+v", defaultValue)
	}
	_, err = st.DB.Exec(ctx, `INSERT INTO v3_user_settings(user_id,notification_settings) VALUES('one',$1::jsonb)`,
		`{"quiet":{"enabled":true,"from":"21:30","to":"07:00"},"digest":"weekly","subscriptions":[{"id":"proposal","label":"Approvals","sub":"My proposals","on":true}]}`)
	if err != nil {
		t.Fatal(err)
	}
	one := read(oneToken)
	if !one.Quiet.Enabled || one.Quiet.From != "21:30" || one.Digest != "weekly" || len(one.Subscriptions) != 1 || one.Subscriptions[0].ID != "proposal" || !one.Subscriptions[0].On {
		t.Fatalf("persisted preferences omitted: %+v", one)
	}
	if got := read(twoToken); !reflect.DeepEqual(got, defaultValue) {
		t.Fatalf("one user's preferences leaked to another: %+v", got)
	}
	_, err = st.DB.Exec(ctx, `UPDATE v3_user_settings SET notification_settings='{"digest":"hourly"}'::jsonb WHERE user_id='one'`)
	if err != nil {
		t.Fatal(err)
	}
	if w := request(oneToken); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("malformed persisted preferences must fail closed: status=%d body=%s", w.Code, w.Body.String())
	}
}
