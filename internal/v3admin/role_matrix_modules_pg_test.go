package v3admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

// PA-231: the built-in roles' route-module grants show in the matrix and
// survive edits of other cells; a custom role can be given and lose them.
func TestRoleMatrixRouteModulesReadAndWritePostgres(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops one random isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_role_modules_test_" + store.ID()
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
	_, adminToken, err := st.Login(ctx, "admin@example.invalid", "isolated-admin-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO rbac_roles(id,name,v3_kind) VALUES('custom-observer','Observer','business')`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('custom-observer','environment','view','global','*')`,
		`INSERT INTO users(id,email,name,role,password_hash) VALUES('observer','observer@example.invalid','Observer','viewer','unused')`,
		// The legacy-role trigger also binds the built-in viewer, which holds
		// every route module; the observer must hold only the custom role.
		`DELETE FROM rbac_user_roles WHERE user_id='observer'`,
		`INSERT INTO rbac_user_roles(user_id,role_id) VALUES('observer','custom-observer')`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
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
	request := func(method, roleID string, matrix []matrixRow) *httptest.ResponseRecorder {
		t.Helper()
		var body []byte
		if method == http.MethodPut {
			if body, err = json.Marshal(matrixUpdate{Matrix: matrix}); err != nil {
				t.Fatal(err)
			}
		}
		r := httptest.NewRequest(method, "/api/v1/roles/"+roleID, bytes.NewReader(body))
		r.Header.Set("X-AIOps-Environment", "dev")
		r.Header.Set("Accept-Language", "zh-CN")
		if method == http.MethodPut {
			var revision int64
			if err := db.QueryRow(ctx, `SELECT v3_matrix_revision FROM rbac_roles WHERE id=$1`, roleID).Scan(&revision); err != nil {
				t.Fatal(err)
			}
			r.Header.Set("If-Role-Matrix-Revision", strconv.FormatInt(revision, 10))
		}
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: adminToken})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	read := func(roleID string) []matrixRow {
		t.Helper()
		w := request(http.MethodGet, roleID, nil)
		var detail roleDetail
		if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil || w.Code != 200 {
			t.Fatalf("GET %s status=%d body=%s", roleID, w.Code, w.Body.String())
		}
		return detail.Matrix
	}
	write := func(roleID string, matrix []matrixRow) {
		t.Helper()
		if w := request(http.MethodPut, roleID, matrix); w.Code != 200 {
			t.Fatalf("PUT %s status=%d body=%s", roleID, w.Code, w.Body.String())
		}
	}
	cells := func(matrix []matrixRow, resource string) []int {
		t.Helper()
		for _, row := range matrix {
			if row.Resource == resource {
				return row.Grants
			}
		}
		t.Fatalf("matrix has no %s row: %+v", resource, matrix)
		return nil
	}
	set := func(matrix []matrixRow, resource string, col, value int) {
		t.Helper()
		cells(matrix, resource)[col] = value
	}
	routeModules := []string{"home", "traces", "logs", "models", "workspace"}
	rowNames := map[string]string{"home": "总览", "traces": "链路", "logs": "日志", "models": "模型", "workspace": "工作区设置"}
	grantsOf := func(roleID string, modules ...string) []string {
		t.Helper()
		if modules == nil {
			modules = []string{} // a nil slice is sent as NULL
		}
		rows, err := db.Query(ctx, `SELECT module||'.'||action||'.'||scope_kind||'.'||scope_id||'.'||assurance
 FROM v3_role_grants WHERE role_id=$1 AND (cardinality($2::text[])=0 OR module=ANY($2::text[])) ORDER BY 1`, roleID, modules)
		if err != nil {
			t.Fatal(err)
		}
		out, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	allows := func(module, action string) bool {
		t.Helper()
		err := policy.Authorize(ctx, core.User{ID: "observer"}, rbac.V3Decision{Module: module, Action: action, EnvironmentID: "dev"})
		if err != nil && !errors.Is(err, store.ErrForbidden) {
			t.Fatal(err)
		}
		return err == nil
	}

	// Built-in roles show the grants they already hold.
	{
		for _, roleID := range []string{"viewer", "operator", "change-approver", "admin"} {
			matrix := read(roleID)
			if len(matrix) != len(matrixCatalog) {
				t.Fatalf("%s matrix rows=%d", roleID, len(matrix))
			}
			for _, module := range routeModules {
				want := make([]int, len(matrixActions))
				for col, action := range matrixActions {
					if slices.Contains(grantsOf(roleID, module), module+"."+action+".global.*.standard") {
						want[col] = 1
					}
				}
				if got := cells(matrix, rowNames[module]); !slices.Equal(got, want) || want[0] != 1 {
					t.Fatalf("%s %s row=%v; stored grants say %v", roleID, module, got, want)
				}
			}
		}
		// Migration 162 gives admin view/change/delete/manage and never approve.
		if got := cells(read("admin"), "链路"); !slices.Equal(got, []int{1, 1, 1, 0, 1}) {
			t.Fatalf("admin traces row=%v", got)
		}
	}

	// Editing another cell of a built-in role keeps its route-module grants.
	{
		for _, edit := range []struct {
			roleID, resource string
			col, value       int
		}{{"operator", "事件与告警", 2, 1}, {"admin", "审计日志", 2, 0}} {
			before := grantsOf(edit.roleID, routeModules...)
			if len(before) == 0 {
				t.Fatalf("%s holds no route-module grants", edit.roleID)
			}
			matrix := read(edit.roleID)
			set(matrix, edit.resource, edit.col, edit.value)
			write(edit.roleID, matrix)
			if after := grantsOf(edit.roleID, routeModules...); !slices.Equal(after, before) {
				t.Fatalf("%s route-module grants changed: before=%v after=%v", edit.roleID, before, after)
			}
			if got := cells(read(edit.roleID), edit.resource)[edit.col]; got != edit.value {
				t.Fatalf("%s %s cell=%d; want %d", edit.roleID, edit.resource, got, edit.value)
			}
		}
		// Changing one route-module cell writes that grant only.
		before := grantsOf("operator")
		matrix := read("operator")
		set(matrix, "链路", 4, 1)
		write("operator", matrix)
		after := grantsOf("operator")
		if len(after) != len(before)+1 || !slices.Contains(after, "traces.manage.global.*.standard") {
			t.Fatalf("operator grants before=%v after=%v", before, after)
		}
		set(matrix, "链路", 4, 0)
		write("operator", matrix)
		if again := grantsOf("operator"); !slices.Equal(again, before) {
			t.Fatalf("operator grants after revoking traces.manage=%v; want %v", again, before)
		}
	}

	// A custom role can be given and lose route-module grants.
	{
		if got := grantsOf("custom-observer", routeModules...); len(got) != 0 || allows("traces", "view") {
			t.Fatalf("fixture already grants %v", got)
		}
		matrix := read("custom-observer")
		for _, module := range routeModules {
			set(matrix, rowNames[module], 0, 1)
		}
		set(matrix, "链路", 4, 1)
		set(matrix, "模型", 4, 1)
		write("custom-observer", matrix)
		want := []string{"home.view.global.*.standard", "logs.view.global.*.standard", "models.manage.global.*.standard",
			"models.view.global.*.standard", "traces.manage.global.*.standard", "traces.view.global.*.standard",
			"workspace.view.global.*.standard"}
		if got := grantsOf("custom-observer", routeModules...); !slices.Equal(got, want) {
			t.Fatalf("custom grants=%v; want %v", got, want)
		}
		for _, need := range [][2]string{{"home", "view"}, {"traces", "view"}, {"traces", "manage"}, {"logs", "view"}, {"models", "manage"}, {"workspace", "view"}} {
			if !allows(need[0], need[1]) {
				t.Fatalf("granted %s.%s does not authorize", need[0], need[1])
			}
		}
		if allows("workspace", "manage") {
			t.Fatal("workspace.manage authorized without a grant")
		}
		if got := cells(read("custom-observer"), "链路"); !slices.Equal(got, []int{1, 0, 0, 0, 1}) {
			t.Fatalf("read back traces row=%v", got)
		}

		set(matrix, "链路", 4, 0)
		set(matrix, "模型", 4, 0)
		set(matrix, "日志", 0, 0)
		write("custom-observer", matrix)
		want = []string{"home.view.global.*.standard", "models.view.global.*.standard", "traces.view.global.*.standard", "workspace.view.global.*.standard"}
		if got := grantsOf("custom-observer", routeModules...); !slices.Equal(got, want) {
			t.Fatalf("custom grants after revoke=%v; want %v", got, want)
		}
		if allows("traces", "manage") || allows("logs", "view") || !allows("traces", "view") {
			t.Fatal("revoked grants still authorize, or a kept grant stopped authorizing")
		}
		var changes []byte
		if err := db.QueryRow(ctx, `SELECT changes FROM management_audit WHERE action='v3.role.matrix.updated'
 AND target='custom-observer' ORDER BY id DESC LIMIT 1`).Scan(&changes); err != nil {
			t.Fatal(err)
		}
		var audited struct {
			Before map[string][]int `json:"before"`
			After  map[string][]int `json:"after"`
		}
		if err := json.Unmarshal(changes, &audited); err != nil || !slices.Equal(audited.Before["traces"], []int{1, 0, 0, 0, 1}) ||
			!slices.Equal(audited.After["traces"], []int{1, 0, 0, 0, 0}) || !slices.Equal(audited.After["logs"], []int{0, 0, 0, 0, 0}) {
			t.Fatalf("audit did not record route-module codes: %s err=%v", changes, err)
		}
	}

	// PA-232: 对话 and 画布 are separate rows. A role holding conversation only
	// shows 对话 granted and 画布 empty; 画布 grants canvas and nothing else.
	{
		for _, statement := range []string{
			`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('custom-observer','conversation','view','global','*')`,
		} {
			if _, err := db.Exec(ctx, statement); err != nil {
				t.Fatal(err)
			}
		}
		matrix := read("custom-observer")
		for _, row := range matrix {
			if row.Resource == "对话与画布" {
				t.Fatalf("matrix still has the combined row: %+v", matrix)
			}
		}
		if got := cells(matrix, "对话"); !slices.Equal(got, []int{1, 0, 0, 0, 0}) {
			t.Fatalf("对话 row=%v", got)
		}
		if got := cells(matrix, "画布"); !slices.Equal(got, []int{0, 0, 0, 0, 0}) || allows("canvas", "view") {
			t.Fatalf("画布 row=%v shows a grant the role doesn't hold", got)
		}
		set(matrix, "画布", 0, 1)
		set(matrix, "画布", 1, 1)
		write("custom-observer", matrix)
		if got := grantsOf("custom-observer", "canvas"); !slices.Equal(got, []string{"canvas.change.global.*.standard", "canvas.view.global.*.standard"}) {
			t.Fatalf("canvas grants=%v", got)
		}
		if got := grantsOf("custom-observer", "conversation"); !slices.Equal(got, []string{"conversation.view.global.*.standard"}) {
			t.Fatalf("writing 画布 changed conversation grants: %v", got)
		}
		if !allows("canvas", "change") || allows("conversation", "change") {
			t.Fatal("画布 cells don't authorize canvas alone")
		}
		// Built-in roles show their stored canvas grants in the 画布 row.
		for _, roleID := range []string{"viewer", "operator", "admin"} {
			want := make([]int, len(matrixActions))
			for col, action := range matrixActions {
				if slices.Contains(grantsOf(roleID, "canvas"), "canvas."+action+".global.*.standard") {
					want[col] = 1
				}
			}
			if got := cells(read(roleID), "画布"); !slices.Equal(got, want) {
				t.Fatalf("%s 画布 row=%v; stored grants say %v", roleID, got, want)
			}
		}
	}

	// A matrix without the route-module rows is refused and changes nothing.
	{
		before := grantsOf("operator")
		var short []matrixRow
		for _, row := range read("operator") {
			if row.Resource == "事件与告警" {
				row.Grants[2] = 0
			}
			if !slices.Contains([]string{"总览", "链路", "日志", "模型", "工作区设置"}, row.Resource) {
				short = append(short, row)
			}
		}
		w := request(http.MethodPut, "operator", short)
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 422 || body.Error.Code != "invalid_matrix" {
			t.Fatalf("short matrix status=%d body=%s", w.Code, w.Body.String())
		}
		if after := grantsOf("operator"); !slices.Equal(after, before) {
			t.Fatalf("refused write changed grants: before=%v after=%v", before, after)
		}
	}
}
