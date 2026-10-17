package v3models

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/store"
)

func readAdminResponse(t *testing.T, w *httptest.ResponseRecorder) adminView {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("admin status=%d body=%s", w.Code, w.Body.String())
	}
	var view adminView
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"providers", "items", "detail"} {
		if _, ok := raw[key]; !ok {
			t.Fatalf("missing top-level %s: %s", key, w.Body.String())
		}
	}
	if len(view.Items) == 0 {
		t.Fatalf("admin returned no bound models: %s", w.Body.String())
	}
	item := raw["items"].([]any)[0].(map[string]any)
	for _, key := range []string{"id", "name", "provider", "brand", "connector", "api", "reasoning", "tools", "vision", "context_k", "cost", "available", "used_by", "scope", "status"} {
		if _, ok := item[key]; !ok {
			t.Fatalf("missing model item %s: %s", key, w.Body.String())
		}
	}
	return view
}

func TestModelAdminVisionReflectsConfiguredInputCapability(t *testing.T) {
	view := buildAdminView(adminText{}, "test", []inference.Model{
		{ID: "image", Provider: "openai", API: "responses", ImageInput: true, PDFInput: true},
		{ID: "text", Provider: "openai", API: "responses"},
	}, "image", nil, nil, "", nil)
	if len(view.Items) != 2 || !view.Items[0].Vision || view.Items[1].Vision {
		t.Fatalf("vision catalog diverged from configured model capability: %+v", view.Items)
	}
}

func TestModelAdminBoundViewObservationsAndHistoricalUse(t *testing.T) {
	st := isolatedModelStore(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models/local" {
			t.Errorf("unexpected model metadata request: %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"id":"local"}`))
	}))
	defer upstream.Close()
	runtime, err := inference.New(inference.Config{Providers: []inference.Provider{
		{ID: "openai:local", Kind: "openai", Model: "local", API: "responses", BaseURL: upstream.URL + "/v1",
			ReasoningKind: "openai_effort", ReasoningLevels: []string{"low", "high"}, ContextWindow: 32768, InputBudget: 24576},
		{ID: "claude:second", Kind: "claude", Model: "second", BaseURL: upstream.URL + "/v1"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	config := `{"tier":"nonprod","environments":[{"id":"dev","models":["openai:local"],"default_model":"openai:local"},{"id":"test","models":["claude:second"],"default_model":"claude:second"}]}`
	bindings, err := LoadBindings(writeBindingConfig(t, config), "nonprod", []string{"dev", "test"}, runtime)
	if err != nil {
		t.Fatal(err)
	}
	api, service := modelAPI(t, st, NewBoundRunner(runtime, bindings))
	if service.bindings != bindings || service.Source == nil {
		t.Fatal("admin view did not receive the execution runner binding and probe source")
	}
	user, token, err := st.Login(t.Context(), "admin@example.invalid", "isolated-model-admin-password")
	if err != nil {
		t.Fatal(err)
	}
	if w := modelRequest(api, http.MethodGet, "/api/v1/models/admin", token, "dev", ""); w.Code != http.StatusServiceUnavailable ||
		!strings.Contains(w.Body.String(), "model_tool_allowlist_unavailable") {
		t.Fatalf("missing tool allowlist=%d %s", w.Code, w.Body.String())
	}
	service.ToolModels = map[string]bool{"openai:local": true}
	devIn := func(lang string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/models/admin", nil)
		r.Header.Set("X-AIOps-Environment", "dev")
		r.Header.Set("Accept-Language", lang)
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	dev := func() *httptest.ResponseRecorder { return devIn("en-US") }
	// PA-166: the table's "used by" and "who can use" columns, and the detail
	// card, follow the request language.
	if zh := readAdminResponse(t, devIn("zh-CN")); len(zh.Items) != 1 ||
		zh.Items[0].UsedBy != "暂无使用记录" || zh.Items[0].Scope != "环境：dev；另受对话与画布访问权限限制" ||
		zh.Items[0].Cost != "未配置" || zh.Detail.APIChain[0].Note != "唯一配置的接口；不自动回退。" ||
		zh.Detail.Limits != "配置的上下文窗口：32768 tokens；输入预算：24576 tokens。" ||
		!strings.Contains(strings.Join(zh.Detail.Checks, " "), "没有模型元数据检查记录。") ||
		!strings.Contains(zh.Detail.Roles, "没有模型专属的角色绑定") {
		t.Fatalf("zh admin text: %+v", zh)
	}
	initial := readAdminResponse(t, dev())
	if len(initial.Providers) != 1 || initial.Providers[0].Status != "unverified" ||
		len(initial.Items) != 1 || initial.Items[0].ID != "openai:local" ||
		initial.Items[0].Status != "unverified" || !initial.Items[0].IsDefault ||
		!initial.Items[0].Tools || initial.Items[0].Vision || !initial.Items[0].Available ||
		initial.Items[0].API != "responses" || initial.Items[0].ContextK != 32 ||
		initial.Items[0].UsedBy != "No recorded use" ||
		initial.Items[0].Scope != "Environment: dev; subject to conversation/canvas access" {
		t.Fatalf("admin invented model state: %+v", initial)
	}
	if initial.Detail.Model != "openai:local" || len(initial.Detail.APIChain) != 1 ||
		initial.Detail.APIChain[0].API != "Responses API" || len(initial.Detail.Mapping) != 2 ||
		initial.Detail.Mapping[0].Param != `reasoning.effort = "low"` ||
		!strings.Contains(initial.Detail.Limits, "24576 tokens") ||
		!strings.Contains(strings.Join(initial.Detail.Checks, " "), "No model metadata check") {
		t.Fatalf("admin detail diverged from configured adapter: %+v", initial.Detail)
	}
	service.ToolModels = map[string]bool{}
	if withoutTools := readAdminResponse(t, dev()); withoutTools.Items[0].Tools {
		t.Fatalf("empty operator tool allowlist advertised tools: %+v", withoutTools.Items[0])
	}
	service.ToolModels = map[string]bool{"openai:local": true}
	if w := modelRequest(api, http.MethodPost, "/api/v1/models/check", token, "dev", `{"model":"claude:second"}`); w.Code != http.StatusNotFound {
		t.Fatalf("cross-environment check=%d %s", w.Code, w.Body.String())
	}
	if w := modelRequest(api, http.MethodGet, "/api/v1/models/observations?model=claude%3Asecond", token, "dev", ""); w.Code != http.StatusNotFound {
		t.Fatalf("cross-environment observation=%d %s", w.Code, w.Body.String())
	}
	testView := readAdminResponse(t, modelRequest(api, http.MethodGet, "/api/v1/models/admin", token, "test", ""))
	if len(testView.Items) != 1 || testView.Items[0].ID != "claude:second" || testView.Detail.Model != "claude:second" || testView.Items[0].Tools {
		t.Fatalf("test environment leaked dev model: %+v", testView)
	}
	if w := modelRequest(api, http.MethodPost, "/api/v1/models/check", token, "dev", `{"model":"openai:local"}`); w.Code != http.StatusOK {
		t.Fatalf("local metadata check=%d %s", w.Code, w.Body.String())
	}
	checked := readAdminResponse(t, dev())
	if checked.Items[0].Status != "unverified" || checked.Providers[0].Status != "unverified" ||
		!strings.Contains(strings.Join(checked.Detail.Checks, " "), "metadata lookup succeeded") {
		t.Fatalf("metadata check was misrepresented as continuous health: %+v", checked)
	}
	// PA-83: the check time comes only in RFC 3339; checks no longer carry it.
	if checked.Detail.CheckedAt == nil || strings.Contains(strings.Join(checked.Detail.Checks, " "), *checked.Detail.CheckedAt) ||
		!strings.Contains(strings.Join(checked.Detail.Checks, " "), "metadata lookup succeeded.") || initial.Detail.CheckedAt != nil {
		t.Fatalf("checked_at: %+v / %+v", checked.Detail, initial.Detail)
	}
	insertAdminHistoricalUse(t, st, user.ID)
	used := readAdminResponse(t, dev())
	if !strings.Contains(used.Items[0].UsedBy, "1 completed conversation turns") ||
		!strings.Contains(used.Items[0].UsedBy, "1 completed canvas turns") ||
		!strings.Contains(used.Items[0].UsedBy, "1 recorded Agent LLM spans") ||
		!strings.Contains(used.Detail.Agents, "No active Agent-model binding") {
		t.Fatalf("historical associations missing or promoted into active bindings: %+v", used)
	}
	if zh := readAdminResponse(t, devIn("zh-CN")); zh.Items[0].UsedBy != "历史：1 次已完成的对话回合、1 次已完成的画布回合、1 条 Agent 模型调用记录" ||
		zh.Detail.Agents != "没有生效的 Agent 与模型绑定；历史 Agent 模型调用 1 条。" {
		t.Fatalf("zh historical use: %+v", zh)
	}
	if w := modelRequest(api, http.MethodGet, "/api/v1/models/admin", token, "all", ""); w.Code == http.StatusOK {
		t.Fatalf("all-environment admin view unexpectedly succeeded: %s", w.Body.String())
	}
	if _, err := st.DB.Exec(t.Context(), `DELETE FROM v3_role_grants WHERE role_id='admin' AND module='models' AND action='view'`); err != nil {
		t.Fatal(err)
	}
	if w := dev(); w.Code != http.StatusForbidden {
		t.Fatalf("revoked models.view=%d %s", w.Code, w.Body.String())
	}
}

func insertAdminHistoricalUse(t *testing.T, st *store.Store, userID string) {
	t.Helper()
	statements := []string{
		`INSERT INTO v3_conversations(id,owner_id,environment_id,title)
 VALUES('admin-conversation',$1,'dev','History fixture')`,
		`INSERT INTO v3_conversation_messages(id,conversation_id,role,author) VALUES
 ('admin-user','admin-conversation','user','tester'),
 ('admin-assistant','admin-conversation','assistant','assistant')`,
		`INSERT INTO v3_conversation_replies(id,conversation_id,user_message_id,assistant_message_id,model_id,state,finished_at)
 VALUES('admin-reply','admin-conversation','admin-user','admin-assistant','openai:local','completed',clock_timestamp())`,
		`INSERT INTO v3_canvases(id,owner_id,environment_id,title,kind)
 VALUES('admin-canvas',$1,'dev','History fixture','analysis')`,
		`INSERT INTO v3_canvas_node_exchanges(id,canvas_id,node_id,user_id,author,user_text,assistant_text,
 model_id,model_name,model_provider,state,lease_expires_at,completed_at)
 VALUES('admin-exchange','admin-canvas','node',$1,'tester','question','answer',
 'openai:local','local','openai','completed',clock_timestamp(),clock_timestamp())`,
		`INSERT INTO applications(id,name) VALUES('admin-application','History fixture')`,
		`INSERT INTO v3_application_environments(application_id,environment_id)
 VALUES('admin-application','dev')`,
		`INSERT INTO v3_agents(id,application_id,environment_id,name,owner_id)
 VALUES('admin-agent','admin-application','dev','History fixture',$1)`,
		`INSERT INTO v3_agent_versions(agent_id,version,artifact_ref,created_by)
 VALUES('admin-agent','v1','history:fixture',$1)`,
		`INSERT INTO v3_agent_runs(id,agent_id,version,application_id,environment_id,trigger_kind,status,finished_at)
 VALUES('admin-agent-run','admin-agent','v1','admin-application','dev','manual','success',clock_timestamp())`,
		`INSERT INTO v3_agent_run_spans(id,run_id,kind,name,started_at,status,model_id)
 VALUES('admin-agent-span','admin-agent-run','llm','history:fixture',clock_timestamp(),'ok','openai:local')`,
	}
	for _, statement := range statements {
		var args []any
		if strings.Contains(statement, "$1") {
			args = []any{userID}
		}
		if _, err := st.DB.Exec(t.Context(), statement, args...); err != nil {
			t.Fatalf("historical use fixture: %v; statement=%s", err, statement)
		}
	}
}

func TestModelAdminRequiresBoundRunnerAndRepresentableAPI(t *testing.T) {
	st := isolatedModelStore(t)
	runtime, err := inference.New(inference.Config{Providers: []inference.Provider{
		{ID: "ollama:local", Kind: "ollama", Model: "local", BaseURL: "http://127.0.0.1:11434"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	user, token, err := st.Login(t.Context(), "admin@example.invalid", "isolated-model-admin-password")
	if err != nil || user.ID == "" {
		t.Fatal(err)
	}
	api, _ := modelAPI(t, st, runtime)
	if w := modelRequest(api, http.MethodGet, "/api/v1/models/admin", token, "dev", ""); w.Code != http.StatusServiceUnavailable ||
		!strings.Contains(w.Body.String(), "model_binding_unavailable") {
		t.Fatalf("unbound admin=%d %s", w.Code, w.Body.String())
	}
	config := `{"tier":"nonprod","environments":[{"id":"dev","models":["ollama:local"],"default_model":"ollama:local"},{"id":"test","models":["ollama:local"],"default_model":"ollama:local"}]}`
	bindings, err := LoadBindings(writeBindingConfig(t, config), "nonprod", []string{"dev", "test"}, runtime)
	if err != nil {
		t.Fatal(err)
	}
	api, service := modelAPI(t, st, NewBoundRunner(runtime, bindings))
	service.ToolModels = map[string]bool{}
	if w := modelRequest(api, http.MethodGet, "/api/v1/models/admin", token, "dev", ""); w.Code != http.StatusServiceUnavailable ||
		!strings.Contains(w.Body.String(), "model_admin_api_unsupported") {
		t.Fatalf("native Ollama was mislabeled: %d %s", w.Code, w.Body.String())
	}
}
