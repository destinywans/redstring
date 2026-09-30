package v3canvas

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

type nodeFakeRunner struct {
	run func(context.Context, inference.Request) (string, error)
}

func (*nodeFakeRunner) Models() []inference.Model {
	return []inference.Model{
		{ID: "fake", Name: "Fake", Provider: "Test", ReasoningLevels: []string{"high"}},
		{ID: "fake-second", Name: "Second Fake", Provider: "Test", ReasoningLevels: []string{"high"}},
	}
}
func (f *nodeFakeRunner) Run(ctx context.Context, req inference.Request, _ func(inference.Event) error) (string, error) {
	return f.run(ctx, req)
}

func TestGenericNodeRepliesPersistAndKeepNodeContextIsolated(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops an isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_canvas_node_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = adminDB.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		adminDB.Close()
		t.Fatal(err)
	}
	var db *pgxpool.Pool
	t.Cleanup(func() {
		if db != nil {
			db.Close()
		}
		if _, e := adminDB.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); e != nil {
			t.Error(e)
		}
		adminDB.Close()
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
		`INSERT INTO rbac_roles(id,name) VALUES ('node-editor','Node Editor')`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('node-editor','environment','view','environment','dev'),
 ('node-editor','canvas','view','environment','dev'),
 ('node-editor','canvas','change','environment','dev')`,
	} {
		if _, err = db.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	owner, err := st.CreateUser(ctx, core.User{Email: "owner@example.invalid", Name: "Owner", Role: "viewer"}, "isolated-owner-password")
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateUser(ctx, core.User{Email: "other@example.invalid", Name: "Other", Role: "viewer"}, "isolated-other-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,'node-editor'),($2,'node-editor')`, owner.ID, other.ID); err != nil {
		t.Fatal(err)
	}
	_, ownerToken, err := st.Login(ctx, "owner@example.invalid", "isolated-owner-password")
	if err != nil {
		t.Fatal(err)
	}
	_, otherToken, err := st.Login(ctx, "other@example.invalid", "isolated-other-password")
	if err != nil {
		t.Fatal(err)
	}
	nodes := []Node{
		{ID: "a", X: 10, Y: 10, W: 240, Icon: "layers", Tone: "info", Title: "Alpha", Sub: "A only", Blocks: []json.RawMessage{}},
		{ID: "b", X: 300, Y: 10, W: 240, Icon: "layers", Tone: "info", Title: "Beta", Sub: "B only", Blocks: []json.RawMessage{}},
	}
	if _, err = db.Exec(ctx, `INSERT INTO v3_canvases(id,owner_id,environment_id,title,kind,nodes) VALUES('CV-test',$1,'dev','Test','plan',$2::jsonb)`, owner.ID, mustJSON(nodes)); err != nil {
		t.Fatal(err)
	}
	var seen []inference.Request
	fake := &nodeFakeRunner{run: func(_ context.Context, req inference.Request) (string, error) {
		seen = append(seen, req)
		return "answer-" + req.BranchID, nil
	}}
	api := httpapi.New(st, nil, true)
	svc := NewWithRuntime(st, fake)
	svc.Register(api)
	policy := &rbac.V3Policy{Store: st}
	if err = api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}}, policy, st.Session,
		func(ctx context.Context, u core.User, module, action, environment string) error {
			return policy.Authorize(ctx, u, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	request := func(method, path, token, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("X-AIOps-Environment", "dev")
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	path := "/api/v1/canvases/CV-test/nodes/"
	if w := request("POST", path+"a/messages", otherToken, `{"text":"hidden","model":"fake"}`); w.Code != 404 {
		t.Fatalf("other owner status=%d body=%s", w.Code, w.Body.String())
	}
	if w := request("POST", path+"missing/messages", ownerToken, `{"text":"hidden","model":"fake"}`); w.Code != 404 {
		t.Fatalf("missing node status=%d body=%s", w.Code, w.Body.String())
	}
	if w := request("POST", path+"a/messages", ownerToken, `{"text":"hidden","model":"unknown"}`); w.Code != 422 {
		t.Fatalf("unknown model status=%d body=%s", w.Code, w.Body.String())
	}
	bindingsPath := filepath.Join(t.TempDir(), "bindings.json")
	if err := os.WriteFile(bindingsPath, []byte(`{"tier":"nonprod","environments":[{"id":"dev","models":["fake"],"default_model":"fake"},{"id":"test","models":["fake-second"],"default_model":"fake-second"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	bindings, err := v3models.LoadBindings(bindingsPath, "nonprod", []string{"dev", "test"}, fake)
	if err != nil {
		t.Fatal(err)
	}
	svc.Runtime = v3models.NewBoundRunner(fake, bindings)
	if w := request("POST", path+"a/messages", ownerToken, `{"text":"cross environment","model":"fake-second"}`); w.Code != 422 || len(seen) != 0 {
		t.Fatalf("cross-environment model reached runner: %d %s calls=%d", w.Code, w.Body.String(), len(seen))
	}
	svc.Runtime = fake
	if w := request("POST", path+"a/messages", ownerToken, `{"text":"hidden","model":"fake","reasoning":"low"}`); w.Code != 422 {
		t.Fatalf("unknown reasoning status=%d body=%s", w.Code, w.Body.String())
	}
	for _, tc := range []struct{ node, text string }{{"a", "private alpha"}, {"b", "private beta"}, {"a", "follow alpha"}} {
		w := request("POST", path+tc.node+"/messages", ownerToken, `{"text":"`+tc.text+`","model":"fake","reasoning":"high"}`)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"reply":"answer-CV-test/`+tc.node+`"`) {
			t.Fatalf("reply status=%d body=%s", w.Code, w.Body.String())
		}
	}
	if len(seen) != 3 || len(seen[0].Tools) != 0 || len(seen[1].Tools) != 0 || len(seen[2].Tools) != 0 || seen[0].EnvironmentID != "dev" {
		t.Fatalf("unexpected model calls: %#v", seen)
	}
	if len(seen[1].Messages) != 2 || !strings.Contains(seen[1].Messages[0].Content, "Beta") || strings.Contains(seen[1].Messages[0].Content, "Alpha") {
		t.Fatalf("node B received node A context: %#v", seen[1].Messages)
	}
	if len(seen[2].Messages) != 4 || seen[2].Messages[1].Content != "private alpha" || seen[2].Messages[2].Content != "answer-CV-test/a" {
		t.Fatalf("node A failed to reuse its own completed turn: %#v", seen[2].Messages)
	}
	w := request("GET", "/api/v1/canvases/CV-test", ownerToken, "")
	if w.Code != 200 {
		t.Fatalf("detail status=%d body=%s", w.Code, w.Body.String())
	}
	var detail struct {
		Threads map[string]nodeThread `json:"threads"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.Threads["a"].Turns) != 4 || len(detail.Threads["b"].Turns) != 2 || detail.Threads["a"].Context != "A only" {
		t.Fatalf("persisted node threads: %#v", detail.Threads)
	}
	firstID := strings.TrimSuffix(detail.Threads["a"].Turns[0].ID, ":user")
	secondID := strings.TrimSuffix(detail.Threads["a"].Turns[2].ID, ":user")
	if _, err = db.Exec(ctx, `UPDATE v3_canvas_node_exchanges SET created_at=CASE id
 WHEN $1 THEN clock_timestamp()+interval '1 day'
 WHEN $2 THEN clock_timestamp()-interval '1 day' END WHERE id IN ($1,$2)`, firstID, secondID); err != nil {
		t.Fatal(err)
	}
	w = request("GET", "/api/v1/canvases/CV-test", ownerToken, "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &detail) != nil || detail.Threads["a"].Turns[0].Text != "private alpha" {
		t.Fatalf("node turns followed inverted timestamps: status=%d body=%s", w.Code, w.Body.String())
	}
	if _, err = db.Exec(ctx, `INSERT INTO v3_user_settings(user_id,ai_preferences)
 VALUES($1,'{"default":{"model":"fake-second","reasoning":"high"}}'::jsonb)
 ON CONFLICT(user_id) DO UPDATE SET ai_preferences=EXCLUDED.ai_preferences`, owner.ID); err != nil {
		t.Fatal(err)
	}
	w = request("POST", path+"a/messages", ownerToken, `{"text":"preferred model"}`)
	if w.Code != 200 || len(seen) != 4 || seen[3].ModelID != "fake-second" || seen[3].Reasoning != "high" {
		t.Fatalf("user default model ignored: status=%d body=%s calls=%#v", w.Code, w.Body.String(), seen)
	}
	if len(seen[3].Messages) != 6 || seen[3].Messages[1].Content != "private alpha" || seen[3].Messages[3].Content != "follow alpha" {
		t.Fatalf("model context followed inverted timestamps: %#v", seen[3].Messages)
	}
	svc.Runtime = v3models.NewBoundRunner(fake, bindings)
	if selected, reasoning, err := svc.defaultNodeChoice(ctx, owner.ID, "dev"); err != nil || selected.ID != "fake" || reasoning != "" {
		t.Fatalf("cross-environment saved preference did not fall back: %+v %q %v", selected, reasoning, err)
	}
	svc.Runtime = fake
	if _, err = db.Exec(ctx, `UPDATE v3_user_settings SET ai_preferences='{"default":{"model":"removed","reasoning":"high"}}'::jsonb WHERE user_id=$1`, owner.ID); err != nil {
		t.Fatal(err)
	}
	w = request("POST", path+"b/messages", ownerToken, `{"text":"fallback model"}`)
	if w.Code != 200 || len(seen) != 5 || seen[4].ModelID != "fake" || seen[4].Reasoning != "" {
		t.Fatalf("removed default did not fall back: status=%d body=%s calls=%#v", w.Code, w.Body.String(), seen)
	}
	var audits int
	var leaked bool
	if err = db.QueryRow(ctx, `SELECT count(*),COALESCE(bool_or(changes::text LIKE '%private alpha%'),false)
 FROM management_audit WHERE action IN ('v3.canvas_node.reply_started','v3.canvas_node.reply_completed') AND target LIKE 'CNT-%'`).Scan(&audits, &leaked); err != nil || audits != 10 || leaked {
		t.Fatalf("node audit missing or leaked message: count=%d leaked=%v err=%v", audits, leaked, err)
	}
	if _, err = db.Exec(ctx, `CREATE FUNCTION reject_node_completion_audit() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN IF NEW.action='v3.canvas_node.reply_completed' THEN RAISE EXCEPTION 'audit unavailable'; END IF;
 RETURN NEW; END $$;
 CREATE TRIGGER reject_node_completion BEFORE INSERT ON management_audit
 FOR EACH ROW EXECUTE FUNCTION reject_node_completion_audit()`); err != nil {
		t.Fatal(err)
	}
	w = request("POST", path+"b/messages", ownerToken, `{"text":"completion audit fails","model":"fake"}`)
	if w.Code != 503 {
		t.Fatalf("audit failure status=%d body=%s", w.Code, w.Body.String())
	}
	if _, err = db.Exec(ctx, `DROP TRIGGER reject_node_completion ON management_audit; DROP FUNCTION reject_node_completion_audit()`); err != nil {
		t.Fatal(err)
	}
	var failedState, failedText string
	if err = db.QueryRow(ctx, `SELECT state,COALESCE(assistant_text,'') FROM v3_canvas_node_exchanges
 WHERE canvas_id='CV-test' AND node_id='b' ORDER BY sort_order DESC LIMIT 1`).Scan(&failedState, &failedText); err != nil || failedState != "failed" || failedText != "" {
		t.Fatalf("audit failure committed assistant: state=%q text=%q err=%v", failedState, failedText, err)
	}
	// The database slot, not an in-process mutex, serializes one node's
	// requests. A competing HTTP request cannot start a second model call.
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	fake.run = func(ctx context.Context, req inference.Request) (string, error) {
		once.Do(func() { close(entered) })
		select {
		case <-release:
			return "serialized", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	first := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		first <- request("POST", path+"a/messages", ownerToken, `{"text":"first concurrent","model":"fake"}`)
	}()
	<-entered
	if concurrent := request("POST", path+"a/messages", ownerToken, `{"text":"second concurrent","model":"fake"}`); concurrent.Code != 409 {
		t.Fatalf("concurrent node reply status=%d body=%s", concurrent.Code, concurrent.Body.String())
	}
	close(release)
	if result := <-first; result.Code != 200 {
		t.Fatalf("first concurrent reply status=%d body=%s", result.Code, result.Body.String())
	}
	// Polling must interrupt an in-flight model request when the grant is
	// removed, before it can return a reply.
	started := make(chan struct{})
	fake.run = func(ctx context.Context, req inference.Request) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	}
	active := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		active <- request("POST", path+"b/messages", ownerToken, `{"text":"revoke while running","model":"fake"}`)
	}()
	<-started
	if _, err = db.Exec(ctx, `DELETE FROM rbac_user_roles WHERE user_id=$1 AND role_id='node-editor'`, owner.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-active:
		if result.Code != 503 {
			t.Fatalf("in-flight revoked reply status=%d body=%s", result.Code, result.Body.String())
		}
	case <-time.After(8 * time.Second):
		t.Fatal("in-flight model was not interrupted after revocation")
	}
	if _, err = db.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,'node-editor')`, owner.ID); err != nil {
		t.Fatal(err)
	}
	// A revoked role during generation must reject the answer at commit time.
	fake.run = func(_ context.Context, req inference.Request) (string, error) {
		if _, e := db.Exec(ctx, `DELETE FROM rbac_user_roles WHERE user_id=$1 AND role_id='node-editor'`, owner.ID); e != nil {
			t.Fatal(e)
		}
		return "should-not-persist", nil
	}
	w = request("POST", path+"a/messages", ownerToken, `{"text":"after revocation","model":"fake"}`)
	if w.Code != 503 {
		t.Fatalf("revoked reply status=%d body=%s", w.Code, w.Body.String())
	}
	var completed int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM v3_canvas_node_exchanges WHERE canvas_id='CV-test' AND assistant_text='should-not-persist'`).Scan(&completed); err != nil || completed != 0 {
		t.Fatalf("revoked answer saved: count=%d err=%v", completed, err)
	}
	// A completion transaction may start while a canvas row is locked and
	// acquire that lock only after the session expires. It must use wall time
	// at the permission read, not its earlier transaction timestamp.
	if _, err = db.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,'node-editor')`, owner.ID); err != nil {
		t.Fatal(err)
	}
	model, ok := svc.selectedModel("fake")
	if !ok {
		t.Fatal("fake model missing")
	}
	pending, _, status, err := svc.startNodeTurn(ctx, ownerToken, owner, "dev", "CV-test", "a", nodeMessageRequest{Text: "expired session"}, model)
	if err != nil {
		t.Fatalf("start pending status=%d err=%v", status, err)
	}
	lock, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(context.Background())
	if _, err = lock.Exec(ctx, `SELECT 1 FROM v3_canvases WHERE id='CV-test' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		finished <- svc.completeNodeTurn(ownerToken, owner, "dev", "CV-test", "a", pending, "must-not-persist")
	}()
	waiting := false
	for until := time.Now().Add(2 * time.Second); time.Now().Before(until); time.Sleep(10 * time.Millisecond) {
		if err = adminDB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
 WHERE datname=current_database() AND wait_event_type='Lock'
 AND query LIKE '%SELECT owner_id,environment_id,nodes FROM v3_canvases%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
	}
	if !waiting {
		t.Fatal("completion did not block on canvas row")
	}
	if _, err = db.Exec(ctx, `UPDATE sessions SET expires_at=clock_timestamp()+interval '700 milliseconds' WHERE token_hash=$1`, store.Hash(ownerToken)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(850 * time.Millisecond)
	if err = lock.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-finished:
		if err == nil {
			t.Fatal("completion accepted a session expired during canvas lock wait")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("blocked completion did not return")
	}
	svc.failNodeTurn(pending)
	if err = db.QueryRow(ctx, `SELECT count(*) FROM v3_canvas_node_exchanges WHERE id=$1 AND assistant_text IS NOT NULL`, pending).Scan(&completed); err != nil || completed != 0 {
		t.Fatalf("expired session answer saved: count=%d err=%v", completed, err)
	}
	if _, err = db.Exec(ctx, `UPDATE sessions SET expires_at=clock_timestamp()+interval '1 hour' WHERE token_hash=$1`, store.Hash(ownerToken)); err != nil {
		t.Fatal(err)
	}
	// A child sees its own blocks, transitive parent blocks and completed
	// parent exchanges. It never receives sibling data or failed prompts.
	richNodes := append(nodes, Node{ID: "c", X: 600, Y: 10, W: 240, Icon: "layers", Tone: "info", Title: "Sibling", Sub: "sibling-secret", Blocks: []json.RawMessage{json.RawMessage(`{"type":"text","text":"sibling-block-secret"}`)}})
	richNodes[0].Blocks = []json.RawMessage{json.RawMessage(`{"type":"text","text":"ancestor-block"}`)}
	richNodes[1].Blocks = []json.RawMessage{json.RawMessage(`{"type":"text","text":"current-block"}`)}
	if _, err = db.Exec(ctx, `UPDATE v3_canvases SET goal='plan-goal',nodes=$1::jsonb,edges=$2::jsonb WHERE id='CV-test'`, mustJSON(richNodes), mustJSON([]Edge{{From: "a", To: "b"}})); err != nil {
		t.Fatal(err)
	}
	var inheritedRequest inference.Request
	modelCalls := 0
	fake.run = func(_ context.Context, req inference.Request) (string, error) {
		modelCalls++
		inheritedRequest = req
		return "inherited answer", nil
	}
	w = request("POST", path+"b/messages", ownerToken, `{"text":"check inheritance","model":"fake"}`)
	if w.Code != 200 || modelCalls != 1 {
		t.Fatalf("inherited reply status=%d body=%s calls=%d", w.Code, w.Body.String(), modelCalls)
	}
	var prompt strings.Builder
	for _, message := range inheritedRequest.Messages {
		prompt.WriteString(message.Content)
	}
	for _, marker := range []string{"plan-goal", "ancestor-block", "current-block", "private alpha"} {
		if !strings.Contains(prompt.String(), marker) {
			t.Fatalf("inherited context missing %q: %s", marker, prompt.String())
		}
	}
	for _, marker := range []string{"sibling-secret", "sibling-block-secret", "after revocation", "completion audit fails"} {
		if strings.Contains(prompt.String(), marker) {
			t.Fatalf("inherited context leaked %q: %s", marker, prompt.String())
		}
	}
	// A large, otherwise valid node block returns a bounded input error
	// before a prompt is recorded or a model is called.
	largeText := strings.Repeat("x", 8000)
	richNodes[1].Blocks = []json.RawMessage{
		mustJSON(map[string]string{"type": "text", "text": largeText}),
		mustJSON(map[string]string{"type": "text", "text": largeText}),
		mustJSON(map[string]string{"type": "text", "text": largeText}),
		mustJSON(map[string]string{"type": "text", "text": largeText}),
	}
	if _, err = db.Exec(ctx, `UPDATE v3_canvases SET nodes=$1::jsonb WHERE id='CV-test'`, mustJSON(richNodes)); err != nil {
		t.Fatal(err)
	}
	var before int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM v3_canvas_node_exchanges WHERE canvas_id='CV-test'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	w = request("POST", path+"b/messages", ownerToken, `{"text":"too much context","model":"fake"}`)
	if w.Code != 413 || modelCalls != 1 || !strings.Contains(w.Body.String(), `"code":"node_context_too_large"`) {
		t.Fatalf("oversized node context status=%d body=%s calls=%d", w.Code, w.Body.String(), modelCalls)
	}
	var after int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM v3_canvas_node_exchanges WHERE canvas_id='CV-test'`).Scan(&after); err != nil || after != before {
		t.Fatalf("oversized context was recorded: before=%d after=%d err=%v", before, after, err)
	}
	if _, err = db.Exec(ctx, `UPDATE v3_canvases SET nodes=$1::jsonb WHERE id='CV-test'`, mustJSON(nodes)); err != nil {
		t.Fatal(err)
	}
	// Hold the completion audit after the permission read. A concurrent
	// grant deletion must wait until the accepted reply commits.
	accepted, _, status, err := svc.startNodeTurn(ctx, ownerToken, owner, "dev", "CV-test", "a", nodeMessageRequest{Text: "grant race"}, model)
	if err != nil || status != 0 {
		t.Fatalf("start grant race status=%d err=%v", status, err)
	}
	auditLock, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer auditLock.Rollback(context.Background())
	if _, err = auditLock.Exec(ctx, `LOCK TABLE management_audit IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	completedTurn := make(chan error, 1)
	go func() {
		completedTurn <- svc.completeNodeTurn(ownerToken, owner, "dev", "CV-test", "a", accepted, "committed before revocation")
	}()
	waiting = false
	for until := time.Now().Add(2 * time.Second); time.Now().Before(until); time.Sleep(10 * time.Millisecond) {
		if err = adminDB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
 WHERE datname=current_database() AND wait_event_type='Lock'
 AND query LIKE '%INSERT INTO management_audit%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
	}
	if !waiting {
		t.Fatal("completion did not reach blocked audit insert")
	}
	reading := make(chan *httptest.ResponseRecorder, 1)
	go func() { reading <- request("GET", "/api/v1/canvases/CV-test", ownerToken, "") }()
	waiting = false
	for until := time.Now().Add(2 * time.Second); time.Now().Before(until); time.Sleep(10 * time.Millisecond) {
		if err = adminDB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
 WHERE datname=current_database() AND wait_event_type='Lock'
 AND query LIKE '%FOR SHARE OF c%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
	}
	if !waiting {
		t.Fatal("detail read did not wait for reply completion")
	}
	revokeDone := make(chan error, 1)
	go func() {
		_, revokeErr := db.Exec(ctx, `DELETE FROM rbac_user_roles WHERE user_id=$1 AND role_id='node-editor'`, owner.ID)
		revokeDone <- revokeErr
	}()
	waiting = false
	for until := time.Now().Add(2 * time.Second); time.Now().Before(until); time.Sleep(10 * time.Millisecond) {
		if err = adminDB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
 WHERE datname=current_database() AND wait_event_type='Lock'
 AND query LIKE '%DELETE FROM rbac_user_roles WHERE user_id=%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
	}
	if !waiting {
		t.Fatal("grant revocation did not wait for completion")
	}
	if err = auditLock.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-completedTurn; err != nil {
		t.Fatalf("accepted reply failed: %v", err)
	}
	readResult := <-reading
	if readResult.Code != 200 || !strings.Contains(readResult.Body.String(), "committed before revocation") {
		t.Fatalf("detail did not read committed reply: status=%d body=%s", readResult.Code, readResult.Body.String())
	}
	if err = <-revokeDone; err != nil {
		t.Fatalf("grant revocation failed: %v", err)
	}
	if err = db.QueryRow(ctx, `SELECT count(*) FROM v3_canvas_node_exchanges WHERE id=$1 AND state='completed' AND assistant_text='committed before revocation'`, accepted).Scan(&completed); err != nil || completed != 1 {
		t.Fatalf("accepted reply missing before revocation: count=%d err=%v", completed, err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,'node-editor')`, owner.ID); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(ctx, `SELECT count(*) FROM v3_canvas_node_exchanges WHERE canvas_id='CV-test'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	// Session revocation while the start transaction waits on the canvas
	// row must prevent both prompt persistence and the model call.
	startLock, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer startLock.Rollback(context.Background())
	if _, err = startLock.Exec(ctx, `SELECT 1 FROM v3_canvases WHERE id='CV-test' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	late := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		late <- request("POST", path+"b/messages", ownerToken, `{"text":"revoked while waiting","model":"fake"}`)
	}()
	waiting = false
	for until := time.Now().Add(2 * time.Second); time.Now().Before(until); time.Sleep(10 * time.Millisecond) {
		if err = adminDB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
 WHERE datname=current_database() AND wait_event_type='Lock'
 AND query LIKE '%SELECT owner_id,environment_id,goal,nodes,edges,artifacts FROM v3_canvases%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
	}
	if !waiting {
		t.Fatal("node start did not block on canvas row")
	}
	if _, err = db.Exec(ctx, `DELETE FROM sessions WHERE token_hash=$1`, store.Hash(ownerToken)); err != nil {
		t.Fatal(err)
	}
	if err = startLock.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-late:
		if result.Code != 401 || modelCalls != 1 {
			t.Fatalf("revoked start status=%d body=%s calls=%d", result.Code, result.Body.String(), modelCalls)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("revoked node start did not finish")
	}
	if err = db.QueryRow(ctx, `SELECT count(*) FROM v3_canvas_node_exchanges WHERE canvas_id='CV-test'`).Scan(&after); err != nil || after != before {
		t.Fatalf("revoked start was recorded: before=%d after=%d err=%v", before, after, err)
	}
}
