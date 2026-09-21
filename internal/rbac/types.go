// Package rbac manages persisted authorization records and atomic association deltas.
package rbac

import "github.com/wans/aiops-canvas/internal/store"

type Service struct{ Store *store.Store }
type Role struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	Builtin         bool   `json:"builtin"`
	Revision        int64  `json:"revision"`
	PermissionCount int    `json:"permission_count"`
	UserCount       int    `json:"user_count"`
	DepartmentCount int    `json:"department_count"`
}
type Permission struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	Builtin       bool   `json:"builtin"`
	Revision      int64  `json:"revision"`
	ResourceCount int    `json:"resource_count"`
}
type Delta struct {
	Add    []string `json:"add"`
	Remove []string `json:"remove"`
}
type Associations struct {
	Revision    int64 `json:"revision"`
	Permissions Delta `json:"permissions"`
	Users       Delta `json:"users"`
	Departments Delta `json:"departments"`
}
type ResourceTarget struct {
	ResourceID string `json:"resource_id"`
	ScopeType  string `json:"scope_type"`
}
type ResourceBinding struct {
	ResourceTarget
	Actions []string `json:"actions"`
}
type ResourceDelta struct {
	Revision int64             `json:"revision"`
	Upsert   []ResourceBinding `json:"upsert"`
	Remove   []ResourceTarget  `json:"remove"`
}
type Candidate struct {
	IsMock            bool     `json:"is_mock"`
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	Description       string   `json:"description"`
	Selected          bool     `json:"selected"`
	Locked            bool     `json:"locked"`
	LockReason        string   `json:"lock_reason,omitempty"`
	Inherited         bool     `json:"inherited"`
	SourceDepartments []string `json:"source_departments"`
	Email             string   `json:"email,omitempty"`
	Action            string   `json:"action,omitempty"`
	ScopeType         string   `json:"scope_type,omitempty"`
	Kind              string   `json:"kind,omitempty"`
	ApplicationID     string   `json:"application_id,omitempty"`
	ApplicationName   string   `json:"application_name,omitempty"`
	Environment       string   `json:"environment,omitempty"`
	DepartmentID      string   `json:"department_id,omitempty"`
	DepartmentName    string   `json:"department_name,omitempty"`
	ResourceCount     int      `json:"resource_count"`
}

var ManagementObjects = []map[string]string{{"id": "rbac", "name": "角色与权限"}, {"id": "users", "name": "成员账户"}, {"id": "teams", "name": "部门"}, {"id": "settings", "name": "平台设置"}, {"id": "applications", "name": "应用目录"}, {"id": "resources", "name": "资源登记"}, {"id": "audit", "name": "审计记录"}}

// Actions is the public resource-binding operation catalog.
var Actions = []map[string]string{{"id": "view", "name": "查看"}, {"id": "update", "name": "变更"}, {"id": "approve", "name": "审批"}, {"id": "delete", "name": "删除"}}
