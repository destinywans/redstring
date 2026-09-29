package v3admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

func TestRoleMatrixWriteHTTPAuthorizationPersistenceAndAudit(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops one random isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_role_write_test_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := adminDB.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		adminDB.Close()
		t.Fatal(err)
	}
	var db *pgxpool.Pool
	t.Cleanup(func() {
		if db != nil {
			db.Close()
		}
		if _, err := adminDB.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
		adminDB.Close()
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	cfg.ConnConfig.RuntimeParams["application_name"] = schema
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-admin-password"); err != nil {
		t.Fatal(err)
	}
	scopedManager, err := st.CreateUser(ctx, core.User{Email: "scoped@example.invalid", Name: "Scoped Manager", Role: "viewer"}, "isolated-scoped-password")
	if err != nil {
		t.Fatal(err)
	}
	globalManager, err := st.CreateUser(ctx, core.User{Email: "global@example.invalid", Name: "Global Manager", Role: "viewer"}, "isolated-global-password")
	if err != nil {
		t.Fatal(err)
	}
	devGlobalManager, err := st.CreateUser(ctx, core.User{Email: "dev-global@example.invalid", Name: "Dev Global Manager", Role: "viewer"}, "isolated-dev-global-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO rbac_roles(id,name,v3_kind) VALUES
 ('ops-role','Operations','business'),('concurrent-role','Concurrent','business'),('machine-role','Machine','machine'),
 ('execute-role','Executor','business'),('mixed-role','Mixed','business'),
	 ('scoped-manager','Scoped Manager','business'),('global-manager','Global Manager','business'),
	 ('dev-global-manager','Dev Global Manager','business')`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('ops-role','events','view','global','*'),
 ('execute-role','proposals','execute','global','*'),
 ('mixed-role','events','view','environment','dev'),
 ('mixed-role','events','change','environment','test'),
 ('scoped-manager','environment','view','environment','dev'),
	 ('scoped-manager','roles','manage','environment','dev'),
	 ('global-manager','environment','view','global','*'),
	 ('global-manager','roles','manage','global','*'),
	 ('dev-global-manager','environment','view','environment','dev'),
	 ('dev-global-manager','roles','manage','global','*')`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,'scoped-manager')`, scopedManager.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,'global-manager')`, globalManager.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,'dev-global-manager')`, devGlobalManager.ID); err != nil {
		t.Fatal(err)
	}
	admin, adminToken, err := st.Login(ctx, "admin@example.invalid", "isolated-admin-password")
	if err != nil {
		t.Fatal(err)
	}
	_, scopedToken, err := st.Login(ctx, "scoped@example.invalid", "isolated-scoped-password")
	if err != nil {
		t.Fatal(err)
	}
	_, globalToken, err := st.Login(ctx, "global@example.invalid", "isolated-global-password")
	if err != nil {
		t.Fatal(err)
	}
	_, devGlobalToken, err := st.Login(ctx, "dev-global@example.invalid", "isolated-dev-global-password")
	if err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	New(st).Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{
		Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "test"},
	}, policy, st.Session, func(ctx context.Context, u core.User, module, action, environment string) error {
		return policy.Authorize(ctx, u, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
	}); err != nil {
		t.Fatal(err)
	}
	request := func(method, roleID, environment, token string, matrix []matrixRow, version ...string) *httptest.ResponseRecorder {
		var body []byte
		if method == http.MethodPut {
			encoded, marshalErr := json.Marshal(matrixUpdate{Matrix: matrix})
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			body = encoded
		}
		r := httptest.NewRequest(method, "/api/v1/roles/"+roleID, bytes.NewReader(body))
		r.Header.Set("X-AIOps-Environment", environment)
		r.Header.Set("Accept-Language", "en-US")
		if method == http.MethodPut {
			if len(version) == 0 {
				var revision int64
				if err := db.QueryRow(ctx, `SELECT v3_matrix_revision FROM rbac_roles WHERE id=$1`, roleID).Scan(&revision); err != nil {
					t.Fatal(err)
				}
				r.Header.Set("If-Role-Matrix-Revision", strconv.FormatInt(revision, 10))
			} else if version[0] != "" {
				r.Header.Set("If-Role-Matrix-Revision", version[0])
			}
		}
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	assertError := func(w *httptest.ResponseRecorder, status int, code string) {
		t.Helper()
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != status || body.Error.Code != code {
			t.Fatalf("status=%d body=%s; want %d %s", w.Code, w.Body.String(), status, code)
		}
	}
	read := func(id, environment, token string) roleDetail {
		t.Helper()
		w := request(http.MethodGet, id, environment, token, nil)
		if w.Code != 200 {
			t.Fatalf("GET %s status=%d body=%s", id, w.Code, w.Body.String())
		}
		var detail roleDetail
		if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
			t.Fatal(err)
		}
		return detail
	}
	assertWriteAck := func(w *httptest.ResponseRecorder) {
		t.Helper()
		var ack map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &ack); err != nil || w.Code != 200 || len(ack) != 1 || string(ack["ok"]) != "true" {
			t.Fatalf("expected minimal write acknowledgement; status=%d body=%s err=%v", w.Code, w.Body.String(), err)
		}
	}

	initial := read("ops-role", "dev", adminToken)
	if initial.Scope[0] != "global" || initial.Matrix[1].Grants[0] != 1 {
		t.Fatalf("initial matrix=%+v", initial)
	}
	initialVersion := request(http.MethodGet, "ops-role", "dev", adminToken, nil).Header().Get("X-Role-Matrix-Revision")
	if initialVersion != "1" {
		t.Fatalf("GET matrix revision=%q", initialVersion)
	}
	assertError(request(http.MethodPut, "ops-role", "dev", adminToken, initial.Matrix, ""), 428, "role_revision_required")
	assertError(request(http.MethodPut, "ops-role", "dev", adminToken, initial.Matrix, "01"), 400, "invalid_precondition")
	assertError(request(http.MethodPut, "ops-role", "dev", "", initial.Matrix), 401, "unauthorized")
	assertError(request(http.MethodPut, "ops-role", "dev", scopedToken, initial.Matrix), 403, "forbidden")
	assertError(request(http.MethodPut, "ops-role", "all", adminToken, initial.Matrix), 409, "env_view_only")
	initial.Matrix[1].Grants[1] = 1
	initial.Matrix[4].Grants[3] = 1
	assertError(request(http.MethodPut, "ops-role", "dev", devGlobalToken, initial.Matrix), 403, "forbidden")
	var unauthorizedGrant bool
	if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_role_grants WHERE role_id='ops-role' AND module='events' AND action='change')`).Scan(&unauthorizedGrant); err != nil || unauthorizedGrant {
		t.Fatalf("dev-only admission changed global matrix: grant=%t err=%v", unauthorizedGrant, err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM rbac_user_roles WHERE user_id=$1 AND role_id='dev-global-manager'`, devGlobalManager.ID); err != nil {
		t.Fatal(err)
	}
	w := request(http.MethodPut, "ops-role", "dev", adminToken, initial.Matrix)
	assertWriteAck(w)
	if w.Header().Get("X-Role-Matrix-Revision") != "2" {
		t.Fatalf("PUT matrix revision=%q", w.Header().Get("X-Role-Matrix-Revision"))
	}
	saved := read("ops-role", "dev", adminToken)
	if saved.Matrix[1].Grants[1] != 1 || saved.Matrix[4].Grants[3] != 2 {
		t.Fatalf("independent GET did not show persisted passkey condition: %+v", saved.Matrix)
	}
	assertError(request(http.MethodPut, "ops-role", "dev", adminToken, initial.Matrix, initialVersion), 409, "role_changed")
	var grants, auditCount int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_role_grants WHERE role_id='ops-role' AND
 ((module='events' AND action='change' AND assurance='standard') OR
  (module='proposals' AND action='approve' AND assurance='passkey'))`).Scan(&grants); err != nil || grants != 2 {
		t.Fatalf("persisted grants=%d err=%v", grants, err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action='v3.role.matrix.updated' AND target='ops-role' AND actor_id=$1`, admin.ID).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("audit count=%d err=%v", auditCount, err)
	}
	var changes []byte
	if err := db.QueryRow(ctx, `SELECT changes FROM management_audit WHERE action='v3.role.matrix.updated' AND target='ops-role'`).Scan(&changes); err != nil {
		t.Fatal(err)
	}
	var audited struct {
		After map[string][]int `json:"after"`
	}
	if err := json.Unmarshal(changes, &audited); err != nil || len(audited.After["events"]) != 5 ||
		len(audited.After["proposals"]) != 5 || audited.After["events"][1] != 1 || audited.After["proposals"][3] != 2 {
		t.Fatalf("audit did not use persisted stable codes: %s err=%v", changes, err)
	}
	w = request(http.MethodPut, "ops-role", "dev", adminToken, saved.Matrix)
	assertWriteAck(w)
	if w.Header().Get("X-Role-Matrix-Revision") != "2" {
		t.Fatalf("no-op changed matrix revision=%q", w.Header().Get("X-Role-Matrix-Revision"))
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action='v3.role.matrix.updated' AND target='ops-role'`).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("no-op created audit: count=%d err=%v", auditCount, err)
	}
	t.Run("concurrent writers cannot overwrite the same matrix revision", func(t *testing.T) {
		first := read("concurrent-role", "dev", adminToken).Matrix
		second := read("concurrent-role", "dev", adminToken).Matrix
		first[1].Grants[0] = 1
		second[1].Grants[1] = 1
		start := make(chan struct{})
		results := make(chan *httptest.ResponseRecorder, 2)
		for _, matrix := range [][]matrixRow{first, second} {
			matrix := matrix
			go func() {
				<-start
				results <- request(http.MethodPut, "concurrent-role", "dev", adminToken, matrix, "1")
			}()
		}
		close(start)
		status := map[int]int{}
		for range 2 {
			result := <-results
			status[result.Code]++
			if result.Code == 409 {
				assertError(result, 409, "role_changed")
			}
		}
		if status[200] != 1 || status[409] != 1 {
			t.Fatalf("concurrent status counts=%v", status)
		}
		var revisions, audits int
		if err := db.QueryRow(ctx, `SELECT v3_matrix_revision FROM rbac_roles WHERE id='concurrent-role'`).Scan(&revisions); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE target='concurrent-role' AND action='v3.role.matrix.updated'`).Scan(&audits); err != nil {
			t.Fatal(err)
		}
		if revisions != 2 || audits != 1 {
			t.Fatalf("concurrent commit count revision=%d audits=%d", revisions, audits)
		}
	})
	t.Run("audit failure rolls back matrix and revision", func(t *testing.T) {
		var revisionBefore int64
		if err := db.QueryRow(ctx, `SELECT v3_matrix_revision FROM rbac_roles WHERE id='ops-role'`).Scan(&revisionBefore); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, `CREATE FUNCTION reject_v3_role_audit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.action='v3.role.matrix.updated' AND NEW.target='ops-role' THEN
  RAISE EXCEPTION 'forced audit insert failure';
 END IF;
 RETURN NEW;
END
$$`); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if _, err := db.Exec(context.Background(), `DROP FUNCTION reject_v3_role_audit()`); err != nil {
				t.Error(err)
			}
		}()
		if _, err := db.Exec(ctx, `CREATE TRIGGER reject_v3_role_audit_insert
 BEFORE INSERT ON management_audit FOR EACH ROW EXECUTE FUNCTION reject_v3_role_audit()`); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if _, err := db.Exec(context.Background(), `DROP TRIGGER reject_v3_role_audit_insert ON management_audit`); err != nil {
				t.Error(err)
			}
		}()
		candidate := read("ops-role", "dev", adminToken).Matrix
		candidate[1].Grants[2] = 1
		assertError(request(http.MethodPut, "ops-role", "dev", adminToken, candidate), 503, "admin_unavailable")
		var grantExists bool
		var revisionAfter int64
		if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_role_grants WHERE role_id='ops-role' AND module='events' AND action='delete')`).Scan(&grantExists); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(ctx, `SELECT v3_matrix_revision FROM rbac_roles WHERE id='ops-role'`).Scan(&revisionAfter); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action='v3.role.matrix.updated' AND target='ops-role'`).Scan(&auditCount); err != nil {
			t.Fatal(err)
		}
		if grantExists || revisionAfter != revisionBefore || auditCount != 1 {
			t.Fatalf("audit failure committed matrix: grant=%t revision=%d/%d audit=%d", grantExists, revisionAfter, revisionBefore, auditCount)
		}
		if got := read("ops-role", "dev", adminToken); got.Matrix[1].Grants[2] != 0 {
			t.Fatalf("GET observed rolled-back grant: %+v", got.Matrix[1])
		}
	})

	assertError(request(http.MethodGet, "execute-role", "dev", adminToken, nil), 409, "matrix_editor_required")
	assertError(request(http.MethodGet, "mixed-role", "all", adminToken, nil), 409, "matrix_editor_required")
	assertError(request(http.MethodPut, "execute-role", "dev", adminToken, saved.Matrix), 409, "matrix_editor_required")
	assertError(request(http.MethodPut, "mixed-role", "dev", adminToken, saved.Matrix), 409, "matrix_editor_required")
	machine := read("machine-role", "dev", adminToken)
	machine.Matrix[4].Grants[3] = 1
	assertError(request(http.MethodPut, "machine-role", "dev", adminToken, machine.Matrix), 422, "machine_approval_forbidden")
	var machineGrants int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_role_grants WHERE role_id='machine-role'`).Scan(&machineGrants); err != nil || machineGrants != 0 {
		t.Fatalf("machine approval persisted: count=%d err=%v", machineGrants, err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id,assurance)
 VALUES('machine-role','proposals','approve','global','*','passkey')`); err != nil {
		t.Fatal(err)
	}
	assertError(request(http.MethodGet, "machine-role", "dev", adminToken, nil), 503, "role_grants_invalid")
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='machine-role' AND module='proposals' AND action='approve'`); err != nil {
		t.Fatal(err)
	}
	t.Run("queued matrix write cannot outlive legacy membership revocation", func(t *testing.T) {
		legacyRole, err := (&rbac.Service{Store: st}).Role(ctx, "global-manager")
		if err != nil {
			t.Fatal(err)
		}
		candidate := read("ops-role", "dev", adminToken).Matrix
		candidate[1].Grants[2] = 1
		blocker, err := db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer blocker.Rollback(context.Background())
		if _, err := blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(752018992)`); err != nil {
			t.Fatal(err)
		}
		operationCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		legacyResult := make(chan error, 1)
		go func() {
			_, err := (&rbac.Service{Store: st}).SaveAssociations(
				store.WithPrincipal(operationCtx, admin), "global-manager",
				rbac.Associations{Revision: legacyRole.Revision, Users: rbac.Delta{Remove: []string{globalManager.ID}}},
			)
			legacyResult <- err
		}()
		waitForQueuedWriters := func(want int) {
			t.Helper()
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				var waiting int
				err := adminDB.QueryRow(operationCtx, `SELECT count(*) FROM pg_locks l
 JOIN pg_stat_activity a ON a.pid=l.pid
 WHERE l.locktype='advisory' AND NOT l.granted AND l.objid::bigint=$1
 AND l.objsubid=1 AND a.application_name=$2`, int64(752018992), schema).Scan(&waiting)
				if err != nil {
					t.Fatal(err)
				}
				if waiting >= want {
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Fatalf("expected %d queued identity writers on advisory lock 992", want)
		}
		waitForQueuedWriters(1)
		writeResult := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			writeResult <- request(http.MethodPut, "ops-role", "dev", globalToken, candidate)
		}()
		// Both requests have reached their write transactions while the actor
		// still holds the grant. The legacy writer entered the lock queue first.
		waitForQueuedWriters(2)
		readResult := make(chan error, 1)
		go func() {
			_, _, err := New(st).getRole(operationCtx, "ops-role", globalManager, globalToken, []string{"dev"}, true)
			readResult <- err
		}()
		waitForQueuedWriters(3)
		if err := blocker.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-legacyResult:
			if err != nil {
				t.Fatalf("legacy association removal: %v", err)
			}
		case <-operationCtx.Done():
			t.Fatal("legacy association removal timed out")
		}
		select {
		case result := <-writeResult:
			assertError(result, 403, "forbidden")
		case <-operationCtx.Done():
			t.Fatal("queued matrix write timed out")
		}
		select {
		case err := <-readResult:
			if !errors.Is(err, store.ErrForbidden) {
				t.Fatalf("queued role detail read after revocation: %v", err)
			}
		case <-operationCtx.Done():
			t.Fatal("queued role detail read timed out")
		}
		var deletedGrant, targetAudits int
		if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_role_grants WHERE role_id='ops-role' AND module='events' AND action='delete'`).Scan(&deletedGrant); err != nil || deletedGrant != 0 {
			t.Fatalf("revoked actor changed matrix: grant=%d err=%v", deletedGrant, err)
		}
		if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action='v3.role.matrix.updated' AND target='ops-role'`).Scan(&targetAudits); err != nil || targetAudits != 1 {
			t.Fatalf("revoked actor wrote audit: count=%d err=%v", targetAudits, err)
		}
	})
	t.Run("queued role detail and matrix write reject logged out session", func(t *testing.T) {
		_, queuedToken, err := st.Login(ctx, "admin@example.invalid", "isolated-admin-password")
		if err != nil {
			t.Fatal(err)
		}
		baseline := read("ops-role", "dev", queuedToken)
		baseline.Matrix[1].Grants[2] = 1
		var revisionBefore int64
		if err := db.QueryRow(ctx, `SELECT v3_matrix_revision FROM rbac_roles WHERE id='ops-role'`).Scan(&revisionBefore); err != nil {
			t.Fatal(err)
		}
		blocker, err := db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer blocker.Rollback(context.Background())
		if _, err := blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(752018992)`); err != nil {
			t.Fatal(err)
		}
		readResult := make(chan *httptest.ResponseRecorder, 1)
		writeResult := make(chan *httptest.ResponseRecorder, 1)
		go func() { readResult <- request(http.MethodGet, "ops-role", "dev", queuedToken, nil) }()
		go func() {
			writeResult <- request(http.MethodPut, "ops-role", "dev", queuedToken, baseline.Matrix, strconv.FormatInt(revisionBefore, 10))
		}()
		operationCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		deadline := time.Now().Add(5 * time.Second)
		for {
			var waiting int
			err := adminDB.QueryRow(operationCtx, `SELECT count(*) FROM pg_locks l
 JOIN pg_stat_activity a ON a.pid=l.pid
 WHERE l.locktype='advisory' AND NOT l.granted AND l.objid::bigint=$1
 AND l.objsubid=1 AND a.application_name=$2`, int64(752018992), schema).Scan(&waiting)
			if err != nil {
				t.Fatal(err)
			}
			if waiting >= 2 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("expected queued GET and PUT; found %d advisory waiters", waiting)
			}
			time.Sleep(10 * time.Millisecond)
		}
		if tag, err := db.Exec(ctx, `DELETE FROM sessions WHERE token_hash=$1`, store.Hash(queuedToken)); err != nil || tag.RowsAffected() != 1 {
			t.Fatalf("revoke queued session: rows=%d err=%v", tag.RowsAffected(), err)
		}
		if err := blocker.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		for _, result := range []<-chan *httptest.ResponseRecorder{readResult, writeResult} {
			select {
			case response := <-result:
				assertError(response, 401, "unauthorized")
			case <-operationCtx.Done():
				t.Fatal("queued role request timed out")
			}
		}
		var revisionAfter, auditCount int64
		var changedGrant bool
		if err := db.QueryRow(ctx, `SELECT v3_matrix_revision FROM rbac_roles WHERE id='ops-role'`).Scan(&revisionAfter); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action='v3.role.matrix.updated' AND target='ops-role'`).Scan(&auditCount); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_role_grants WHERE role_id='ops-role' AND module='events' AND action='delete')`).Scan(&changedGrant); err != nil {
			t.Fatal(err)
		}
		if revisionAfter != revisionBefore || auditCount != 1 || changedGrant {
			t.Fatalf("logged out session wrote role: revision=%d/%d audit=%d grant=%t", revisionAfter, revisionBefore, auditCount, changedGrant)
		}
	})
	// This human can manage roles globally but lacks global environment
	// admission. Keeping only this manager must still be rejected.
	if _, err := db.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,'dev-global-manager')`, devGlobalManager.ID); err != nil {
		t.Fatal(err)
	}
	administrator := read("admin", "dev", adminToken)
	if !strings.Contains(administrator.Notes, "outside this matrix") {
		t.Fatalf("platform grants omitted without a note: %q", administrator.Notes)
	}
	administrator.Matrix[10].Grants[4] = 0
	assertError(request(http.MethodPut, "admin", "dev", adminToken, administrator.Matrix), 409, "last_role_manager")
	var stillManager bool
	if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_role_grants WHERE role_id='admin' AND module='roles' AND action='manage')`).Scan(&stillManager); err != nil || !stillManager {
		t.Fatalf("last-manager rollback failed: manager=%t err=%v", stillManager, err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM rbac_user_roles WHERE user_id=$1 AND role_id='dev-global-manager'`, devGlobalManager.ID); err != nil {
		t.Fatal(err)
	}

	// Exercise the transaction recheck directly: the route middleware cannot
	// protect a grant removed between its first check and the database write.
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='admin' AND module='roles' AND action='manage'`); err != nil {
		t.Fatal(err)
	}
	ctx = store.WithPrincipal(ctx, admin)
	if _, err := New(st).saveMatrix(ctx, "ops-role", "dev", adminToken, 1, map[string][]int{}); !errors.Is(err, store.ErrForbidden) {
		t.Fatalf("revoked manager changed matrix: %v", err)
	}
}
