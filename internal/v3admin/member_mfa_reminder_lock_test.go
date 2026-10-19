package v3admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func reminderLockFixture(t *testing.T) (*Service, core.User, string) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops one random isolated schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_reminder_lock_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	var db *pgxpool.Pool
	t.Cleanup(func() {
		if db != nil {
			db.Close()
		}
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	cfg.ConnConfig.Tracer = reminderQueryErrors{t}
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err = st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = st.Bootstrap(ctx, "admin@example.invalid", "isolated-reminder-password"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO users(id,email,name,role,password_hash)
 VALUES('target','target@example.invalid','Reminder target','viewer','unused')`); err != nil {
		t.Fatal(err)
	}
	actor, token, err := st.Login(ctx, "admin@example.invalid", "isolated-reminder-password")
	if err != nil {
		t.Fatal(err)
	}
	return New(st), actor, token
}

type reminderQueryErrors struct{ t *testing.T }

func (reminderQueryErrors) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	return ctx
}
func (v reminderQueryErrors) TraceQueryEnd(_ context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	if data.Err != nil && !errors.Is(data.Err, pgx.ErrNoRows) {
		v.t.Logf("isolated reminder SQL failure: %v", data.Err)
	}
}

func TestMFAReminderPreparePersistsAndDeduplicatesPostgres(t *testing.T) {
	s, actor, token := reminderLockFixture(t)
	observed := time.Now().UTC()
	id, skip, err := s.prepareMFAReminder(t.Context(), actor, token, "dev", "target", observed)
	if err != nil || skip || id == "" {
		t.Fatalf("initial reminder preparation: id=%q skip=%t err=%v", id, skip, err)
	}
	var state string
	if err = s.Store.DB.QueryRow(t.Context(), `SELECT status FROM v3_mfa_reminder_attempts WHERE id=$1`, id).Scan(&state); err != nil || state != "pending" {
		t.Fatalf("durable pending reminder missing: status=%q err=%v", state, err)
	}
	// An in-flight send may have succeeded remotely; it must not be retried.
	if _, _, err = s.prepareMFAReminder(t.Context(), actor, token, "dev", "target", observed); !errors.Is(err, errMFAReminderUncertain) {
		t.Fatalf("pending attempt must block a duplicate: %v", err)
	}
	if _, err = s.Store.DB.Exec(t.Context(), `UPDATE v3_mfa_reminder_attempts SET status='accepted',accepted_at=clock_timestamp(),feishu_message_id='synthetic-receipt' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if retryID, retrySkip, err := s.prepareMFAReminder(t.Context(), actor, token, "dev", "target", observed); err != nil || !retrySkip || retryID != "" {
		t.Fatalf("accepted reminder not deduplicated: id=%q skip=%t err=%v", retryID, retrySkip, err)
	}
	var count int
	if err = s.Store.DB.QueryRow(t.Context(), `SELECT count(*) FROM v3_mfa_reminder_attempts`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate attempt persisted: count=%d err=%v", count, err)
	}
}

func TestMFAReminderConcurrentFirstPreparePostgres(t *testing.T) {
	s, actor, token := reminderLockFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	type result struct {
		id   string
		skip bool
		err  error
	}
	start := make(chan struct{})
	done := make(chan result, 2)
	for range 2 {
		go func() {
			<-start
			id, skip, err := s.prepareMFAReminder(ctx, actor, token, "dev", "target", time.Now().UTC())
			done <- result{id, skip, err}
		}()
	}
	close(start)
	successes, rejected := 0, 0
	for range 2 {
		select {
		case r := <-done:
			if r.err == nil && r.id != "" && !r.skip {
				successes++
			} else if errors.Is(r.err, errMFAReminderUncertain) {
				rejected++
			} else {
				t.Fatalf("unexpected concurrent prepare: %+v", r)
			}
		case <-ctx.Done():
			t.Fatal("concurrent preparation did not finish")
		}
	}
	var count int
	if err := s.Store.DB.QueryRow(ctx, `SELECT count(*) FROM v3_mfa_reminder_attempts`).Scan(&count); err != nil || count != 1 || successes != 1 || rejected != 1 {
		t.Fatalf("first-send serialization: count=%d success=%d rejected=%d err=%v", count, successes, rejected, err)
	}
}

type reminderProtocolEvidence struct{}

func (reminderProtocolEvidence) Snapshot(_ context.Context, _ string, ids []string) (MFASnapshot, error) {
	grace := 7
	// Authoritative sources may supply nanoseconds, while PostgreSQL stores
	// timestamps at microsecond precision. Keep a deterministic remainder.
	v := MFASnapshot{Complete: true, ObservedAt: time.Now().UTC().Truncate(time.Microsecond).Add(123 * time.Nanosecond), GraceDays: &grace, Members: map[string]MFAMemberEvidence{}}
	for _, id := range ids {
		v.Members[id] = MFAMemberEvidence{Enrollment: "enrolled", FactorKind: "totp"}
	}
	if _, ok := v.Members["target"]; ok {
		v.Members["target"] = MFAMemberEvidence{Enrollment: "unenrolled", FactorKind: "none", FeishuOpenID: "ou_synthetic12345"}
	}
	return v, nil
}

type reminderProtocolSender struct {
	calls        atomic.Int32
	loseResponse atomic.Bool
}

func (s *reminderProtocolSender) SendMFAReminder(_ context.Context, v FeishuMFAReminder) (FeishuMFAReminderReceipt, error) {
	if v.OpenID != "ou_synthetic12345" || v.Environment != "dev" || v.DeliveryID == "" {
		return FeishuMFAReminderReceipt{}, errors.New("unexpected synthetic recipient")
	}
	s.calls.Add(1)
	if s.loseResponse.Load() {
		// The synthetic upstream may have accepted the message, but its receipt
		// was lost. Neither this request nor the next may repeat the send.
		return FeishuMFAReminderReceipt{}, context.DeadlineExceeded
	}
	return FeishuMFAReminderReceipt{MessageID: "synthetic-receipt"}, nil
}

func TestMFAReminderHTTPMockReceiptAndAuditPostgres(t *testing.T) {
	s, _, token := reminderLockFixture(t)
	sender := &reminderProtocolSender{}
	s.MFAEvidence = reminderProtocolEvidence{}
	s.MFAReminders = &MFAReminderService{Sender: sender}
	policy := &rbac.V3Policy{Store: s.Store}
	api := httpapi.New(s.Store, nil, true)
	s.Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}}, policy, s.Store.Session,
		func(ctx context.Context, u core.User, m, a, e string) error {
			return policy.Authorize(ctx, u, rbac.V3Decision{Module: m, Action: a, EnvironmentID: e})
		}); err != nil {
		t.Fatal(err)
	}
	request := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/members/mfa-reminders", nil)
		r.Header.Set("X-AIOps-Environment", "dev")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	for i, wantSent := range []int{1, 0} {
		w := request()
		var response struct {
			Sent int `json:"sent"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != 200 || response.Sent != wantSent {
			t.Fatalf("request %d: status=%d body=%s err=%v", i, w.Code, w.Body.String(), err)
		}
	}
	var attempts, audits int
	if err := s.Store.DB.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM v3_mfa_reminder_attempts WHERE status='accepted' AND target_id='target' AND feishu_message_id='synthetic-receipt'),(SELECT count(*) FROM management_audit WHERE action='v3.member.mfa_reminder.feishu_accepted')`).Scan(&attempts, &audits); err != nil || attempts != 1 || audits != 1 || sender.calls.Load() != 1 {
		t.Fatalf("receipt/audit/sender mismatch: attempts=%d audits=%d sends=%d err=%v", attempts, audits, sender.calls.Load(), err)
	}
	// After the real 24-hour suppression period would elapse, a lost response
	// must leave an uncertain attempt that blocks every later automatic retry.
	// This is synthetic history, not a 24-hour continuous-run claim.
	if _, err := s.Store.DB.Exec(t.Context(), `UPDATE v3_mfa_reminder_attempts SET created_at=clock_timestamp()-interval '25 hours' WHERE target_id='target'`); err != nil {
		t.Fatal(err)
	}
	sender.loseResponse.Store(true)
	for range 2 {
		if w := request(); w.Code != 503 || sender.calls.Load() != 2 {
			t.Fatalf("unknown delivery retried: status=%d sends=%d body=%s", w.Code, sender.calls.Load(), w.Body.String())
		}
	}
	var uncertain int
	if err := s.Store.DB.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM v3_mfa_reminder_attempts WHERE status='uncertain'),(SELECT count(*) FROM management_audit WHERE action='v3.member.mfa_reminder.feishu_accepted')`).Scan(&uncertain, &audits); err != nil || uncertain != 1 || audits != 1 {
		t.Fatalf("unknown delivery ledger/audit mismatch: uncertain=%d accepted audits=%d err=%v", uncertain, audits, err)
	}
	if _, err := s.Store.DB.Exec(t.Context(), `DELETE FROM v3_role_grants WHERE role_id='admin' AND module='members' AND action='manage'`); err != nil {
		t.Fatal(err)
	}
	if w := request(); w.Code != 403 || sender.calls.Load() != 2 {
		t.Fatalf("revoked manager could send: status=%d sends=%d body=%s", w.Code, sender.calls.Load(), w.Body.String())
	}
}
