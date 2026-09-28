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
	"github.com/wans/aiops-canvas/internal/store"
)

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
	q.Set("search_path", schema)
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
