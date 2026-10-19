package v3canvas

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestInvestigationWritebackRegisteredRoutes(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops an isolated schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_writeback_" + store.ID()
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
		if _, e := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); e != nil {
			t.Error(e)
		}
		admin.Close()
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err = st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = st.Bootstrap(ctx, "admin@example.invalid", "isolated-admin-password"); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`DELETE FROM v3_role_grants WHERE role_id='viewer'`,
		`INSERT INTO rbac_roles(id,name) VALUES ('wb-view','Writeback View'),('wb-change','Writeback Change')`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('wb-view','environment','view','environment','test'),('wb-view','canvas','view','application','app-wb'),
 ('wb-change','environment','view','environment','test'),('wb-change','canvas','view','application','app-wb'),
 ('wb-change','canvas','change','application','app-wb')`,
		`INSERT INTO teams(id,name) VALUES ('team-wb','Team')`,
		`INSERT INTO applications(id,name,key,owner_team_id) VALUES
 ('app-wb','Writeback App','app-wb','team-wb'),('app-other','Other App','app-other','team-wb')`,
		`INSERT INTO application_environments(id,application_id,key,name) VALUES
 ('ae-wb','app-wb','test','Test'),('ae-other','app-other','test','Test')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES ('app-wb','test'),('app-other','test')`,
		`INSERT INTO investigations(id,application_id,title,status,environment_id) VALUES
 ('INV-wb','app-wb','Writeback investigation','open','ae-wb'),
 ('INV-other','app-other','Other investigation','open','ae-other')`,
		`INSERT INTO branches(id,investigation_id,parent_id,title,revision) VALUES
 ('wb-root','INV-wb',NULL,'Root',0),('wb-child','INV-wb','wb-root','Child',1),
 ('wb-grand','INV-wb','wb-child','Grandchild',1),('wb-sibling','INV-wb','wb-root','Sibling',0),
 ('other-root','INV-other',NULL,'Other root',0)`,
		`INSERT INTO context_revisions(branch_id,revision,messages,provenance) VALUES
 ('wb-root',0,'[{"id":"u0","role":"user","content":"original"},{"id":"shared","role":"assistant","content":"shared"}]','{"kind":"initial"}'),
 ('wb-child',0,'[{"id":"u0","role":"user","content":"original"},{"id":"shared","role":"assistant","content":"shared"}]','{"kind":"fork"}'),
 ('wb-child',1,'[{"id":"u0","role":"user","content":"original"},{"id":"shared","role":"assistant","content":"shared"},{"id":"child-answer","role":"assistant","content":"saved child answer"}]','{"kind":"model_run"}'),
 ('wb-grand',0,'[{"id":"u0","role":"user","content":"original"},{"id":"shared","role":"assistant","content":"shared"},{"id":"child-answer","role":"assistant","content":"saved child answer"}]','{"kind":"fork"}'),
 ('wb-grand',1,'[{"id":"u0","role":"user","content":"original"},{"id":"shared","role":"assistant","content":"shared"},{"id":"child-answer","role":"assistant","content":"saved child answer"},{"id":"grand-answer","role":"assistant","content":"saved grandchild answer","evidence_ids":["ev-wb"]},{"id":"grand-late-answer","role":"assistant","content":"another saved conclusion"}]','{"kind":"model_run"}'),
 ('wb-sibling',0,'[]','{"kind":"initial"}'),('other-root',0,'[]','{"kind":"initial"}')`,
		`INSERT INTO evidence(id,branch_id,kind,title,content,source) VALUES
		 ('ev-wb','wb-grand','log','Saved observation','exact persisted bytes','read-only probe'),
		 ('ev-sibling','wb-sibling','log','Sibling observation','private sibling evidence','read-only probe')`,
	} {
		if _, err = db.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	createUser := func(email, role string) (core.User, string) {
		t.Helper()
		u, e := st.CreateUser(ctx, core.User{Email: email, Name: email, Role: "viewer"}, "isolated-user-password")
		if e != nil {
			t.Fatal(e)
		}
		if _, e = db.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,$2)`, u.ID, role); e != nil {
			t.Fatal(e)
		}
		_, token, e := st.Login(ctx, email, "isolated-user-password")
		if e != nil {
			t.Fatal(e)
		}
		return u, token
	}
	owner, ownerToken := createUser("owner@example.invalid", "wb-change")
	_, viewerToken := createUser("viewer@example.invalid", "wb-view")
	noGrantUser, noGrantToken := createUser("no-grant@example.invalid", "wb-view")
	if _, err = db.Exec(ctx, `DELETE FROM rbac_user_roles WHERE user_id=$1 AND role_id='wb-view'`, noGrantUser.ID); err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	New(st).Register(api)
	if err = api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test"}},
		policy, st.Session, func(ctx context.Context, u core.User, module, action, environment string) error {
			return policy.Authorize(ctx, u, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	request := func(method, token, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("X-AIOps-Environment", "test")
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	preview := func(token, suffix string) *httptest.ResponseRecorder {
		return request(http.MethodGet, token, "/api/v1/investigations/INV-wb/writeback-preview?"+suffix, "")
	}
	post := func(token, body string) *httptest.ResponseRecorder {
		return request(http.MethodPost, token, "/api/v1/investigations/INV-wb/writeback", body)
	}
	assertStatus := func(w *httptest.ResponseRecorder, status int) {
		t.Helper()
		if w.Code != status {
			t.Fatalf("status=%d, want=%d, body=%s", w.Code, status, w.Body.String())
		}
	}

	w := preview(viewerToken, "from=wb-grand")
	assertStatus(w, 200)
	var data struct {
		From struct{ ID, Version string } `json:"from"`
		To   struct {
			ID, Version, NextVersion string
			Hops                     int
		} `json:"to"`
		SourceRevision int64 `json:"source_revision"`
		TargetRevision int64 `json:"target_revision"`
		Conclusions    []struct {
			ID, Text    string
			Recommended bool
			Evidence    []string
		} `json:"conclusions"`
		Budget map[string]json.RawMessage `json:"budget"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if data.From.ID != "wb-grand" || data.To.ID != "wb-root" || data.To.Hops != 2 ||
		data.SourceRevision != 1 || data.TargetRevision != 0 || len(data.Conclusions) != 3 ||
		data.Conclusions[0].ID != "child-answer" || data.Conclusions[1].ID != "grand-answer" ||
		data.Conclusions[2].ID != "grand-late-answer" ||
		data.Conclusions[1].Recommended || string(data.Budget["before"]) != "null" {
		t.Fatalf("preview invented or omitted persisted facts: %s", w.Body.String())
	}
	// PA-62: the checks follow the request language (Chinese by default).
	if !strings.Contains(w.Body.String(), "请人工核对所选来源文字和证据") || strings.Contains(w.Body.String(), "semantic conflicts") {
		t.Fatalf("preview checks are not in the request language: %s", w.Body.String())
	}
	assertStatus(preview(viewerToken, "node=wb-grand&target=wb-child"), 200)
	assertStatus(preview(viewerToken, "node=wb-grand&target=wb-sibling"), 422)
	assertStatus(preview(viewerToken, "node=wb-grand&target=other-root"), 422)
	assertStatus(request(http.MethodGet, viewerToken, "/api/v1/investigations/INV-other/writeback-preview?node=other-root", ""), 404)
	assertStatus(preview("", "node=wb-grand"), 401)
	assertStatus(preview(noGrantToken, "node=wb-grand"), 403)

	valid := `{"node":"wb-grand","target":"wb-root","mode":"messages","items":["child-answer"],"expected_source_revision":1,"expected_target_revision":0}`
	assertStatus(post(viewerToken, valid), 404)
	assertStatus(post(ownerToken, `{"node":"wb-grand","target":"wb-root","mode":"messages","items":["child-answer"]}`), 428)
	assertStatus(post(ownerToken, `{"node":"wb-grand","target":"wb-sibling","mode":"messages","items":["child-answer"],"expected_source_revision":1,"expected_target_revision":0}`), 422)
	assertStatus(post(ownerToken, `{"node":"other-root","target":"wb-root","mode":"messages","items":["child-answer"],"expected_source_revision":0,"expected_target_revision":0}`), 404)
	assertStatus(post(ownerToken, `{"node":"wb-grand","target":"wb-root","mode":"messages","items":["shared"],"expected_source_revision":1,"expected_target_revision":0}`), 409)
	assertStatus(post(ownerToken, `{"node":"wb-grand","target":"wb-root","mode":"messages","items":["child-answer"],"expected_source_revision":0,"expected_target_revision":0}`), 409)
	assertStatus(post(ownerToken, `{"node":"wb-grand","target":"wb-root","mode":"messages","items":["child-answer"],"expected_source_revision":1,"expected_target_revision":1}`), 409)
	assertStatus(post(ownerToken, valid), 200)
	assertStatus(post(ownerToken, valid), 409)

	var revision int64
	var raw, provenance []byte
	if err = db.QueryRow(ctx, `SELECT b.revision,r.messages,r.provenance FROM branches b
 JOIN context_revisions r ON r.branch_id=b.id AND r.revision=b.revision WHERE b.id='wb-root'`).Scan(&revision, &raw, &provenance); err != nil {
		t.Fatal(err)
	}
	if revision != 1 || !strings.Contains(string(raw), "saved child answer") ||
		strings.Contains(string(raw), "saved grandchild answer") || !strings.Contains(string(provenance), "child-answer") {
		t.Fatalf("messages mode wrote wrong revision: rev=%d raw=%s provenance=%s", revision, raw, provenance)
	}

	summary := `{"from":"wb-grand","target":"wb-root","mode":"summary","conclusions":["grand-answer"],"expected_source_revision":1,"expected_target_revision":1}`
	assertStatus(post(ownerToken, summary), 200)
	if err = db.QueryRow(ctx, `SELECT messages FROM context_revisions WHERE branch_id='wb-root' AND revision=2`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "historical_assistant_text") || !strings.Contains(string(raw), "saved grandchild answer") || strings.Contains(string(raw), `"role": "assistant", "content": "saved grandchild answer"`) {
		t.Fatalf("summary fabricated model output: %s", raw)
	}
	assertStatus(post(ownerToken, `{"node":"wb-grand","target":"wb-root","mode":"summary","items":["grand-answer"],"expected_source_revision":1,"expected_target_revision":2}`), 409)
	w = preview(viewerToken, "node=wb-grand")
	assertStatus(w, 200)
	if strings.Contains(w.Body.String(), `"id":"grand-answer"`) || strings.Contains(w.Body.String(), `"id":"child-answer"`) {
		t.Fatalf("preview offered already applied source messages: %s", w.Body.String())
	}
	evidence := `{"node":"wb-grand","target":"wb-child","mode":"evidence","items":["grand-answer"],"expected_source_revision":1,"expected_target_revision":1}`
	assertStatus(post(ownerToken, evidence), 200)
	if err = db.QueryRow(ctx, `SELECT messages FROM context_revisions WHERE branch_id='wb-child' AND revision=2`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "exact persisted bytes") || !strings.Contains(string(raw), "ev-wb") {
		t.Fatalf("evidence mode omitted snapshot: %s", raw)
	}
	assertStatus(post(ownerToken, `{"node":"wb-grand","target":"wb-child","mode":"evidence","items":["grand-answer"],"expected_source_revision":1,"expected_target_revision":2}`), 409)
	var audits, events int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action='v3.investigation_canvas.writeback'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(ctx, `SELECT count(*) FROM events WHERE type='branch.writeback'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if audits != 3 || events != 3 {
		t.Fatalf("writeback missing atomic audit/events: audits=%d events=%d", audits, events)
	}
	// The canvas shows how many writebacks each node received and from where.
	canvas := request(http.MethodGet, viewerToken, "/api/v1/investigations/INV-wb/canvas", "")
	assertStatus(canvas, 200)
	var graph struct {
		Nodes []struct {
			ID         string `json:"id"`
			Writebacks struct {
				Received int      `json:"received"`
				Sources  []string `json:"sources"`
			} `json:"writebacks"`
		} `json:"nodes"`
	}
	if err = json.Unmarshal(canvas.Body.Bytes(), &graph); err != nil {
		t.Fatal(err)
	}
	received := map[string]string{}
	for _, node := range graph.Nodes {
		received[node.ID] = fmt.Sprintf("%d:%s", node.Writebacks.Received, strings.Join(node.Writebacks.Sources, ","))
	}
	if received["wb-root"] != "2:wb-grand" || received["wb-child"] != "1:wb-grand" || received["wb-grand"] != "0:" {
		t.Fatalf("canvas writeback counts: %v", received)
	}
	if err = db.QueryRow(ctx, `SELECT revision FROM branches WHERE id='wb-grand'`).Scan(&revision); err != nil || revision != 1 {
		t.Fatalf("source changed: revision=%d err=%v", revision, err)
	}

	// Two requests sharing an expected target revision serialize on the same
	// branch row. Exactly one can commit.
	concurrent := `{"node":"wb-grand","target":"wb-root","mode":"summary","items":["grand-late-answer"],"expected_source_revision":1,"expected_target_revision":2}`
	var wg sync.WaitGroup
	results := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- post(ownerToken, concurrent).Code
		}()
	}
	wg.Wait()
	close(results)
	counts := map[int]int{}
	for status := range results {
		counts[status]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatalf("concurrent writebacks did not serialize: %v", counts)
	}
	if _, err = db.Exec(ctx, `UPDATE context_revisions
	 SET messages=jsonb_set(messages,'{4,evidence_ids}','["ev-sibling"]'::jsonb)
 WHERE branch_id='wb-grand' AND revision=1`); err != nil {
		t.Fatal(err)
	}
	assertStatus(post(ownerToken, `{"node":"wb-grand","target":"wb-child","mode":"evidence","items":["grand-late-answer"],"expected_source_revision":1,"expected_target_revision":2}`), 422)
	if _, err = db.Exec(ctx, `UPDATE context_revisions
	 SET messages=messages #- '{4,evidence_ids}'
 WHERE branch_id='wb-grand' AND revision=1`); err != nil {
		t.Fatal(err)
	}
	assertStatus(post(ownerToken, `{"node":"wb-grand","target":"wb-child","mode":"evidence","items":["grand-late-answer"],"expected_source_revision":1,"expected_target_revision":2}`), 422)
	if _, err = db.Exec(ctx, `CREATE FUNCTION reject_writeback_audit() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN IF NEW.action='v3.investigation_canvas.writeback' THEN RAISE EXCEPTION 'audit unavailable'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER reject_writeback_audit BEFORE INSERT ON management_audit
 FOR EACH ROW EXECUTE FUNCTION reject_writeback_audit()`); err != nil {
		t.Fatal(err)
	}
	assertStatus(post(ownerToken, `{"node":"wb-grand","target":"wb-child","mode":"summary","items":["grand-late-answer"],"expected_source_revision":1,"expected_target_revision":2}`), 503)
	if err = db.QueryRow(ctx, `SELECT revision FROM branches WHERE id='wb-child'`).Scan(&revision); err != nil || revision != 2 {
		t.Fatalf("audit failure did not roll back target revision: rev=%d err=%v", revision, err)
	}
	if err = db.QueryRow(ctx, `SELECT count(*) FROM events WHERE type='branch.writeback'`).Scan(&events); err != nil || events != 4 {
		t.Fatalf("audit failure leaked event: count=%d err=%v", events, err)
	}
	if _, err = db.Exec(ctx, `DROP TRIGGER reject_writeback_audit ON management_audit; DROP FUNCTION reject_writeback_audit()`); err != nil {
		t.Fatal(err)
	}
	// A legacy context revision decodes through core.Message and drops any
	// V3-only envelope fields. Immutable revision provenance must still keep
	// the previously selected assistant IDs from being offered or copied again.
	if err = db.QueryRow(ctx, `SELECT messages FROM context_revisions WHERE branch_id='wb-root' AND revision=3`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var legacyMessages []core.Message
	if err = json.Unmarshal(raw, &legacyMessages); err != nil {
		t.Fatal(err)
	}
	legacyRaw, err := json.Marshal(legacyMessages)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `UPDATE branches SET revision=4 WHERE id='wb-root'`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO context_revisions(branch_id,revision,messages,provenance)
 VALUES('wb-root',4,$1::jsonb,'{"kind":"legacy_rewrite"}'::jsonb)`, legacyRaw); err != nil {
		t.Fatal(err)
	}
	w = preview(viewerToken, "node=wb-grand")
	assertStatus(w, 200)
	if strings.Contains(w.Body.String(), `"id":"grand-answer"`) || strings.Contains(w.Body.String(), `"id":"grand-late-answer"`) {
		t.Fatalf("legacy rewrite restored applied candidates: %s", w.Body.String())
	}
	assertStatus(post(ownerToken, `{"node":"wb-grand","target":"wb-root","mode":"summary","items":["grand-answer"],"expected_source_revision":1,"expected_target_revision":4}`), 409)
	// The Cookie may expire while a request waits on the investigation or
	// branch row. Preview must not reveal the old snapshot, and writeback must
	// not commit its context, event, or audit after the wait.
	for _, tc := range []struct {
		name, role, lockSQL, method, path, body string
	}{
		{"preview", "wb-view", `SELECT id FROM investigations WHERE id='INV-wb' FOR UPDATE`, http.MethodGet,
			"/api/v1/investigations/INV-wb/writeback-preview?node=wb-grand", ""},
		{"writeback", "wb-change", `SELECT id FROM branches WHERE id='wb-grand' FOR UPDATE`, http.MethodPost,
			"/api/v1/investigations/INV-wb/writeback",
			`{"node":"wb-grand","target":"wb-child","mode":"summary","items":["grand-late-answer"],"expected_source_revision":1,"expected_target_revision":2}`},
	} {
		t.Run("expired during "+tc.name+" lock wait", func(t *testing.T) {
			_, token := createUser("expiry-"+tc.name+"@example.invalid", tc.role)
			blocker, e := db.Begin(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer blocker.Rollback(context.Background())
			var blockerPID int32
			if e = blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID); e != nil {
				t.Fatal(e)
			}
			var lockedID string
			if e = blocker.QueryRow(ctx, tc.lockSQL).Scan(&lockedID); e != nil {
				t.Fatal(e)
			}
			if _, e = db.Exec(ctx, `UPDATE sessions SET expires_at=clock_timestamp()+interval '1200 milliseconds' WHERE token_hash=$1`, store.Hash(token)); e != nil {
				t.Fatal(e)
			}
			responses := make(chan *httptest.ResponseRecorder, 1)
			go func() { responses <- request(tc.method, token, tc.path, tc.body) }()
			deadline := time.Now().Add(6 * time.Second)
			for {
				var blocked bool
				if e = db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity a
 WHERE a.pid<>pg_backend_pid() AND $1=ANY(pg_blocking_pids(a.pid)))`, blockerPID).Scan(&blocked); e != nil {
					t.Fatal(e)
				}
				if blocked {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("request did not wait on locked investigation or branch")
				}
				time.Sleep(10 * time.Millisecond)
			}
			for {
				var expired bool
				if e = db.QueryRow(ctx, `SELECT expires_at<=clock_timestamp() FROM sessions WHERE token_hash=$1`, store.Hash(token)).Scan(&expired); e != nil {
					t.Fatal(e)
				}
				if expired {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("blocked session did not expire")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if e = blocker.Rollback(ctx); e != nil {
				t.Fatal(e)
			}
			select {
			case response := <-responses:
				if response.Code != 401 {
					t.Fatalf("expired request status=%d body=%s", response.Code, response.Body.String())
				}
			case <-time.After(6 * time.Second):
				t.Fatal("blocked request did not finish")
			}
		})
	}
	if err = db.QueryRow(ctx, `SELECT revision FROM branches WHERE id='wb-child'`).Scan(&revision); err != nil || revision != 2 {
		t.Fatalf("expired writeback changed target revision=%d err=%v", revision, err)
	}
	if _, err = db.Exec(ctx, `UPDATE investigations SET status='closed' WHERE id='INV-wb'`); err != nil {
		t.Fatal(err)
	}
	assertStatus(preview(viewerToken, "node=wb-grand"), 409)
	assertStatus(post(ownerToken, `{"node":"wb-grand","target":"wb-root","mode":"summary","items":["grand-answer"],"expected_source_revision":1,"expected_target_revision":4}`), 409)
	if _, err = db.Exec(ctx, `UPDATE investigations SET status='open' WHERE id='INV-wb'`); err != nil {
		t.Fatal(err)
	}

	if _, err = db.Exec(ctx, `UPDATE sessions SET expires_at=clock_timestamp()-interval '1 second' WHERE user_id=$1`, owner.ID); err != nil {
		t.Fatal(err)
	}
	assertStatus(post(ownerToken, `{"node":"wb-grand","target":"wb-root","mode":"summary","items":["grand-answer"],"expected_source_revision":1,"expected_target_revision":4}`), 401)
	if _, err = db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='wb-view' AND module='canvas' AND action='view'`); err != nil {
		t.Fatal(err)
	}
	assertStatus(preview(viewerToken, "node=wb-grand"), 404)
}
