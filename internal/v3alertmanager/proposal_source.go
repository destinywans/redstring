package v3alertmanager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
)

const (
	ProposedSilenceCreateVersion = 1
	ProposedSilenceCreateKind    = "alertmanager.silence.create"
)

// ProposedSilenceCreate is trusted, time-independent action input for a future
// approval flow. It is not an approved action or an Alertmanager request.
// The caller must persist it and its digest atomically with the proposal.
type ProposedSilenceCreate struct {
	SchemaVersion      int       `json:"schema_version"`
	Kind               string    `json:"kind"`
	ActorID            string    `json:"actor_id"`
	EnvironmentID      string    `json:"environment_id"`
	EnvironmentLabel   string    `json:"environment_label"`
	ApplicationID      string    `json:"application_id"`
	ApplicationLabel   string    `json:"application_label"`
	OwnerTeamID        string    `json:"owner_team_id"`
	IdempotencyKey     string    `json:"idempotency_key"`
	Matchers           []Matcher `json:"matchers"`
	DurationSeconds    int64     `json:"duration_seconds"`
	Comment            string    `json:"comment"`
	BindingFingerprint string    `json:"binding_fingerprint"`
}

type silenceProposalBinding struct {
	Domain           string `json:"domain"`
	EndpointURL      string `json:"endpointUrl"`
	BearerTokenFile  string `json:"bearerTokenFile"`
	AllowHTTP        bool   `json:"allowHttp"`
	EnvironmentID    string `json:"environmentId"`
	EnvironmentLabel string `json:"environmentLabel"`
	ApplicationLabel string `json:"applicationLabel"`
	ApplicationID    string `json:"applicationId"`
}

func silenceProposalBindingFingerprint(ep Endpoint, envID, envLabel, appLabel, appID string) (string, error) {
	if envID == "" || envLabel == "" || appLabel == "" || appID == "" {
		return "", ErrSilenceCreateInvalid
	}
	// Client.New owns endpoint validation. Recheck here because an endpoint
	// configuration may have changed since the Client was constructed.
	if _, err := New(map[string]Endpoint{envID: ep}, nil); err != nil {
		return "", ErrSilenceCreateInvalid
	}
	value := silenceProposalBinding{
		Domain:           "aiops.v3.alertmanager.silence.create.binding.v1",
		EndpointURL:      strings.TrimRight(ep.URL, "/"),
		BearerTokenFile:  ep.BearerTokenFile,
		AllowHTTP:        ep.AllowHTTP,
		EnvironmentID:    envID,
		EnvironmentLabel: envLabel,
		ApplicationLabel: appLabel,
		ApplicationID:    appID,
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// PrepareCreateProposalWithTx validates a specific application-scoped action
// against current identity, catalog binding, and RBAC in the caller's tx. It
// performs no reservation and no Alertmanager network request.
func (l *DatabaseSilenceLedger) PrepareCreateProposalWithTx(ctx context.Context, tx pgx.Tx,
	actor core.User, in SilenceCreateInput, client *Client) (ProposedSilenceCreate, error) {
	var zero ProposedSilenceCreate
	if tx == nil || client == nil {
		return zero, ErrSilenceCreateInvalid
	}
	matchers, appLabel, envLabel, err := l.prepare(in)
	if err != nil {
		return zero, err
	}
	if appLabel == "" {
		return zero, ErrSilenceCreateInvalid
	}
	ep, ok := client.endpoints[in.EnvironmentID]
	if !ok {
		return zero, ErrNotConfigured
	}
	appID, teamID, err := l.authorizeScope(ctx, tx, actor, in.EnvironmentID, appLabel, "")
	if err != nil {
		return zero, err
	}
	fingerprint, err := silenceProposalBindingFingerprint(ep, in.EnvironmentID, envLabel, appLabel, appID)
	if err != nil {
		return zero, err
	}
	return ProposedSilenceCreate{
		SchemaVersion: ProposedSilenceCreateVersion, Kind: ProposedSilenceCreateKind,
		ActorID: actor.ID, EnvironmentID: in.EnvironmentID, EnvironmentLabel: envLabel,
		ApplicationID: appID, ApplicationLabel: appLabel, OwnerTeamID: teamID,
		IdempotencyKey: in.IdempotencyKey, Matchers: matchers,
		DurationSeconds: int64(in.Duration.Seconds()), Comment: in.Comment,
		BindingFingerprint: fingerprint,
	}, nil
}

// RevalidateCreateProposalWithTx checks the stored action against current
// catalog/RBAC and server-owned connector configuration before future use.
// Approval and execution must still be enforced by their own durable gates.
func (l *DatabaseSilenceLedger) RevalidateCreateProposalWithTx(ctx context.Context, tx pgx.Tx,
	actor core.User, action ProposedSilenceCreate, client *Client) error {
	if action.ActorID == "" || action.ActorID != actor.ID ||
		action.SchemaVersion != ProposedSilenceCreateVersion || action.Kind != ProposedSilenceCreateKind ||
		action.DurationSeconds < int64((30*time.Minute)/time.Second) ||
		action.DurationSeconds > int64((24*time.Hour)/time.Second) {
		return ErrSilenceCreateInvalid
	}
	current, err := l.PrepareCreateProposalWithTx(ctx, tx, actor, SilenceCreateInput{
		EnvironmentID:  action.EnvironmentID,
		IdempotencyKey: action.IdempotencyKey,
		Comment:        action.Comment,
		Matchers:       action.Matchers,
		Duration:       time.Duration(action.DurationSeconds) * time.Second,
	}, client)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current, action) {
		return ErrSilenceCreateConflict
	}
	return nil
}
