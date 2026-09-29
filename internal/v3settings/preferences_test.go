package v3settings

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

type allowPreferencesEnvironment struct{}

func (allowPreferencesEnvironment) AuthorizeEnvironment(_ context.Context, _ core.User, _ string) error {
	return nil
}

func TestPreferencesAuthenticatedHTTPAndEnvironment(t *testing.T) {
	s := isolatedPreferencesStore(t)
	ctx := t.Context()
	password := "isolated-preferences-test-password"
	for _, id := range []string{"one", "two"} {
		if _, err := s.DB.Exec(ctx, `UPDATE users SET password_hash=$1 WHERE id=$2`, store.PasswordHash(password), id); err != nil {
			t.Fatal(err)
		}
	}
	_, oneToken, err := s.Login(ctx, "one@example.invalid", password)
	if err != nil {
		t.Fatal(err)
	}
	_, twoToken, err := s.Login(ctx, "two@example.invalid", password)
	if err != nil {
		t.Fatal(err)
	}
	api := httpapi.New(s, nil, true)
	New(s, nil).Register(api)
	api.MarkV3Route("PATCH /api/v1/me/preferences", "", "")
	err = api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "test"}}, allowPreferencesEnvironment{}, s.Session, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := func(token, environment, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPatch, "/api/v1/me/preferences", strings.NewReader(body))
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		}
		r.Header.Set("X-AIOps-Environment", environment)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	assertPreferences := func(w *httptest.ResponseRecorder, expected Preferences) {
		t.Helper()
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d, body=%s", w.Code, w.Body.String())
		}
		var got Preferences
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got != expected {
			t.Fatalf("response=%+v, error=%v, want=%+v", got, err, expected)
		}
		var fields map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &fields); err != nil || len(fields) != 4 {
			t.Fatalf("response fields=%v, error=%v", fields, err)
		}
	}
	assertError := func(w *httptest.ResponseRecorder, status int, code string) {
		t.Helper()
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != status || body.Error.Code != code {
			t.Fatalf("status=%d, body=%s, error=%v; want %d %s", w.Code, w.Body.String(), err, status, code)
		}
	}
	assertError(request("", "dev", `{"theme":"dark"}`), http.StatusUnauthorized, "unauthorized")
	assertPreferences(request(oneToken, "dev", `{"theme":"dark","reduce_motion":true}`), Preferences{Theme: "dark", Locale: "zh-CN", Density: "comfortable", ReduceMotion: true})
	assertPreferences(request(oneToken, "dev", `{"locale":"en-US","reduce_motion":false}`), Preferences{Theme: "dark", Locale: "en-US", Density: "comfortable"})
	assertPreferences(request(twoToken, "test", `{}`), Preferences{Theme: "system", Locale: "zh-CN", Density: "comfortable"})
	for _, body := range []string{`{"theme":"invalid"}`, `{"unknown":true}`, `{"locale":null}`, `null`, `[]`} {
		w := request(oneToken, "dev", body)
		if w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("invalid %s: status=%d body=%s", body, w.Code, w.Body.String())
		}
	}
	assertError(request(oneToken, "all", `{"theme":"light"}`), http.StatusConflict, "env_view_only")
	stored, err := New(s, nil).prefs(ctx, "one")
	if err != nil || stored != (Preferences{Theme: "dark", Locale: "en-US", Density: "comfortable"}) {
		t.Fatalf("invalid/all writes changed data: %+v, %v", stored, err)
	}
	other, err := New(s, nil).prefs(ctx, "two")
	if err != nil || other != (Preferences{Theme: "system", Locale: "zh-CN", Density: "comfortable"}) {
		t.Fatalf("account isolation: %+v, %v", other, err)
	}
}

func TestDecodePreferencesPatch(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		ok   bool
	}{
		{"partial", `{"theme":"dark","reduce_motion":false}`, true},
		{"empty patch", `{}`, true},
		{"null object", `null`, false},
		{"null field", `{"theme":null}`, false},
		{"spaced null field", `{"theme": null }`, false},
		{"unknown field", `{"theme":"dark","other":true}`, false},
		{"wrong type", `{"reduce_motion":"false"}`, false},
		{"array", `[]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			patch, ok := decodePreferencesPatch(w, httptest.NewRequest(http.MethodPatch, "/api/v1/me/preferences", strings.NewReader(tc.body)))
			if ok != tc.ok {
				t.Fatalf("ok=%v, status=%d body=%s", ok, w.Code, w.Body.String())
			}
			if !ok && w.Code != http.StatusUnprocessableEntity {
				t.Fatalf("invalid body status=%d, want 422", w.Code)
			}
			if tc.name == "partial" && (patch.Theme == nil || *patch.Theme != "dark" || patch.ReduceMotion == nil || *patch.ReduceMotion) {
				t.Fatalf("missing submitted values: %+v", patch)
			}
		})
	}
}

func isolatedPreferencesStore(t *testing.T) *store.Store {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops only a random isolated schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_preferences_test_" + store.ID()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema+",public")
	q.Set("application_name", schema)
	u.RawQuery = q.Encode()
	s, err := store.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = s.DB.Exec(ctx, `INSERT INTO users(id,email,name,role,password_hash) VALUES
	 ('one','one@example.invalid','One','viewer','unused'),
	 ('two','two@example.invalid','Two','viewer','unused')`)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPreferencesPersistIsolationAndAudit(t *testing.T) {
	s := isolatedPreferencesStore(t)
	service := New(s, nil)
	ctx := t.Context()
	dark, compact, english := "dark", "compact", "en-US"
	got, err := service.savePrefs(ctx, "one", preferencesPatch{Theme: &dark, ReduceMotion: boolPtr(true)})
	if err != nil || got != (Preferences{Theme: "dark", Locale: "zh-CN", Density: "comfortable", ReduceMotion: true}) {
		t.Fatalf("initial patch: %+v, %v", got, err)
	}
	got, err = service.savePrefs(ctx, "one", preferencesPatch{Locale: &english, Density: &compact, ReduceMotion: boolPtr(false)})
	if err != nil || got != (Preferences{Theme: "dark", Locale: "en-US", Density: "compact", ReduceMotion: false}) {
		t.Fatalf("partial patch or explicit false: %+v, %v", got, err)
	}
	stored, err := service.prefs(ctx, "one")
	if err != nil || stored != got {
		t.Fatalf("durable read: %+v, %v", stored, err)
	}
	other, err := service.prefs(ctx, "two")
	if err != nil || other != (Preferences{Theme: "system", Locale: "zh-CN", Density: "comfortable"}) {
		t.Fatalf("another user's defaults: %+v, %v", other, err)
	}
	var count int
	var changes []byte
	err = s.DB.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE actor_id='one' AND action='v3.preferences.updated' AND target='one' AND outcome='success'`).Scan(&count)
	if err != nil || count != 2 {
		t.Fatalf("audit count=%d err=%v", count, err)
	}
	err = s.DB.QueryRow(ctx, `SELECT changes FROM management_audit WHERE actor_id='one' AND action='v3.preferences.updated' ORDER BY id DESC LIMIT 1`).Scan(&changes)
	if err != nil {
		t.Fatal(err)
	}
	var recorded map[string]any
	if err = json.Unmarshal(changes, &recorded); err != nil || recorded["locale"] != "en-US" || recorded["reduce_motion"] != false {
		t.Fatalf("audit changes=%s err=%v", changes, err)
	}
}

func TestPreferencesAuditFailureRollsBackUpdate(t *testing.T) {
	s := isolatedPreferencesStore(t)
	ctx := t.Context()
	if _, err := s.DB.Exec(ctx, `CREATE FUNCTION reject_preferences_audit() RETURNS trigger LANGUAGE plpgsql AS $$
	BEGIN
		IF NEW.action = 'v3.preferences.updated' THEN
			RAISE EXCEPTION 'test audit failure';
		END IF;
		RETURN NEW;
	END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, `CREATE TRIGGER reject_preferences_audit BEFORE INSERT ON management_audit
		FOR EACH ROW EXECUTE FUNCTION reject_preferences_audit()`); err != nil {
		t.Fatal(err)
	}
	dark := "dark"
	if _, err := New(s, nil).savePrefs(ctx, "one", preferencesPatch{Theme: &dark}); err == nil {
		t.Fatal("audit failure must reject the preferences update")
	}
	got, err := New(s, nil).prefs(ctx, "one")
	if err != nil || got != (Preferences{Theme: "system", Locale: "zh-CN", Density: "comfortable"}) {
		t.Fatalf("preferences changed despite audit failure: %+v, %v", got, err)
	}
}

func TestConcurrentPreferencesPatchesPreserveSeparateFields(t *testing.T) {
	s := isolatedPreferencesStore(t)
	service := New(s, nil)
	ctx := t.Context()
	dark, compact := "dark", "compact"
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, patch := range []preferencesPatch{{Theme: &dark}, {Density: &compact}} {
		wg.Add(1)
		go func(p preferencesPatch) {
			defer wg.Done()
			_, err := service.savePrefs(ctx, "one", p)
			errs <- err
		}(patch)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := service.prefs(ctx, "one")
	if err != nil || got.Theme != "dark" || got.Density != "compact" {
		t.Fatalf("concurrent patches lost a field: %+v, %v", got, err)
	}
}

func boolPtr(v bool) *bool { return &v }
