package core

import "time"

type User struct {
	ID       string `json:"id"`
	Email    string `json:"email"`
	Name     string `json:"name"`
	Role     string `json:"role"`
	Disabled bool   `json:"disabled"`
}
type Application struct {
	ID             string             `json:"id"`
	Name           string             `json:"name"`
	Description    string             `json:"description"`
	Environment    string             `json:"environment"`
	Key            string             `json:"key"`
	OwnerTeamID    string             `json:"owner_team_id"`
	MaintainerID   string             `json:"maintainer_id"`
	Tags           []string           `json:"tags"`
	RepositoryURL  string             `json:"repository_url"`
	RunbookURL     string             `json:"runbook_url"`
	Archived       bool               `json:"archived"`
	Environments   []Environment      `json:"environments"`
	Profile        ApplicationProfile `json:"profile"`
	MaintainerName string             `json:"maintainer_name"`
	OwnerTeamName  string             `json:"owner_team_name"`
}

// ApplicationProfile is catalog metadata, never an executable delivery definition.
type ApplicationProfile struct {
	BusinessUnit     string `json:"business_unit"`
	ServiceType      string `json:"service_type"`
	Lifecycle        string `json:"lifecycle"`
	Criticality      string `json:"criticality"`
	Language         string `json:"language"`
	Framework        string `json:"framework"`
	DefaultBranch    string `json:"default_branch"`
	BuildCommand     string `json:"build_command"`
	ImageRepository  string `json:"image_repository"`
	DocumentationURL string `json:"documentation_url"`
}

// EnvironmentConfiguration records intended settings. No connector applies these values.
// Nil fields distinguish unregistered settings from an explicit zero.
type EnvironmentConfiguration struct {
	Replicas              *int `json:"replicas"`
	CPURequestMillicores  *int `json:"cpu_request_millicores"`
	CPULimitMillicores    *int `json:"cpu_limit_millicores"`
	MemoryRequestMiB      *int `json:"memory_request_mib"`
	MemoryLimitMiB        *int `json:"memory_limit_mib"`
	Port                  *int `json:"port"`
	ConnectionPoolMax     *int `json:"connection_pool_max"`
	RequestTimeoutMS      *int `json:"request_timeout_ms"`
	RateLimitRPS          *int `json:"rate_limit_rps"`
	Retries               *int `json:"retries"`
	CircuitBreakerPercent *int `json:"circuit_breaker_percent"`
}
type Message struct {
	ID          string    `json:"id"`
	Role        string    `json:"role"`
	Content     string    `json:"content"`
	CreatedAt   time.Time `json:"created_at"`
	ModelID     string    `json:"model_id,omitempty"`
	EvidenceIDs []string  `json:"evidence_ids,omitempty"`
	RunID       string    `json:"run_id,omitempty"`
}

// RunStep is one visible model call or read-only tool call inside an Attempt.
type RunStep struct {
	ID           string     `json:"id"`
	RunID        string     `json:"run_id"`
	AttemptID    string     `json:"attempt_id"`
	Seq          int        `json:"seq"`
	Kind         string     `json:"kind"`
	Name         string     `json:"name"`
	Status       string     `json:"status"`
	Input        string     `json:"input,omitempty"`
	Output       string     `json:"output,omitempty"`
	Error        string     `json:"error,omitempty"`
	EvidenceID   string     `json:"evidence_id,omitempty"`
	InputTokens  *int       `json:"input_tokens,omitempty"`
	OutputTokens *int       `json:"output_tokens,omitempty"`
	ToolCalls    []string   `json:"tool_calls,omitempty"`
	TraceID      string     `json:"trace_id,omitempty"`
	StartedAt    time.Time  `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
}
type Branch struct {
	ID                      string    `json:"id"`
	InvestigationID         string    `json:"investigation_id"`
	ParentID                string    `json:"parent_id,omitempty"`
	InheritedParentRevision *int64    `json:"inherited_parent_revision,omitempty"`
	SyncedParentRevision    *int64    `json:"synced_parent_revision,omitempty"`
	CurrentParentRevision   *int64    `json:"current_parent_revision,omitempty"`
	ParentHasUpdates        bool      `json:"parent_has_updates"`
	Title                   string    `json:"title"`
	ModelID                 string    `json:"model_id"`
	Revision                int64     `json:"revision"`
	Status                  string    `json:"status"`
	Messages                []Message `json:"messages"`
}
type Evidence struct {
	ID        string    `json:"id"`
	BranchID  string    `json:"branch_id"`
	Kind      string    `json:"kind"`
	Title     string    `json:"title"`
	Content   string    `json:"content"`
	Source    string    `json:"source"`
	CreatedAt time.Time `json:"created_at"`
}
type Run struct {
	ID        string    `json:"id"`
	BranchID  string    `json:"branch_id"`
	Status    string    `json:"status"`
	ModelID   string    `json:"model_id"`
	Attempt   int       `json:"attempt"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}
type Investigation struct {
	ID            string     `json:"id"`
	ApplicationID string     `json:"application_id"`
	EnvironmentID string     `json:"environment_id"`
	Title         string     `json:"title"`
	Status        string     `json:"status"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	Branches      []Branch   `json:"branches"`
	Evidence      []Evidence `json:"evidence"`
	Runs          []Run      `json:"runs"`
}
type Preview struct {
	ID              string          `json:"id"`
	SourceBranchID  string          `json:"source_branch_id"`
	TargetBranchID  string          `json:"target_branch_id"`
	SourceRevision  int64           `json:"source_revision"`
	TargetRevision  int64           `json:"target_revision"`
	Additions       []Message       `json:"additions"`
	Conflicts       []string        `json:"conflicts"`
	Evidence        []Evidence      `json:"evidence"`
	ConflictDetails []MergeConflict `json:"conflict_details"`
}
type Event struct {
	Seq             int64     `json:"seq"`
	Type            string    `json:"type"`
	InvestigationID string    `json:"investigation_id"`
	BranchID        string    `json:"branch_id,omitempty"`
	Payload         any       `json:"payload"`
	CreatedAt       time.Time `json:"created_at"`
}

// RevisionProvenance records why a new immutable snapshot was created; it does
// not represent approval or grant any execution capability.
type RevisionProvenance struct {
	Kind           string   `json:"kind"`
	SourceBranchID string   `json:"source_branch_id,omitempty"`
	SourceRevision *int64   `json:"source_revision,omitempty"`
	PreviewID      string   `json:"preview_id,omitempty"`
	MergeMode      string   `json:"merge_mode,omitempty"`
	MessageIDs     []string `json:"message_ids,omitempty"`
}
type ContextRevision struct {
	BranchID     string             `json:"branch_id"`
	Revision     int64              `json:"revision"`
	CreatedAt    time.Time          `json:"created_at"`
	MessageCount int                `json:"message_count"`
	Provenance   RevisionProvenance `json:"provenance"`
	Messages     []Message          `json:"messages,omitempty"`
	Evidence     []Evidence         `json:"evidence,omitempty"`
}
type MergeConflict struct {
	Kind             string   `json:"kind"`
	Review           string   `json:"review"`
	Message          string   `json:"message"`
	SourceMessageIDs []string `json:"source_message_ids"`
}
type MergeOptions struct {
	MessageIDs []string `json:"message_ids,omitempty"`
	Mode       string   `json:"mode,omitempty"`
}

// Environment keeps resource bindings separate from application ownership.
type Environment struct {
	ID                    string                    `json:"id"`
	ApplicationID         string                    `json:"application_id"`
	Key                   string                    `json:"key"`
	Name                  string                    `json:"name"`
	Archived              bool                      `json:"archived"`
	BindingRevision       int64                     `json:"binding_revision"`
	Bindings              []Binding                 `json:"bindings"`
	Capabilities          []string                  `json:"capabilities"`
	Configuration         *EnvironmentConfiguration `json:"configuration"`
	ConfigurationRevision int64                     `json:"configuration_revision"`
}
type Binding struct {
	Kind        string `json:"kind"`
	ConnectorID string `json:"connector_id"`
	Resource    string `json:"resource"`
}
type Team struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}
type Grant struct {
	SubjectType string `json:"subject_type"`
	SubjectID   string `json:"subject_id"`
	Permission  string `json:"permission"`
}
