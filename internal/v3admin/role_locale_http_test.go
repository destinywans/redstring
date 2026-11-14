package v3admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"

	"github.com/destinywans/redstring/internal/core"
)

func TestSystemRoleLocaleHTTP(t *testing.T) {
	s, actor, token, api := accessHTTPFixture(t)
	if _, err := s.Store.DB.Exec(t.Context(), `
 INSERT INTO rbac_roles(id,name,description) VALUES ('custom-review','知识审核','自定义说明');
 INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES ('custom-review','knowledge','view','environment','dev')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.DB.Exec(t.Context(), `
 INSERT INTO rbac_user_roles(user_id,role_id) VALUES ($1,'custom-review')`, actor.ID); err != nil {
		t.Fatal(err)
	}

	fetch := func(path, language string, out any) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1"+path, nil)
		req.Header.Set("X-AIOps-Environment", "dev")
		req.Header.Set("Accept-Language", language)
		req.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s (%s) returned %d: %s", path, language, w.Code, w.Body)
		}
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			t.Fatalf("GET %s (%s) body %s: %v", path, language, w.Body, err)
		}
		return w
	}
	wantNames := map[string]string{
		"admin": "Platform administrator", "operator": "Operator",
		"viewer": "Read-only member", "change-approver": "Change approver",
		"custom-review": "知识审核",
	}
	var roles struct {
		Items []role `json:"items"`
	}
	fetch("/roles", "en-US", &roles)
	for _, item := range roles.Items {
		if want, ok := wantNames[item.ID]; ok {
			if item.Name != want {
				t.Errorf("role %q name = %q, want %q", item.ID, item.Name, want)
			}
			delete(wantNames, item.ID)
		}
	}
	if len(wantNames) > 0 {
		t.Fatalf("roles missing from list: %v", wantNames)
	}

	var english, chinese, defaultLanguage, custom roleDetail
	englishRole := fetch("/roles/change-approver", "en-US", &english)
	chineseRole := fetch("/roles/change-approver", "zh-CN", &chinese)
	fetch("/roles/change-approver", "", &defaultLanguage)
	fetch("/roles/custom-review", "en-US", &custom)
	if english.ID != "change-approver" || english.Kind != "system" ||
		english.Name != "Change approver" || english.Desc != "View and approve changes to associated resources." {
		t.Errorf("unexpected English system role: %+v", english)
	}
	if chinese.Name != "变更审批员" || chinese.Desc != "查看并审批已关联资源的变更。" ||
		defaultLanguage.Name != chinese.Name || defaultLanguage.Desc != chinese.Desc {
		t.Errorf("Chinese name and description = %q / %q, default = %q / %q",
			chinese.Name, chinese.Desc, defaultLanguage.Name, defaultLanguage.Desc)
	}
	if version := englishRole.Header().Get("X-Role-Matrix-Revision"); version == "" || version != chineseRole.Header().Get("X-Role-Matrix-Revision") {
		t.Error("role matrix revision changed with language")
	}
	if custom.Name != "知识审核" || custom.Desc != "自定义说明" || custom.Kind != "business" {
		t.Errorf("custom role metadata was altered: %+v", custom)
	}

	for _, name := range []string{"admin", "平台管理员", "Platform administrator", "知识审核"} {
		for _, language := range []string{"en-US", "zh-CN"} {
			var members membersView
			fetch("/members?role="+url.QueryEscape(name), language, &members)
			wantAdminName := "平台管理员"
			if language == "en-US" {
				wantAdminName = "Platform administrator"
			}
			if len(members.Items) != 1 || members.Items[0].ID != actor.ID ||
				!slices.Contains(members.Items[0].Roles, wantAdminName) || !slices.Contains(members.Items[0].Roles, "知识审核") {
				t.Errorf("filter %q in %s returned %+v, want the same member and localized role names", name, language, members.Items)
			}
		}
	}

	memberPath := "/members/" + url.PathEscape(actor.ID)
	var member memberDetail
	fetch(memberPath, "en-US", &member)
	if !slices.Contains(member.Roles, memberRef{ID: "admin", Name: "Platform administrator"}) ||
		!slices.Contains(member.Roles, memberRef{ID: "custom-review", Name: "知识审核"}) {
		t.Errorf("member detail roles = %+v", member.Roles)
	}

	var chineseAccess, englishAccess memberAccess
	fetch(memberPath+"/access", "zh-CN", &chineseAccess)
	fetch(memberPath+"/access", "en-US", &englishAccess)
	if englishAccess.Revision == "" || englishAccess.Revision != chineseAccess.Revision {
		t.Error("the member access revision changed with language")
	}
	if !slices.ContainsFunc(englishAccess.Roles, func(role accessRole) bool {
		return role.ID == "admin" && role.Name == "Platform administrator"
	}) {
		t.Error("member access did not localize the platform role")
	}

	target, err := s.Store.CreateUser(t.Context(), core.User{
		Email: "role-locale@example.invalid", Name: "Member", Role: "viewer",
	}, "synthetic-role-locale-password")
	if err != nil {
		t.Fatal(err)
	}
	accessPath := "/members/" + url.PathEscape(target.ID) + "/access"
	var initial memberAccess
	fetch(accessPath, "zh-CN", &initial)
	enabled := true
	input := memberAccessInput{
		ExpectedRevision: initial.Revision, Enabled: &enabled,
		RoleIDs: []string{"custom-review"}, RoleRevisions: map[string]string{"custom-review": "1"},
	}
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/v1"+accessPath, bytes.NewReader(body))
	req.Header.Set("X-AIOps-Environment", "dev")
	req.Header.Set("Accept-Language", "en-US")
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
	w := httptest.NewRecorder()
	api.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("English write with Chinese revision returned %d: %s", w.Code, w.Body)
	}
	var updated, readBack memberAccess
	if err = json.Unmarshal(w.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	fetch(accessPath, "zh-CN", &readBack)
	if !slices.ContainsFunc(updated.Roles, func(role accessRole) bool {
		return role.ID == "viewer" && role.Name == "Read-only member"
	}) || !slices.ContainsFunc(readBack.Roles, func(role accessRole) bool {
		return role.ID == "custom-review" && role.Name == "知识审核" && role.Origin == "direct"
	}) || updated.Revision == initial.Revision || updated.Revision != readBack.Revision {
		t.Error("cross-language access update did not preserve localized labels and revision")
	}

	if _, err := s.Store.DB.Exec(t.Context(), `
 UPDATE rbac_roles SET name='变更审批',description='审批迁移时未归档应用；不包含执行。'
 WHERE id='change-approver'`); err != nil {
		t.Fatal(err)
	}
	fetch("/roles/change-approver", "en-US", &english)
	if english.Name != "Change approver" ||
		english.Desc != "Approve changes to applications that were active at migration. Execution is excluded." {
		t.Errorf("legacy seed metadata was not localized: %q / %q", english.Name, english.Desc)
	}
	if _, err := s.Store.DB.Exec(t.Context(), `
 UPDATE rbac_roles SET name='自定义审批员',description='由管理员编辑的说明' WHERE id='change-approver'`); err != nil {
		t.Fatal(err)
	}
	fetch("/roles/change-approver", "en-US", &english)
	if english.Name != "自定义审批员" || english.Desc != "由管理员编辑的说明" {
		t.Errorf("edited legacy metadata was altered: %q / %q", english.Name, english.Desc)
	}
}
