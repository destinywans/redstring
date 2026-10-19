package v3settings

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
)

type meOncallSubstitute struct {
	status OncallStatus
	err    error
	user   string
	envs   []string
}

func freshOncall(active bool) OncallStatus {
	now := time.Now().UTC()
	status := OncallStatus{Active: active, observedAt: now, validUntil: now.Add(2 * time.Minute)}
	if active {
		status.Schedule = "Primary"
		status.assignmentUntil = now.Add(time.Hour)
		status.Until = status.assignmentUntil.Format(time.RFC3339)
	}
	return status
}

func (s *meOncallSubstitute) Current(_ context.Context, userID string, ids []string) (OncallStatus, error) {
	s.user, s.envs = userID, append([]string(nil), ids...)
	return s.status, s.err
}

func TestOncallHTTPSourceTrustBoundary(t *testing.T) {
	now := time.Now().UTC()
	var mu sync.Mutex
	answer := oncallAnswer{UserID: "one", EnvironmentIDs: []string{"dev"}, ObservedAt: now.Format(time.RFC3339Nano), ValidUntil: now.Add(2 * time.Minute).Format(time.RFC3339Nano), Assignments: []oncallAssignment{{EnvironmentID: "dev", Schedule: "Primary", Until: now.Add(time.Hour).Format(time.RFC3339Nano)}}}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer private-token" {
			t.Errorf("untrusted adapter request: method=%s authorization=%q", r.Method, r.Header.Get("Authorization"))
		}
		var body struct {
			UserID         string   `json:"user_id"`
			EnvironmentIDs []string `json:"environment_ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.UserID != "one" || len(body.EnvironmentIDs) != 1 || body.EnvironmentIDs[0] != "dev" {
			t.Errorf("incorrect scoped lookup: %+v %v", body, err)
		}
		mu.Lock()
		defer mu.Unlock()
		_ = json.NewEncoder(w).Encode(answer)
	}))
	defer server.Close()
	if _, err := NewOncallHTTPSource("http://example.invalid/current", "token", nil); !errors.Is(err, ErrOncallUnavailable) {
		t.Fatalf("plain HTTP allowed: %v", err)
	}
	source, err := NewOncallHTTPSource(server.URL, "private-token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	got, err := source.Current(t.Context(), "one", []string{"dev"})
	if err != nil || !got.Active || got.Schedule != "Primary" || got.Until == "" {
		t.Fatalf("fresh scoped answer: %+v %v", got, err)
	}
	mu.Lock()
	answer.UserID = "two"
	mu.Unlock()
	if _, err := source.Current(t.Context(), "one", []string{"dev"}); !errors.Is(err, ErrOncallUnavailable) {
		t.Fatalf("other user accepted: %v", err)
	}
	mu.Lock()
	answer.UserID = "one"
	answer.ObservedAt = now.Add(-2 * time.Minute).Format(time.RFC3339Nano)
	mu.Unlock()
	if _, err := source.Current(t.Context(), "one", []string{"dev"}); !errors.Is(err, ErrOncallUnavailable) {
		t.Fatalf("stale answer accepted: %v", err)
	}
	mu.Lock()
	answer.ObservedAt = now.Format(time.RFC3339Nano)
	answer.Assignments = nil
	mu.Unlock()
	got, err = source.Current(t.Context(), "one", []string{"dev"})
	if err != nil || got.Active {
		t.Fatalf("covered empty schedule must mean inactive: %+v %v", got, err)
	}
	mu.Lock()
	answer.EnvironmentIDs = []string{"test"}
	mu.Unlock()
	if _, err := source.Current(t.Context(), "one", []string{"dev"}); !errors.Is(err, ErrOncallUnavailable) {
		t.Fatalf("wrong environment accepted: %v", err)
	}
}

func TestOncallTokenFilePermission(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "oncall-token")
	if err := os.WriteFile(path, []byte("private-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadOncallTokenFile(path); err != nil || got != "private-token" {
		t.Fatalf("protected token read: %q %v", got, err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOncallTokenFile(path); !errors.Is(err, ErrOncallUnavailable) {
		t.Fatalf("readable token file accepted: %v", err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOncallTokenFile(link); !errors.Is(err, ErrOncallUnavailable) {
		t.Fatalf("symlink accepted: %v", err)
	}
}

func TestMeHTTPAuthenticatedProjectionPatchAndAudit(t *testing.T) {
	st := isolatedPreferencesStore(t)
	ctx := t.Context()
	password := "isolated-me-password"
	if _, err := st.DB.Exec(ctx, `UPDATE users SET password_hash=$1 WHERE id IN ('one','two')`, store.PasswordHash(password)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('viewer','environment','view','environment','dev')`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(ctx, `INSERT INTO teams(id,name) VALUES('me-team','Me Team')`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(ctx, `UPDATE users SET department_id='me-team',department='Engineering',job_title='SRE',phone='+10000000000' WHERE id='one'`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(ctx, `INSERT INTO v3_mfa_totp(instance_id,user_id,generation,state,seed_nonce,seed_ciphertext,key_id,activated_at)
 VALUES('i-test','one','00000000000000000000000000000001','active',$1,$2,'00000000000000000000000000000001',now())`, make([]byte, 12), make([]byte, 36)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(ctx, `INSERT INTO v3_passkey_accounts(instance_id,rp_id,user_id,user_handle) VALUES('i-test','example.invalid','one',$1)`, make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(ctx, `INSERT INTO v3_passkey_credentials(instance_id,rp_id,user_id,credential_id,credential_json)
 VALUES('i-test','example.invalid','one',$1,'{}'::jsonb)`, []byte{1}); err != nil {
		t.Fatal(err)
	}
	_, token, err := st.Login(ctx, "one@example.invalid", password)
	if err != nil {
		t.Fatal(err)
	}
	oncall := &meOncallSubstitute{status: freshOncall(true)}
	service := New(st, nil)
	service.MeEvidence = MeEvidence{MFAInstanceID: "i-test", PasskeyRPID: "example.invalid", Oncall: oncall}
	api := httpapi.New(st, nil, true)
	service.Register(api)
	api.MarkV3Route("GET /api/v1/me", "", "")
	api.MarkV3Route("PATCH /api/v1/me", "", "")
	policy := &rbac.V3Policy{Store: st}
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}}, policy, st.Session, nil); err != nil {
		t.Fatal(err)
	}
	request := func(method, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1/me", strings.NewReader(body))
		r.Header.Set("X-AIOps-Environment", "dev")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	read := request(http.MethodGet, "")
	if read.Code != http.StatusOK {
		t.Fatalf("GET /me: %d %s", read.Code, read.Body.String())
	}
	var got meView
	if err := json.Unmarshal(read.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != "one" || got.Name != "One" || got.Team != "Me Team" || got.Department != "Engineering" || got.Phone != "+10000000000" || got.Title != "SRE" || !got.MFA.TOTP || got.MFA.Passkeys != 1 || got.Oncall == nil || !got.Oncall.Active || got.OncallState != "verified" || got.Preferences.Theme != "system" || len(got.Roles) != 1 || got.Roles[0] != "viewer" {
		t.Fatalf("incorrect projection: %+v", got)
	}
	if oncall.user != "one" || len(oncall.envs) != 1 || oncall.envs[0] != "dev" {
		t.Fatalf("source lookup not scoped: %+v", oncall)
	}
	for _, body := range []string{`{"role":"admin"}`, `{"mfa":{"totp":false}}`, `{"oncall":{"active":false}}`, `{"email":"other@example.invalid"}`, `{"name":null}`, `{"preferences":{"unknown":true}}`} {
		w := request(http.MethodPatch, body)
		if w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("forbidden self-edit %s: %d %s", body, w.Code, w.Body.String())
		}
	}
	updated := request(http.MethodPatch, `{"name":"Updated","title":"Senior SRE","timezone":"UTC","preferences":{"theme":"dark","reduce_motion":true}}`)
	if updated.Code != http.StatusOK {
		t.Fatalf("PATCH /me: %d %s", updated.Code, updated.Body.String())
	}
	if err := json.Unmarshal(updated.Body.Bytes(), &got); err != nil || got.Name != "Updated" || got.Title != "Senior SRE" || got.Timezone != "UTC" || got.Preferences.Theme != "dark" || !got.Preferences.ReduceMotion || got.MFA.Passkeys != 1 {
		t.Fatalf("incorrect full updated DTO: %+v %v", got, err)
	}
	var auditCount int
	if err := st.DB.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE actor_id='one' AND action='v3.me.updated' AND changes->>'name'='Updated'`).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("missing exact account audit: %d %v", auditCount, err)
	}
	// A failing on-call authority makes on-call unknown; it no longer blocks reading or
	// editing the account, and unknown is never reported as "not on call".
	oncall.err = ErrOncallUnavailable
	failed := request(http.MethodPatch, `{"name":"SavedWithoutOncall"}`)
	if failed.Code != http.StatusOK || !strings.Contains(failed.Body.String(), `"oncall":null`) || !strings.Contains(failed.Body.String(), `"oncall_state":"unavailable"`) {
		t.Fatalf("failed on-call source: %d %s", failed.Code, failed.Body.String())
	}
	if err := st.DB.QueryRow(ctx, `SELECT name FROM users WHERE id='one'`).Scan(&got.Name); err != nil || got.Name != "SavedWithoutOncall" {
		t.Fatalf("account edit without on-call evidence: %q %v", got.Name, err)
	}
	service.MeEvidence.Oncall = nil
	if w := request(http.MethodGet, ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"oncall":null`) || !strings.Contains(w.Body.String(), `"oncall_state":"not_configured"`) {
		t.Fatalf("unconfigured on-call source: %d %s", w.Code, w.Body.String())
	}
	if _, err := st.DB.Exec(ctx, `UPDATE users SET disabled=true WHERE id='one'`); err != nil {
		t.Fatal(err)
	}
	if w := request(http.MethodGet, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("disabled account read: %d %s", w.Code, w.Body.String())
	}
}

func TestMeSessionRevocationRowsAreLocked(t *testing.T) {
	st := isolatedPreferencesStore(t)
	ctx := t.Context()
	password := "isolated-me-revocation-password"
	if _, err := st.DB.Exec(ctx, `UPDATE users SET password_hash=$1 WHERE id='one'`, store.PasswordHash(password)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('viewer','environment','view','environment','dev')`); err != nil {
		t.Fatal(err)
	}
	_, token, err := st.Login(ctx, "one@example.invalid", password)
	if err != nil {
		t.Fatal(err)
	}
	service := New(st, nil)
	service.MeEvidence = MeEvidence{MFAInstanceID: "i-test", PasskeyRPID: "example.invalid", Oncall: &meOncallSubstitute{status: freshOncall(false)}}
	user, err := st.Session(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	scope := httpapi.V3EnvironmentScope{EnvironmentIDs: []string{"dev"}}
	// A revocation that owns the session row first must prevent both reads and
	// writes. The row is held until release so each operation queues behind it.
	for _, write := range []bool{false, true} {
		revokeTx, err := st.DB.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := revokeTx.Exec(ctx, `DELETE FROM sessions WHERE token_hash=$1`, store.Hash(token)); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			var patch *mePatch
			if write {
				name := "RevokedWrite"
				patch = &mePatch{Name: &name}
			}
			_, opErr := service.meTransaction(context.Background(), user, store.Hash(token), scope, oncallEvidence{status: freshOncall(false), known: true, state: "verified"}, patch)
			done <- opErr
		}()
		select {
		case err := <-done:
			t.Fatalf("operation crossed queued revocation: %v", err)
		case <-time.After(30 * time.Millisecond):
		}
		if err := revokeTx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-done:
			if !errors.Is(err, errMeUnauthorized) {
				t.Fatalf("revoked operation returned %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("revoked operation did not finish")
		}
		if !write {
			if _, err := st.DB.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,'one',now()+interval '1 hour')`, store.Hash(token)); err != nil {
				t.Fatal(err)
			}
		}
	}
	var name string
	if err := st.DB.QueryRow(ctx, `SELECT name FROM users WHERE id='one'`).Scan(&name); err != nil || name != "One" {
		t.Fatalf("revoked write changed profile: %q %v", name, err)
	}
}

func TestMeEvidenceExpiresDuringLockWait(t *testing.T) {
	st := isolatedPreferencesStore(t)
	ctx := t.Context()
	password := "isolated-me-expiry-password"
	if _, err := st.DB.Exec(ctx, `UPDATE users SET password_hash=$1 WHERE id='one'`, store.PasswordHash(password)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('viewer','environment','view','environment','dev')`); err != nil {
		t.Fatal(err)
	}
	_, token, err := st.Login(ctx, "one@example.invalid", password)
	if err != nil {
		t.Fatal(err)
	}
	user, err := st.Session(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	service := New(st, nil)
	service.MeEvidence = MeEvidence{MFAInstanceID: "i-test", PasskeyRPID: "example.invalid"}
	scope := httpapi.V3EnvironmentScope{EnvironmentIDs: []string{"dev"}}
	for _, tc := range []struct {
		name      string
		expire    string
		wantError error
	}{
		{"session", "session", errMeUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := st.DB.Exec(ctx, `UPDATE sessions SET expires_at=now()+interval '1 hour' WHERE token_hash=$1`, store.Hash(token)); err != nil {
				t.Fatal(err)
			}
			status := freshOncall(false)
			if tc.expire == "session" {
				if _, err := st.DB.Exec(ctx, `UPDATE sessions SET expires_at=clock_timestamp()+interval '100 milliseconds' WHERE token_hash=$1`, store.Hash(token)); err != nil {
					t.Fatal(err)
				}
			} else {
				status.validUntil = time.Now().Add(100 * time.Millisecond)
			}
			block, err := st.DB.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := block.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(752018992)); err != nil {
				t.Fatal(err)
			}
			name := "ShouldNotSave"
			done := make(chan error, 1)
			go func() {
				_, opErr := service.meTransaction(context.Background(), user, store.Hash(token), scope, oncallEvidence{status: status, known: true, state: "verified"}, &mePatch{Name: &name})
				done <- opErr
			}()
			select {
			case err := <-done:
				t.Fatalf("operation did not wait for lock: %v", err)
			case <-time.After(150 * time.Millisecond):
			}
			if err := block.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if !errors.Is(err, tc.wantError) {
					t.Fatalf("expired operation returned %v; want %v", err, tc.wantError)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("expired operation did not finish")
			}
			var nameStored string
			if err := st.DB.QueryRow(ctx, `SELECT name FROM users WHERE id='one'`).Scan(&nameStored); err != nil || nameStored != "One" {
				t.Fatalf("expired evidence committed update: %q %v", nameStored, err)
			}
		})
	}
}

func TestMeOIDCUserFirstSessionRevocationDoesNotDeadlock(t *testing.T) {
	st := isolatedPreferencesStore(t)
	ctx := t.Context()
	password := "isolated-me-oidc-order-password"
	if _, err := st.DB.Exec(ctx, `UPDATE users SET password_hash=$1 WHERE id='one'`, store.PasswordHash(password)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('viewer','environment','view','environment','dev')`); err != nil {
		t.Fatal(err)
	}
	service := New(st, nil)
	service.MeEvidence = MeEvidence{MFAInstanceID: "i-test", PasskeyRPID: "example.invalid"}
	scope := httpapi.V3EnvironmentScope{EnvironmentIDs: []string{"dev"}}
	var applicationName string
	if err := st.DB.QueryRow(ctx, `SHOW application_name`).Scan(&applicationName); err != nil {
		t.Fatal(err)
	}
	for _, write := range []bool{false, true} {
		t.Run(map[bool]string{false: "GET", true: "PATCH"}[write], func(t *testing.T) {
			_, token, err := st.Login(ctx, "one@example.invalid", password)
			if err != nil {
				t.Fatal(err)
			}
			user, err := st.Session(ctx, token)
			if err != nil {
				t.Fatal(err)
			}
			// This is the order used by OIDC bind/unbind: lock the account,
			// then delete every active session for it.
			identityTx, err := st.DB.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer identityTx.Rollback(ctx)
			var locked string
			if err := identityTx.QueryRow(ctx, `SELECT id FROM users WHERE id='one' FOR UPDATE`).Scan(&locked); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				var patch *mePatch
				if write {
					name := "MustNotCommit"
					patch = &mePatch{Name: &name}
				}
				_, opErr := service.meTransaction(context.Background(), user, store.Hash(token), scope, oncallEvidence{status: freshOncall(false), known: true, state: "verified"}, patch)
				done <- opErr
			}()
			// Observe the read or write waiting on the account row before
			// deleting sessions. This would expose the previous session-first
			// cycle rather than relying on a scheduling sleep.
			deadline := time.Now().Add(3 * time.Second)
			for {
				var waiting bool
				err := st.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=$1 AND wait_event_type='Lock' AND query LIKE 'SELECT id FROM users WHERE id=$1 AND NOT disabled%')`, applicationName).Scan(&waiting)
				if err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				select {
				case err := <-done:
					t.Fatalf("account operation ended before lock contention: %v", err)
				default:
				}
				if time.Now().After(deadline) {
					t.Fatal("account operation did not reach user row lock")
				}
				time.Sleep(10 * time.Millisecond)
			}
			deleteCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			if _, err := identityTx.Exec(deleteCtx, `DELETE FROM sessions WHERE user_id='one'`); err != nil {
				t.Fatalf("OIDC-style session deletion deadlocked with /me: %v", err)
			}
			if err := identityTx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if !errors.Is(err, errMeUnauthorized) {
					t.Fatalf("revoked /me operation returned %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("/me did not finish after identity transaction")
			}
			var name string
			if err := st.DB.QueryRow(ctx, `SELECT name FROM users WHERE id='one'`).Scan(&name); err != nil || name != "One" {
				t.Fatalf("revoked /me changed account: %q %v", name, err)
			}
		})
	}
}

func TestMeOncallRedirectIsUnavailable(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("on-call credential followed a redirect")
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	source, err := NewOncallHTTPSource(redirect.URL, "private-token", redirect.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Current(t.Context(), "one", []string{"dev"}); !errors.Is(err, ErrOncallUnavailable) {
		t.Fatalf("redirect accepted: %v", err)
	}
}
