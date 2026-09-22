package resources

// GraphExploration embeds the legacy graph fields. Groups are presentation-only
// summaries of authorized resource identities, never persisted configuration items.
type GraphExploration struct {
	Graph
	Mode        string          `json:"mode"`
	RootID      string          `json:"root_id,omitempty"`
	Total       int             `json:"total"`
	Offset      int             `json:"offset"`
	Limit       int             `json:"limit"`
	Remaining   int             `json:"remaining"`
	NextOffset  *int            `json:"next_offset"`
	PageNodeIDs []string        `json:"page_node_ids"`
	Groups      []ResourceGroup `json:"groups"`
	GroupEdges  []GroupRelation `json:"group_edges"`
	Facets      GraphFacets     `json:"facets"`
	Limits      GraphLimits     `json:"limits"`
	Warnings    []string        `json:"warnings"`
}

type graphBudgetError struct{ message string }

func (e *graphBudgetError) Error() string { return e.message }

type ResourceGroup struct {
	ID            string   `json:"id"`
	Label         string   `json:"label"`
	Virtual       bool     `json:"virtual"`
	SourceID      string   `json:"source_id"`
	SourceKind    string   `json:"source_kind"`
	Cluster       string   `json:"cluster"`
	Namespace     string   `json:"namespace"`
	Count         int      `json:"count"`
	ResourceKinds []Option `json:"resource_kinds"`
}

type GroupRelation struct {
	SourceGroupID string `json:"source_group_id"`
	TargetGroupID string `json:"target_group_id"`
	Kind          string `json:"kind"`
	Count         int    `json:"count"`
	Virtual       bool   `json:"virtual"`
}

type GraphFacets struct {
	ResourceKinds []Option `json:"resource_kinds"`
	RelationKinds []Option `json:"relation_kinds"`
}

type GraphLimits struct {
	Resources     int `json:"resources"`
	Relationships int `json:"relationships"`
	Nodes         int `json:"nodes"`
	Edges         int `json:"edges"`
}

type graphOptions struct {
	mode, root, group, direction string
	depth, offset, limit         int
	includeRuntime               bool
	visible                      []string
}
