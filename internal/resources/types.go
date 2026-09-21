package resources

import "time"

type ApplicationLink struct {
	ID              string `json:"id"`
	Key             string `json:"key"`
	Name            string `json:"name"`
	EnvironmentID   string `json:"environment_id"`
	EnvironmentKey  string `json:"environment_key"`
	EnvironmentName string `json:"environment_name"`
}
type Resource struct {
	ID           string            `json:"id"`
	Kind         string            `json:"kind"`
	Name         string            `json:"name"`
	Description  string            `json:"description"`
	ExternalID   string            `json:"external_id"`
	SourceKind   string            `json:"source_kind"`
	SourceID     string            `json:"source_id"`
	OwnerUserID  string            `json:"owner_user_id"`
	OwnerName    string            `json:"owner_name"`
	OwnerTeamID  string            `json:"owner_team_id"`
	TeamName     string            `json:"team_name"`
	Applications []ApplicationLink `json:"applications"`
	Attributes   map[string]any    `json:"attributes"`
	Tags         []string          `json:"tags"`
	IsMock       bool              `json:"is_mock"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
	ObservedAt   *time.Time        `json:"observed_at,omitempty"`
}
type Relation struct {
	ID               string    `json:"id"`
	SourceResourceID string    `json:"source_resource_id"`
	TargetResourceID string    `json:"target_resource_id"`
	Kind             string    `json:"kind"`
	Provenance       string    `json:"provenance"`
	SourceKind       string    `json:"source_kind"`
	SourceID         string    `json:"source_id"`
	Description      string    `json:"description"`
	IsMock           bool      `json:"is_mock"`
	UpdatedAt        time.Time `json:"updated_at"`
}
type Change struct {
	ID          int64     `json:"id"`
	Action      string    `json:"action"`
	Description string    `json:"description"`
	IsMock      bool      `json:"is_mock"`
	CreatedAt   time.Time `json:"created_at"`
}
type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
	Count int    `json:"count"`
}
type Facets struct {
	Kinds        []Option `json:"kinds"`
	Applications []Option `json:"applications"`
	Sources      []Option `json:"sources"`
	Owners       []Option `json:"owners"`
	Teams        []Option `json:"teams"`
}
type Page struct {
	Items    []Resource `json:"items"`
	Total    int        `json:"total"`
	Page     int        `json:"page"`
	PageSize int        `json:"page_size"`
	Facets   Facets     `json:"facets"`
}
type Detail struct {
	Resource         Resource   `json:"resource"`
	RelatedResources []Resource `json:"related_resources"`
	Relations        []Relation `json:"relations"`
	Changes          []Change   `json:"changes"`
}
type Graph struct {
	Nodes     []Resource `json:"nodes"`
	Edges     []Relation `json:"edges"`
	Truncated bool       `json:"truncated"`
}
