package v3canvas

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3models"
)

type investigationReplyRunner struct {
	mu  sync.Mutex
	run func(context.Context, inference.Request) (string, error)
}

func (*investigationReplyRunner) Models() []inference.Model {
	return []inference.Model{
		{ID: "test-reply", Name: "Controlled Reply", Provider: "Test", ReasoningLevels: []string{"high"}},
		{ID: "staging-reply", Name: "Other Environment Reply", Provider: "Test"},
	}
}

func (f *investigationReplyRunner) Run(ctx context.Context, req inference.Request, _ func(inference.Event) error) (string, error) {
	f.mu.Lock()
	run := f.run
	f.mu.Unlock()
	return run(ctx, req)
}

func (f *investigationReplyRunner) set(run func(context.Context, inference.Request) (string, error)) {
	f.mu.Lock()
	f.run = run
	f.mu.Unlock()
}

// Every request uses the registered HTTP route and a disposable PostgreSQL
// schema. The runner is controlled locally; no paid model or tool is called.
func TestInvestigationNodeReplyRegisteredRoute(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops an isolated schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_inv_reply_" + store.ID()
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
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
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
		`INSERT INTO rbac_roles(id,name) VALUES ('reply-editor','Reply Editor'),('reply-reader','Reply Reader')`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('reply-editor','environment','view','environment','test'),
 ('reply-editor','canvas','view','application','app-one'),
 ('reply-editor','canvas','change','application','app-one'),
 ('reply-reader','environment','view','environment','test'),
 ('reply-reader','canvas','view','application','app-one')`,
		`INSERT INTO teams(id,name) VALUES ('team-one','One'),('team-two','Two')`,
		`INSERT INTO applications(id,name,key,owner_team_id) VALUES
 ('app-one','One','app-one','team-one'),('app-two','Two','app-two','team-two')`,
		`INSERT INTO application_environments(id,application_id,key,name) VALUES
 ('ae-one','app-one','test','Test'),('ae-two','app-two','test','Test')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES ('app-one','test'),('app-two','test')`,
		`INSERT INTO investigations(id,application_id,title,status,environment_id) VALUES
 ('INV-reply','app-one','Reply investigation','open','ae-one'),
 ('INV-other','app-two','Other investigation','open','ae-two')`,
		`INSERT INTO branches(id,investigation_id,parent_id,title,revision) VALUES
 ('root-reply','INV-reply',NULL,'Root',0),
 ('child-reply','INV-reply','root-reply','Child',0),
 ('sibling-reply','INV-reply','root-reply','Sibling',0),
 ('root-other','INV-other',NULL,'Other root',0)`,
		`INSERT INTO context_revisions(branch_id,revision,messages,provenance) VALUES
 ('root-reply',0,'[{"id":"ancestor","role":"user","content":"ancestor-before-fork"}]','{"kind":"initial"}'),
 ('child-reply',0,'[{"id":"ancestor","role":"user","content":"ancestor-before-fork"}]','{"kind":"fork","source_branch_id":"root-reply","source_revision":0}'),
 ('sibling-reply',0,'[{"id":"sibling","role":"user","content":"sibling-secret"}]','{"kind":"initial"}'),
 ('root-other',0,'[{"id":"other","role":"user","content":"other-app-secret"}]','{"kind":"initial"}')`,
		`UPDATE branches SET revision=1 WHERE id='root-reply'`,
		`INSERT INTO context_revisions(branch_id,revision,messages,provenance) VALUES
 ('root-reply',1,'[{"id":"later","role":"user","content":"ancestor-after-fork"}]','{"kind":"legacy_run"}')`,
		`INSERT INTO branches(id,investigation_id,parent_id,title,revision) VALUES
 ('oversized-reply','INV-reply','root-reply','Oversized',0)`,
		`INSERT INTO context_revisions(branch_id,revision,messages,provenance) VALUES
 ('oversized-reply',0,jsonb_build_array(jsonb_build_object('id','large','role','user','content',repeat('x',30000))),'{"kind":"initial"}')`,
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
	owner, ownerToken := createUser("reply-owner@example.invalid", "reply-editor")
	_, readerToken := createUser("reply-reader@example.invalid", "reply-reader")
	runner := &investigationReplyRunner{}
	api := httpapi.New(st, nil, true)
	NewWithRuntime(st, runner).Register(api)
	policy := &rbac.V3Policy{Store: st}
	if err = api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test", "staging"}},
		policy, st.Session, func(ctx context.Context, user core.User, module, action, environment string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	request := func(method, token, env, investigation, node, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1/investigations/"+investigation+"/nodes/"+node+"/messages", strings.NewReader(body))
		r.Header.Set("X-AIOps-Environment", env)
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	post := func(token, inv, node, body string) *httptest.ResponseRecorder {
		return request(http.MethodPost, token, "test", inv, node, body)
	}
	get := func(token, node string) *httptest.ResponseRecorder {
		return request(http.MethodGet, token, "test", "INV-reply", node, "")
	}
	const child = "child-reply"
	const prompt = `{"text":"child question","model":"test-reply","reasoning":"high"}`
	var called int
	runner.set(func(_ context.Context, req inference.Request) (string, error) {
		called++
		if req.BranchID != child || req.EnvironmentID != "test" || req.ModelID != "test-reply" || req.Reasoning != "high" || len(req.Tools) != 0 {
			t.Errorf("unsafe model request: %+v", req)
		}
		var content strings.Builder
		for _, message := range req.Messages {
			content.WriteString(message.Content)
		}
		if !strings.Contains(content.String(), "ancestor-before-fork") || !strings.Contains(content.String(), "child question") ||
			strings.Contains(content.String(), "ancestor-after-fork") || strings.Contains(content.String(), "sibling-secret") ||
			strings.Contains(content.String(), "other-app-secret") {
			t.Errorf("model context broke frozen branch isolation: %#v", req.Messages)
		}
		return "exact controlled model reply", nil
	})
	for _, tc := range []struct {
		name, token, inv, node, body string
		want                         int
	}{
		{"missing session", "", "INV-reply", child, prompt, 401},
		{"reader cannot change", readerToken, "INV-reply", child, prompt, 404},
		{"other application", ownerToken, "INV-other", "root-other", prompt, 404},
		{"wrong investigation", ownerToken, "INV-other", child, prompt, 404},
		{"missing branch", ownerToken, "INV-reply", "missing", prompt, 404},
		{"missing text", ownerToken, "INV-reply", child, `{"model":"test-reply"}`, 422},
		{"unknown model", ownerToken, "INV-reply", child, `{"text":"question","model":"missing"}`, 422},
		{"unsupported reasoning", ownerToken, "INV-reply", child, `{"text":"question","model":"test-reply","reasoning":"low"}`, 422},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := post(tc.token, tc.inv, tc.node, tc.body)
			if w.Code != tc.want {
				t.Fatalf("status=%d body=%s want=%d", w.Code, w.Body.String(), tc.want)
			}
		})
	}
	if called != 0 {
		t.Fatalf("denied/invalid requests called model %d times", called)
	}
	if large := post(ownerToken, "INV-reply", "oversized-reply", prompt); large.Code != 413 {
		t.Fatalf("oversized persisted context status=%d body=%s", large.Code, large.Body.String())
	}
	var oversizedAttempts int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM v3_investigation_node_replies WHERE branch_id='oversized-reply'`).Scan(&oversizedAttempts); err != nil || oversizedAttempts != 0 || called != 0 {
		t.Fatalf("oversized context started attempt=%d calls=%d err=%v", oversizedAttempts, called, err)
	}
	w := post(ownerToken, "INV-reply", child, prompt)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"summary":"exact controlled model reply"`) || strings.Contains(w.Body.String(), "new_branch") {
		t.Fatalf("success status=%d body=%s", w.Code, w.Body.String())
	}
	if called != 1 {
		t.Fatalf("model calls=%d want=1", called)
	}
	var revision int64
	var messages []byte
	if err = db.QueryRow(ctx, `SELECT b.revision,r.messages FROM branches b JOIN context_revisions r
 ON r.branch_id=b.id AND r.revision=b.revision WHERE b.id=$1`, child).Scan(&revision, &messages); err != nil {
		t.Fatal(err)
	}
	if revision != 1 {
		t.Fatalf("committed child revision=%d", revision)
	}
	var committed []core.Message
	if err = json.Unmarshal(messages, &committed); err != nil || len(committed) != 3 ||
		committed[0].Content != "ancestor-before-fork" || committed[1].Role != "user" || committed[1].Content != "child question" ||
		committed[2].Role != "assistant" || committed[2].Content != "exact controlled model reply" {
		t.Fatalf("committed child messages=%s err=%v", messages, err)
	}
	if read := get(ownerToken, child); read.Code != 200 || !strings.Contains(read.Body.String(), "exact controlled model reply") || strings.Contains(read.Body.String(), "sibling-secret") {
		t.Fatalf("refresh status=%d body=%s", read.Code, read.Body.String())
	}
	var state, assistant string
	var base, result int64
	if err = db.QueryRow(ctx, `SELECT state,COALESCE(assistant_text,''),base_revision,result_revision
 FROM v3_investigation_node_replies WHERE branch_id=$1 ORDER BY created_at DESC LIMIT 1`, child).Scan(&state, &assistant, &base, &result); err != nil ||
		state != "completed" || assistant != "exact controlled model reply" || base != 0 || result != 1 {
		t.Fatalf("reply state=%q assistant=%q base=%d result=%d err=%v", state, assistant, base, result, err)
	}
	var auditCount int
	var auditLeak bool
	if err = db.QueryRow(ctx, `SELECT count(*),COALESCE(bool_or(changes::text LIKE '%child question%' OR changes::text LIKE '%exact controlled model reply%'),false)
 FROM management_audit WHERE action LIKE 'v3.investigation_canvas.reply_%' AND target=$1`, child).Scan(&auditCount, &auditLeak); err != nil || auditCount < 2 || auditLeak {
		t.Fatalf("reply audit count=%d leaked=%v err=%v", auditCount, auditLeak, err)
	}
	// A provider error records a failed attempt while keeping the last good
	// branch context intact. The next prompt must not include the failed text.
	runner.set(func(_ context.Context, _ inference.Request) (string, error) {
		return "", errors.New("controlled provider failure")
	})
	w = post(ownerToken, "INV-reply", child, `{"text":"failed prompt secret","model":"test-reply"}`)
	if w.Code != 503 {
		t.Fatalf("failed model status=%d body=%s", w.Code, w.Body.String())
	}
	if err = db.QueryRow(ctx, `SELECT revision FROM branches WHERE id=$1`, child).Scan(&revision); err != nil || revision != 1 {
		t.Fatalf("failed model changed revision=%d err=%v", revision, err)
	}
	if read := get(ownerToken, child); read.Code != 200 || !strings.Contains(read.Body.String(), `"state":"failed"`) || strings.Contains(read.Body.String(), "controlled provider failure") {
		t.Fatalf("failed attempt read status=%d body=%s", read.Code, read.Body.String())
	}
	runner.set(func(_ context.Context, req inference.Request) (string, error) {
		for _, message := range req.Messages {
			if strings.Contains(message.Content, "failed prompt secret") {
				t.Error("failed prompt entered model context")
			}
		}
		return "recovered reply", nil
	})
	w = post(ownerToken, "INV-reply", child, `{"text":"retry question","model":"test-reply"}`)
	if w.Code != 200 {
		t.Fatalf("recovery status=%d body=%s", w.Code, w.Body.String())
	}
	if err = db.QueryRow(ctx, `SELECT revision FROM branches WHERE id=$1`, child).Scan(&revision); err != nil || revision != 2 {
		t.Fatalf("recovery revision=%d err=%v", revision, err)
	}
	// A running model owns one branch slot. The losing HTTP request must not
	// invoke the runner or append another context revision.
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	runner.set(func(ctx context.Context, _ inference.Request) (string, error) {
		once.Do(func() { close(entered) })
		select {
		case <-release:
			return "first concurrent reply", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})
	first := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		first <- post(ownerToken, "INV-reply", child, `{"text":"first concurrent","model":"test-reply"}`)
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first model did not start")
	}
	if running := get(ownerToken, child); running.Code != 200 || !strings.Contains(running.Body.String(), `"state":"running"`) ||
		!strings.Contains(running.Body.String(), "first concurrent") {
		t.Fatalf("running attempt invisible: status=%d body=%s", running.Code, running.Body.String())
	}
	if competing := post(ownerToken, "INV-reply", child, `{"text":"second concurrent","model":"test-reply"}`); competing.Code != 409 {
		t.Fatalf("same-node concurrent status=%d body=%s", competing.Code, competing.Body.String())
	}
	close(release)
	select {
	case completed := <-first:
		if completed.Code != 200 {
			t.Fatalf("first concurrent status=%d body=%s", completed.Code, completed.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first concurrent reply did not complete")
	}
	if err = db.QueryRow(ctx, `SELECT revision FROM branches WHERE id=$1`, child).Scan(&revision); err != nil || revision != 3 {
		t.Fatalf("concurrent revision=%d err=%v", revision, err)
	}
	// A grant revoked after generation starts invalidates completion. The
	// returned text may never become an assistant message.
	started, finish := make(chan struct{}), make(chan struct{})
	runner.set(func(ctx context.Context, _ inference.Request) (string, error) {
		close(started)
		select {
		case <-finish:
			return "answer after revocation", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})
	active := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		active <- post(ownerToken, "INV-reply", child, `{"text":"revoked prompt","model":"test-reply"}`)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("revocation model did not start")
	}
	if _, err = db.Exec(ctx, `DELETE FROM rbac_user_roles WHERE user_id=$1 AND role_id='reply-editor'`, owner.ID); err != nil {
		t.Fatal(err)
	}
	close(finish)
	select {
	case denied := <-active:
		if denied.Code == 200 {
			t.Fatalf("revoked reply accepted: %s", denied.Body.String())
		}
	case <-time.After(8 * time.Second):
		t.Fatal("revoked reply did not finish")
	}
	if err = db.QueryRow(ctx, `SELECT revision FROM branches WHERE id=$1`, child).Scan(&revision); err != nil || revision != 3 {
		t.Fatalf("revocation changed revision=%d err=%v", revision, err)
	}
	var leaked int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM context_revisions WHERE branch_id=$1 AND messages::text LIKE '%answer after revocation%'`, child).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("revoked answer persisted=%d err=%v", leaked, err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,'reply-editor')`, owner.ID); err != nil {
		t.Fatal(err)
	}
	// An existing writer can advance the branch while inference is running.
	// Completion must compare the captured base revision and reject the stale
	// answer, even though the model itself returned successfully.
	baseStarted, baseFinish := make(chan struct{}), make(chan struct{})
	runner.set(func(ctx context.Context, _ inference.Request) (string, error) {
		close(baseStarted)
		select {
		case <-baseFinish:
			return "answer for stale revision", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})
	stale := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		stale <- post(ownerToken, "INV-reply", child, `{"text":"stale version prompt","model":"test-reply"}`)
	}()
	select {
	case <-baseStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("version-race model did not start")
	}
	if _, err = db.Exec(ctx, `INSERT INTO context_revisions(branch_id,revision,messages,provenance)
 SELECT branch_id,4,messages,'{"kind":"external_change"}'::jsonb FROM context_revisions
 WHERE branch_id=$1 AND revision=3`, child); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `UPDATE branches SET revision=4 WHERE id=$1`, child); err != nil {
		t.Fatal(err)
	}
	close(baseFinish)
	select {
	case conflict := <-stale:
		if conflict.Code != 409 || !strings.Contains(conflict.Body.String(), `"code":"context_version_conflict"`) {
			t.Fatalf("stale answer status=%d body=%s", conflict.Code, conflict.Body.String())
		}
	case <-time.After(8 * time.Second):
		t.Fatal("version-race reply did not finish")
	}
	if err = db.QueryRow(ctx, `SELECT revision FROM branches WHERE id=$1`, child).Scan(&revision); err != nil || revision != 4 {
		t.Fatalf("version conflict changed revision=%d err=%v", revision, err)
	}
	if err = db.QueryRow(ctx, `SELECT count(*) FROM context_revisions WHERE branch_id=$1 AND messages::text LIKE '%answer for stale revision%'`, child).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("stale answer persisted=%d err=%v", leaked, err)
	}
	// The configured OpenAI-compatible adapter also reaches this registered
	// route. Its only endpoint is the local test server; streamed text must be
	// the exact text committed in the branch revision.
	var providerCalls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls.Add(1)
		if r.URL.Path != "/chat/completions" || r.Method != http.MethodPost {
			t.Errorf("unexpected provider request %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
			t.Errorf("provider input: %v", e)
		}
		encoded, _ := json.Marshal(body)
		if strings.Contains(string(encoded), `"tools"`) || !strings.Contains(string(encoded), "ancestor-after-fork") ||
			strings.Contains(string(encoded), "sibling-secret") {
			t.Errorf("provider received unsafe context: %s", encoded)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"adapter \"},\"finish_reason\":null}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer provider.Close()
	adapter, err := inference.New(inference.Config{Providers: []inference.Provider{{ID: "adapter-test", Kind: "openai", Model: "mock-model", BaseURL: provider.URL}},
		HTTPClient: provider.Client(), Timeout: 10 * time.Second, MaxCalls: 1})
	if err != nil {
		t.Fatal(err)
	}
	adapterAPI := httpapi.New(st, nil, true)
	NewWithRuntime(st, adapter).Register(adapterAPI)
	if err = adapterAPI.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test"}},
		policy, st.Session, func(ctx context.Context, user core.User, module, action, environment string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	adapterRequest := httptest.NewRequest(http.MethodPost, "/api/v1/investigations/INV-reply/nodes/root-reply/messages",
		strings.NewReader(`{"text":"adapter prompt","model":"adapter-test"}`))
	adapterRequest.Header.Set("X-AIOps-Environment", "test")
	adapterRequest.Header.Set("Content-Type", "application/json")
	adapterRequest.AddCookie(&http.Cookie{Name: "aiops_session", Value: ownerToken})
	adapterResponse := httptest.NewRecorder()
	adapterAPI.ServeHTTP(adapterResponse, adapterRequest)
	if adapterResponse.Code != 200 || !strings.Contains(adapterResponse.Body.String(), `"summary":"adapter answer"`) || providerCalls.Load() != 1 {
		t.Fatalf("adapter reply status=%d body=%s calls=%d", adapterResponse.Code, adapterResponse.Body.String(), providerCalls.Load())
	}
	var adapterRevision int64
	var adapterContext []byte
	if err = db.QueryRow(ctx, `SELECT b.revision,r.messages FROM branches b JOIN context_revisions r
 ON r.branch_id=b.id AND r.revision=b.revision WHERE b.id='root-reply'`).Scan(&adapterRevision, &adapterContext); err != nil ||
		adapterRevision != 2 || !strings.Contains(string(adapterContext), "adapter answer") {
		t.Fatalf("adapter not committed: revision=%d context=%s err=%v", adapterRevision, adapterContext, err)
	}
	bindingsPath := filepath.Join(t.TempDir(), "models.json")
	bindingsJSON := `{"tier":"nonprod","environments":[{"id":"test","models":["test-reply"],"default_model":"test-reply"},{"id":"staging","models":["staging-reply"],"default_model":"staging-reply"}]}`
	if err = os.WriteFile(bindingsPath, []byte(bindingsJSON), 0600); err != nil {
		t.Fatal(err)
	}
	bindings, err := v3models.LoadBindings(bindingsPath, "nonprod", []string{"test", "staging"}, runner)
	if err != nil {
		t.Fatal(err)
	}
	boundAPI := httpapi.New(st, nil, true)
	NewWithRuntime(st, v3models.NewBoundRunner(runner, bindings)).Register(boundAPI)
	if err = boundAPI.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test", "staging"}},
		policy, st.Session, func(ctx context.Context, user core.User, module, action, environment string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	boundPost := func(token, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/investigations/INV-reply/nodes/root-reply/messages", strings.NewReader(body))
		r.Header.Set("X-AIOps-Environment", "test")
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		boundAPI.ServeHTTP(w, r)
		return w
	}
	boundCalls := 0
	runner.set(func(_ context.Context, req inference.Request) (string, error) {
		boundCalls++
		if req.EnvironmentID != "test" || req.ModelID != "test-reply" {
			t.Errorf("reply did not use persisted investigation environment binding: %+v", req)
		}
		return "bound environment answer", nil
	})
	if denied := boundPost(ownerToken, `{"text":"cross environment","model":"staging-reply"}`); denied.Code != 422 || boundCalls != 0 {
		t.Fatalf("cross-environment model status=%d body=%s calls=%d", denied.Code, denied.Body.String(), boundCalls)
	}
	if accepted := boundPost(ownerToken, `{"text":"bound default"}`); accepted.Code != 200 || boundCalls != 1 {
		t.Fatalf("bound default status=%d body=%s calls=%d", accepted.Code, accepted.Body.String(), boundCalls)
	}
	// Revoking the exact Cookie while the local runner is blocked must prevent
	// its answer from becoming a branch revision.
	_, runToken, err := st.Login(ctx, owner.Email, "isolated-user-password")
	if err != nil {
		t.Fatal(err)
	}
	runStarted, runFinish := make(chan struct{}), make(chan struct{})
	runner.set(func(runCtx context.Context, _ inference.Request) (string, error) {
		close(runStarted)
		select {
		case <-runFinish:
			return "logged out answer", nil
		case <-runCtx.Done():
			return "", runCtx.Err()
		}
	})
	loggedOut := make(chan *httptest.ResponseRecorder, 1)
	go func() { loggedOut <- boundPost(runToken, `{"text":"logout while generating"}`) }()
	select {
	case <-runStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("bound model did not start")
	}
	if err = st.Logout(ctx, runToken); err != nil {
		t.Fatal(err)
	}
	close(runFinish)
	select {
	case denied := <-loggedOut:
		if denied.Code == 200 {
			t.Fatalf("logged-out model reply persisted: %s", denied.Body.String())
		}
	case <-time.After(8 * time.Second):
		t.Fatal("logged-out model reply did not finish")
	}
	if err = db.QueryRow(ctx, `SELECT count(*) FROM context_revisions WHERE branch_id='root-reply' AND messages::text LIKE '%logged out answer%'`).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("logged-out answer persisted=%d err=%v", leaked, err)
	}
	if _, err = db.Exec(ctx, `UPDATE investigations SET status='closed' WHERE id='INV-reply'`); err != nil {
		t.Fatal(err)
	}
	runner.set(func(_ context.Context, _ inference.Request) (string, error) {
		t.Error("closed investigation invoked model")
		return "unreachable", nil
	})
	if closed := post(ownerToken, "INV-reply", child, prompt); closed.Code != 409 {
		t.Fatalf("closed investigation status=%d body=%s", closed.Code, closed.Body.String())
	}
}
