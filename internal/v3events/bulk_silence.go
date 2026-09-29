package v3events

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3alertmanager"
	"github.com/wans/aiops-canvas/internal/v3proposals"
)

type bulkSilencePlan struct {
	InvestigationID  string
	EnvironmentID    string
	ApplicationLabel string
	AlertName        string
	Key              string
}

func (b *BulkService) applicationLabel(env, app string) (string, error) {
	if b.Silence == nil || b.Silence.Proposals == nil || b.Silence.Ledger == nil ||
		b.Silence.Client == nil || b.Silence.Proposals.DB != b.DB || b.Silence.Ledger.DB != b.DB {
		return "", errBulkNotConnected
	}
	label := ""
	for candidate, boundApp := range b.Silence.ApplicationBindings[env] {
		if boundApp != app {
			continue
		}
		if label != "" {
			return "", errBulkNotConnected // Ambiguous provider scope.
		}
		label = candidate
	}
	if label == "" {
		return "", errBulkNotConnected
	}
	return label, nil
}

// The group rows retain source identity, not the original webhook labels.
// Require the existing operator-attested V3 ingress source and a current
// mapping that forces the exact labels used by the outbound proposal. This
// excludes synthetic patrol incidents and arbitrary webhook mappings.
func (b *BulkService) attestedSilenceSource(ctx context.Context, env string,
	key groupKey, group lockedBulkGroup, appLabel string, oldest time.Time) (string, error) {
	if b.Silence.AlertRangeCovered == nil || oldest.IsZero() {
		return "", errBulkNotConnected
	}
	coverageCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	sourceID := b.Silence.AlertRangeCovered(coverageCtx, key.ApplicationID, env, oldest, time.Now().UTC())
	if coverageCtx.Err() != nil {
		return "", errBulkNotConnected
	}
	if sourceID == "" || sourceID == "ai-patrol" {
		return "", errBulkNotConnected
	}
	envLabel := b.Silence.EnvironmentLabels[env]
	if envLabel == "" || envLabel == "application" ||
		b.Silence.Ledger.EnvironmentLabel(env) != envLabel {
		return "", errBulkNotConnected
	}
	sources := 0
	selected := 0
	for _, source := range b.Silence.IngressSources {
		if source.ID != sourceID {
			continue
		}
		sources++
		if source.TokenFile == "" {
			return "", errBulkNotConnected
		}
		for _, mapping := range source.Mappings {
			if mapping.ApplicationID != key.ApplicationID || mapping.EnvironmentID != group.EnvironmentID {
				continue
			}
			selected++
			if mapping.Labels["application"] != appLabel || mapping.Labels[envLabel] != env ||
				mapping.Labels["alertname"] != key.Title {
				return "", errBulkNotConnected
			}
		}
	}
	if sources != 1 || selected != 1 {
		return "", errBulkNotConnected
	}
	return sourceID, nil
}

// prepareBulkSilence only derives a fixed one-hour pending proposal from
// active, linked, persisted alert members. It performs no upstream operation.
func (b *BulkService) prepareBulkSilence(ctx context.Context, tx pgx.Tx, actor core.User,
	id, env string, key groupKey, group lockedBulkGroup) (bulkSilencePlan, error) {
	var plan bulkSilencePlan
	appLabel, err := b.applicationLabel(env, key.ApplicationID)
	if err != nil {
		return plan, err
	}
	// Ingest derives title from alertname but truncates it at 300 runes. The
	// proposal must use an exact provider matcher; a fallback or truncated
	// title is not sufficient evidence for an exact alert-group target.
	if key.Title == "" || key.Title == "Alert" || len([]byte(key.Title)) > 256 {
		return plan, errBulkNotConnected
	}
	active := 0
	linked := ""
	oldest := time.Time{}
	for _, member := range group.Members {
		if !activeClaimMember(member) {
			continue
		}
		active++
		if member.Suspected || member.SourceID == "" ||
			member.Fingerprint == "" || member.StartsAt.IsZero() {
			return plan, errBulkNotConnected
		}
		if oldest.IsZero() || member.StartsAt.Before(oldest) {
			oldest = member.StartsAt
		}
		if member.InvestigationID == "" {
			return plan, errInvestigationRequired
		}
		if linked != "" && linked != member.InvestigationID {
			return plan, store.ErrConflict
		}
		linked = member.InvestigationID
	}
	if active == 0 {
		return plan, store.ErrConflict
	}
	sourceID, err := b.attestedSilenceSource(ctx, env, key, group, appLabel, oldest)
	if err != nil {
		return plan, err
	}
	for _, member := range group.Members {
		if activeClaimMember(member) && member.SourceID != sourceID {
			return plan, errBulkNotConnected
		}
	}
	var status string
	err = tx.QueryRow(ctx, `SELECT status FROM investigations
 WHERE id=$1 AND application_id=$2 AND environment_id=$3 FOR SHARE`,
		linked, key.ApplicationID, group.EnvironmentID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && status != "open" {
		return plan, errInvestigationRequired
	}
	if err != nil {
		return plan, err
	}
	plan = bulkSilencePlan{InvestigationID: linked, EnvironmentID: env, ApplicationLabel: appLabel,
		AlertName: key.Title,
		Key:       "bulk-" + store.Hash("v3.bulk.silence.v1\x00"+actor.ID+"\x00"+id+"\x00"+linked)}
	return plan, nil
}

func (b *BulkService) createBulkSilenceProposal(ctx context.Context, actor core.User,
	id string, matched int, plan bulkSilencePlan) bulkResult {
	result, err := b.Silence.Proposals.CreateSilenceProposal(ctx, actor,
		v3proposals.SilenceProposalRequest{InvestigationID: plan.InvestigationID,
			Silence: v3alertmanager.SilenceCreateInput{
				EnvironmentID:  plan.EnvironmentID,
				IdempotencyKey: plan.Key,
				Comment:        "AIOps Canvas alert group " + id + "; one-hour silence requested pending approval",
				Matchers: []v3alertmanager.Matcher{
					{Name: "application", Value: plan.ApplicationLabel, IsEqual: true},
					{Name: "alertname", Value: plan.AlertName, IsEqual: true},
				},
				Duration: time.Hour,
			}}, b.Silence.Ledger, b.Silence.Client)
	if err != nil {
		if errors.Is(err, v3alertmanager.ErrSilenceCreateInvalid) || errors.Is(err, v3proposals.ErrInvalidProposal) {
			return bulkFailed(id, "not_connected", matched)
		}
		return safeBulkError(id, matched, err)
	}
	if result.Status != "pending" {
		failed := bulkFailed(id, "conflict", matched)
		failed.ProposalID = &result.ProposalID
		return failed
	}
	// No alert state changed; this result only records a pending review item.
	out := bulkSucceeded(id, "pending_approval", matched, 0)
	out.ProposalID = &result.ProposalID
	out.InvestigationID = &plan.InvestigationID
	return out
}
