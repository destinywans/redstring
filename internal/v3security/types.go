// Package v3security exposes only security state confirmed by an effective
// runtime authority. A persisted preference is not evidence of enforcement.
package v3security

import (
	"context"
	"errors"
	"time"
)

var ErrAuthorityUnavailable = errors.New("security runtime authority unavailable")
var ErrRuntimeConflict = errors.New("security runtime revision conflict")
var ErrRuntimeRejected = errors.New("security runtime rejected policy")

// SecurityView is the frozen V3 security DTO. Even read-only fields must come
// from the authority; a missing value cannot be replaced with a zero value.
type SecurityView struct {
	SSO             []SSOProvider     `json:"sso"`
	SCIM            bool              `json:"scim"`
	MFAPolicy       string            `json:"mfa_policy"`
	MFAMethods      []MFAMethod       `json:"mfa_methods"`
	ApprovalPasskey bool              `json:"approval_passkey"`
	Session         SessionPolicyView `json:"session"`
	Vault           VaultView         `json:"vault"`
	TokenTTL        string            `json:"token_ttl"`
	PasswordPolicy  string            `json:"password_policy"`
}

// securityResponse is the GET/PUT body: the authority's view plus an empty
// unavailable map, because every field of a valid snapshot is proven.
type securityResponse struct {
	SecurityView
	Unavailable map[string]string `json:"unavailable"`
}

// unknownSecurityView is returned when no runtime authority is configured.
// Every field is null; nothing local can prove the effective policy.
type unknownSecurityView struct {
	SSO             *struct{}         `json:"sso"`
	SCIM            *bool             `json:"scim"`
	MFAPolicy       *string           `json:"mfa_policy"`
	MFAMethods      *struct{}         `json:"mfa_methods"`
	ApprovalPasskey *bool             `json:"approval_passkey"`
	Session         *struct{}         `json:"session"`
	Vault           *struct{}         `json:"vault"`
	TokenTTL        *string           `json:"token_ttl"`
	PasswordPolicy  *string           `json:"password_policy"`
	Unavailable     map[string]string `json:"unavailable"`
}

func newUnknownSecurityView(reason string) unknownSecurityView {
	unavailable := map[string]string{}
	for _, field := range []string{"sso", "scim", "mfa_policy", "mfa_methods", "approval_passkey", "session", "vault", "token_ttl", "password_policy"} {
		unavailable[field] = reason
	}
	return unknownSecurityView{Unavailable: unavailable}
}

type SSOProvider struct {
	Brand   string `json:"brand"`
	Name    string `json:"name"`
	Sub     string `json:"sub"`
	Enabled bool   `json:"enabled"`
	Default *bool  `json:"default,omitempty"`
}

type MFAMethod struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Enabled bool   `json:"enabled"`
	Note    string `json:"note,omitempty"`
}

type SessionPolicyView struct {
	Idle            string `json:"idle"`
	Max             string `json:"max"`
	IPAllow         string `json:"ip_allow"`
	NewDeviceNotice bool   `json:"new_device_notice"`
}

type VaultView struct {
	Name      string `json:"name"`
	Sub       string `json:"sub"`
	Connected bool   `json:"connected"`
}

// SecurityControls contains only settings editable by the current V3 form.
// An adapter must reject a control it cannot actually enforce.
type SecurityControls struct {
	SCIM            bool               `json:"scim"`
	MFAPolicy       string             `json:"mfa_policy"`
	MFAMethods      []MFAMethodControl `json:"mfa_methods"`
	ApprovalPasskey bool               `json:"approval_passkey"`
	IPAllow         string             `json:"ip_allow"`
	NewDeviceNotice bool               `json:"new_device_notice"`
}

type MFAMethodControl struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
}

type RuntimeScope struct {
	InstanceID    string `json:"instance_id"`
	EnvironmentID string `json:"environment_id"`
}

type RuntimeSnapshot struct {
	InstanceID    string       `json:"instance_id"`
	EnvironmentID string       `json:"environment_id"`
	Revision      int64        `json:"revision"`
	Complete      bool         `json:"complete"`
	Enforced      bool         `json:"enforced"`
	ObservedAt    time.Time    `json:"observed_at"`
	ValidUntil    time.Time    `json:"valid_until"`
	View          SecurityView `json:"view"`
}

type RuntimeApply struct {
	InstanceID       string           `json:"instance_id"`
	EnvironmentID    string           `json:"environment_id"`
	ExpectedRevision int64            `json:"expected_revision"`
	OperationID      string           `json:"operation_id"`
	Controls         SecurityControls `json:"controls"`
}

type RuntimeApplyReceipt struct {
	InstanceID       string `json:"instance_id"`
	EnvironmentID    string `json:"environment_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	OperationID      string `json:"operation_id"`
	ControlsSHA256   string `json:"controls_sha256"`
	Revision         int64  `json:"revision"`
	Durable          bool   `json:"durable"`
	Enforced         bool   `json:"enforced"`
}

// RuntimeController must report the actual applied policy of this instance
// and selected environment. Apply must be idempotent for an operation ID.
type RuntimeController interface {
	Snapshot(context.Context, RuntimeScope) (RuntimeSnapshot, error)
	Apply(context.Context, RuntimeApply) (RuntimeApplyReceipt, error)
}
