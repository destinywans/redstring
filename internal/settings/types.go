// Package settings projects existing identity and application ACLs into a workspace.
package settings

import (
	"errors"
	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/store"
	"time"
)

var ErrInvalid = errors.New("invalid settings")

type Workspace struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Description    string    `json:"description"`
	OwnerID        string    `json:"owner_id"`
	OwnerName      string    `json:"owner_name"`
	DeploymentMode string    `json:"deployment_mode"`
	Timezone       string    `json:"timezone"`
	Language       string    `json:"language"`
	Revision       int64     `json:"revision"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}
type WorkspacePatch struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Timezone    *string `json:"timezone"`
	Language    *string `json:"language"`
	Revision    int64   `json:"revision"`
}
type IntegrationSummary struct {
	Kind       string `json:"kind"`
	Total      int    `json:"total"`
	Configured int    `json:"configured"`
	State      string `json:"state"`
}
type Member struct {
	store.Member
	IsMock    bool       `json:"is_mock"`
	TeamIDs   []string   `json:"team_ids"`
	CreatedAt *time.Time `json:"created_at"`
}
type Application struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Key  string `json:"key"`
}
type TeamApplication struct {
	Application
	Permission   string `json:"permission"`
	Relationship string `json:"relationship"`
}
type TeamProfile struct {
	OwnerID    string     `json:"owner_id"`
	OwnerName  string     `json:"owner_name"`
	Department string     `json:"department"`
	IsMock     bool       `json:"is_mock"`
	CreatedAt  *time.Time `json:"created_at"`
	UpdatedAt  *time.Time `json:"updated_at"`
}
type Team struct {
	core.Team
	TeamProfile
	MemberIDs    []string          `json:"member_ids"`
	Applications []TeamApplication `json:"applications"`
}
type TeamProfilePatch struct {
	OwnerID    *string `json:"owner_id"`
	Department *string `json:"department"`
}
type Role struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	MemberCount int             `json:"member_count"`
	Permissions map[string]bool `json:"permissions"`
	Scope       string          `json:"scope"`
}
type Department struct {
	Name        string `json:"name"`
	MemberCount int    `json:"member_count"`
	TeamCount   int    `json:"team_count"`
}
type Grant struct {
	core.Grant
	ApplicationID string `json:"application_id"`
}
type Directory struct {
	Members      []Member      `json:"members"`
	Teams        []Team        `json:"teams"`
	Roles        []Role        `json:"roles"`
	Departments  []Department  `json:"departments"`
	Applications []Application `json:"applications"`
	Grants       []Grant       `json:"grants"`
	MockNotice   string        `json:"mock_notice"`
}

func Roles() []Role {
	return []Role{
		{ID: "admin", Name: "管理员", Description: "管理平台、成员与授权；可调查、审批和执行已批准动作。", Permissions: map[string]bool{"view": true, "investigate": true, "approve": true, "execute": true, "manage": true}, Scope: "全部应用；执行仍需审批与执行器校验"},
		{ID: "operator", Name: "运维人员", Description: "在获授应用范围内调查、审批和执行已批准动作。", Permissions: map[string]bool{"view": true, "investigate": true, "approve": true, "execute": true, "manage": false}, Scope: "应用 view / operate 授权；写操作需 operate，执行需已批准提案"},
		{ID: "viewer", Name: "只读成员", Description: "在获授应用范围内查看与执行有界只读查询。", Permissions: map[string]bool{"view": true, "investigate": false, "approve": false, "execute": false, "manage": false}, Scope: "获授应用只读；operate 授权不提升平台角色上限"},
	}
}
