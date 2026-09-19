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
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Environment string `json:"environment"`
}
type Message struct {
	ID          string    `json:"id"`
	Role        string    `json:"role"`
	Content     string    `json:"content"`
	CreatedAt   time.Time `json:"created_at"`
	ModelID     string    `json:"model_id,omitempty"`
	EvidenceIDs []string  `json:"evidence_ids,omitempty"`
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
