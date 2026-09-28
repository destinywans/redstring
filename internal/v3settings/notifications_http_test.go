package v3settings

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
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

func TestNotificationSettingsPUTPersistsCurrentUserAndRejectsInvalidChanges(t *testing.T) {
	st := isolatedPreferencesStore(t)
	ctx := t.Context()
	password := "isolated-notifications-put-password"
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
	_, err = st.DB.Exec(ctx, `INSERT INTO v3_user_settings(user_id,notification_settings) VALUES('one',$1::jsonb)`,
		`{"quiet":{"enabled":false,"from":"21:30","to":"07:00"},"digest":"daily","subscriptions":[{"id":"proposal","label":"Approvals","sub":"My proposals","on":true}]}`)
	if err != nil {
		t.Fatal(err)
	}
	api := httpapi.New(st, nil, true)
	New(st, nil).Register(api)
	api.MarkV3Route("GET /api/v1/me/notification-settings", "", "")
	api.MarkV3Route("PUT /api/v1/me/notification-settings", "", "")
	if err = api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}},
		allowPreferencesEnvironment{}, st.Session, nil); err != nil {
		t.Fatal(err)
	}
	put := func(token, env, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPut, "/api/v1/me/notification-settings", strings.NewReader(body))
		r.Header.Set("X-AIOps-Environment", env)
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	if w := put("", "dev", `{"quiet":true}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated PUT: %d %s", w.Code, w.Body.String())
	}
	if w := put(oneToken, "all", `{"quiet":true}`); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "env_view_only") {
		t.Fatalf("all-environment PUT: %d %s", w.Code, w.Body.String())
	}
	w := put(oneToken, "dev", `{"quiet":true,"digest":"weekly","subscriptions":{"proposal":false}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("save PUT: %d %s", w.Code, w.Body.String())
	}
	var saved notificationSettings
	if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if !saved.Quiet.Enabled || saved.Quiet.From != "21:30" || saved.Quiet.To != "07:00" || saved.Digest != "weekly" || len(saved.Subscriptions) != 1 || saved.Subscriptions[0].On {
		t.Fatalf("returned setting is not the merged value: %+v", saved)
	}
	if got, err := New(st, nil).getNotifications(ctx, "one"); err != nil || !reflect.DeepEqual(got, saved) {
		t.Fatalf("saved value not persisted: %+v, %v", got, err)
	}
	if got, err := New(st, nil).getNotifications(ctx, "two"); err != nil || !reflect.DeepEqual(got, defaultNotifications()) {
		t.Fatalf("another user changed: %+v, %v", got, err)
	}
	for _, body := range []string{
		`{}`, `{"quiet":null}`, `{"quiet":{"enabled":true}}`, `{"digest":"hourly"}`,
		`{"subscriptions":{"missing":true}}`, `{"subscriptions":{"proposal":null}}`,
		`{"subscriptions":[{"id":"proposal","label":"Changed","sub":"My proposals","on":true}]}`,
		`{"unexpected":true}`,
	} {
		if w := put(oneToken, "dev", body); w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("invalid PUT %s: %d %s", body, w.Code, w.Body.String())
		}
	}
	if got, err := New(st, nil).getNotifications(ctx, "one"); err != nil || !reflect.DeepEqual(got, saved) {
		t.Fatalf("invalid PUT changed settings: %+v, %v", got, err)
	}
	var auditCount int
	if err := st.DB.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE actor_id='one' AND action='v3.notification_settings.updated'`).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("audit count after valid and invalid PUTs: %d, %v", auditCount, err)
	}
	if w := put(oneToken, "dev", `{"subscriptions":[{"id":"proposal","label":"Approvals","sub":"My proposals","on":true}]}`); w.Code != http.StatusOK {
		t.Fatalf("full DTO subscription update: %d %s", w.Code, w.Body.String())
	}
	if got, err := New(st, nil).getNotifications(ctx, "one"); err != nil || !got.Subscriptions[0].On {
		t.Fatalf("full DTO subscription flag not saved: %+v, %v", got, err)
	}
	if w := put(twoToken, "dev", `{"quiet":true}`); w.Code != http.StatusOK {
		t.Fatalf("second user's PUT: %d %s", w.Code, w.Body.String())
	}
	if got, err := New(st, nil).getNotifications(ctx, "one"); err != nil || !got.Quiet.Enabled || got.Digest != "weekly" {
		t.Fatalf("second user's PUT changed first user: %+v, %v", got, err)
	}
}

func TestNotificationSettingsPUTRollsBackWhenAuditFails(t *testing.T) {
	st := isolatedPreferencesStore(t)
	ctx := t.Context()
	_, err := st.DB.Exec(ctx, `CREATE FUNCTION reject_notification_audit() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN IF NEW.action='v3.notification_settings.updated' THEN RAISE EXCEPTION 'audit blocked'; END IF; RETURN NEW; END $$`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.DB.Exec(ctx, `CREATE TRIGGER reject_notification_audit BEFORE INSERT ON management_audit FOR EACH ROW EXECUTE FUNCTION reject_notification_audit()`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = New(st, nil).putNotifications(ctx, "one", map[string]json.RawMessage{"quiet": json.RawMessage(`true`)})
	if err == nil {
		t.Fatal("audit failure accepted")
	}
	var count int
	if err := st.DB.QueryRow(ctx, `SELECT count(*) FROM v3_user_settings WHERE user_id='one'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("settings row survived failed audit: %d, %v", count, err)
	}
}

func TestNotificationSettingsPUTSerializesPartialUpdates(t *testing.T) {
	st := isolatedPreferencesStore(t)
	ctx := t.Context()
	service := New(st, nil)
	for range 5 {
		if _, err := st.DB.Exec(ctx, `DELETE FROM v3_user_settings WHERE user_id='one'`); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		var errs [2]error
		for i, patch := range []map[string]json.RawMessage{
			{"quiet": json.RawMessage(`true`)},
			{"digest": json.RawMessage(`"weekly"`)},
		} {
			wg.Add(1)
			go func(i int, patch map[string]json.RawMessage) {
				defer wg.Done()
				<-start
				_, errs[i] = service.putNotifications(ctx, "one", patch)
			}(i, patch)
		}
		close(start)
		wg.Wait()
		if errs[0] != nil || errs[1] != nil {
			t.Fatalf("concurrent PUT errors: %v, %v", errs[0], errs[1])
		}
		got, err := service.getNotifications(ctx, "one")
		if err != nil || !got.Quiet.Enabled || got.Digest != "weekly" {
			t.Fatalf("concurrent partial updates lost: %+v, %v", got, err)
		}
	}
}
