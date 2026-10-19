package v3ingestpush

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"time"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/delivery"
	"github.com/destinywans/redstring/internal/operations"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
)

// SetNativeWebhookSources adds metadata from receivers that are already
// installed by operations and delivery. Call it before Register and do not
// mutate the supplied configurations afterwards. It does not read secrets,
// provision remote hooks, or enable outbound delivery.
func (s *Service) SetNativeWebhookSources(alerts []operations.Source, gitlabs []delivery.GitLabConfig) error {
	if s == nil {
		return ErrInvalidConfiguration
	}
	alertIDs := map[string]bool{}
	for _, source := range alerts {
		if source.ID == "" || alertIDs[source.ID] {
			return ErrInvalidConfiguration
		}
		alertIDs[source.ID] = true
		for _, mapping := range source.Mappings {
			if mapping.ApplicationID == "" || mapping.EnvironmentID == "" {
				return ErrInvalidConfiguration
			}
		}
	}
	gitlabIDs := map[string]bool{}
	for _, source := range gitlabs {
		if source.ID == "" || gitlabIDs[source.ID] {
			return ErrInvalidConfiguration
		}
		gitlabIDs[source.ID] = true
		for _, binding := range source.Bindings {
			if binding.ApplicationID == "" || binding.EnvironmentID == "" || binding.ProjectID <= 0 {
				return ErrInvalidConfiguration
			}
		}
	}
	s.nativeAlerts = append([]operations.Source(nil), alerts...)
	s.nativeGitLabs = append([]delivery.GitLabConfig(nil), gitlabs...)
	return nil
}

func nativeWebhookPathID(id string) string {
	// PathEscape leaves dot segments unchanged, while ServeMux normalizes
	// literal "." and ".." before route matching.
	switch id {
	case ".":
		return "%2E"
	case "..":
		return "%2E%2E"
	default:
		return url.PathEscape(id)
	}
}

func (s *Service) nativeWebhookView(ctx context.Context, tx pgx.Tx, user core.User, envs []string) ([]webhookInbound, error) {
	items := []webhookInbound{}
	cutoff := time.Now().UTC().Add(-24 * time.Hour)
	for _, source := range s.nativeAlerts {
		seen := map[string]bool{}
		var count int64
		var last *time.Time
		visible := false
		for _, mapping := range source.Mappings {
			key := mapping.ApplicationID + "\x00" + mapping.EnvironmentID
			if seen[key] {
				continue
			}
			seen[key] = true
			allowed, err := s.nativeVisible(ctx, tx, user, envs, mapping.ApplicationID, mapping.EnvironmentID)
			if err != nil {
				return nil, err
			}
			if !allowed {
				continue
			}
			visible = true
			var n int64
			var newest *time.Time
			err = tx.QueryRow(ctx, `SELECT count(*),max(o.received_at)
			 FROM ops_incidents i JOIN ops_alert_occurrences o ON o.incident_id=i.id
			 WHERE i.source_id=$1 AND i.application_id=$2 AND i.environment_id=$3 AND o.received_at >= $4`,
				source.ID, mapping.ApplicationID, mapping.EnvironmentID, cutoff).Scan(&n, &newest)
			if err != nil {
				return nil, err
			}
			count += n
			if newest != nil && (last == nil || newest.After(*last)) {
				last = newest
			}
		}
		if !visible {
			continue
		}
		item := webhookInbound{ID: "alertmanager:" + source.ID, SourceID: source.ID, EventType: "alert",
			Brand: "prometheus", Name: source.ID, URL: "/api/v1/webhooks/alertmanager/" + nativeWebhookPathID(source.ID),
			Verify: "Bearer", Count: strconv.FormatInt(count, 10),
			Status: "unverified", StatusBasis: "health_unverified"}
		if last != nil {
			lastAt, lastCode := last.UTC().Format(time.RFC3339), 202
			item.LastAt, item.LastCode = &lastAt, &lastCode
		}
		items = append(items, item)
	}
	for _, source := range s.nativeGitLabs {
		visible := false
		for _, binding := range source.Bindings {
			allowed, err := s.nativeVisible(ctx, tx, user, envs, binding.ApplicationID, binding.EnvironmentID)
			if err != nil {
				return nil, err
			}
			if allowed {
				visible = true
				break
			}
		}
		if !visible {
			continue
		}
		name := source.Name
		if name == "" {
			name = source.ID
		}
		// delivery_webhook_inbox only persists source and project. A changed
		// project binding could expose receipts from a former application, so
		// historical counts cannot safely be attributed to this current scope.
		items = append(items, webhookInbound{ID: "gitlab:" + source.ID, SourceID: source.ID, EventType: "pipeline",
			Brand: "gitlab", Name: name, URL: "/api/v1/webhooks/gitlab/" + nativeWebhookPathID(source.ID),
			Verify: "X-Gitlab-Token", Count: "unknown",
			Status: "unverified", StatusBasis: "historical_receipt_scope_unavailable"})
	}
	return items, nil
}

func (s *Service) nativeVisible(ctx context.Context, tx pgx.Tx, user core.User, envs []string, app, environmentID string) (bool, error) {
	var env, team string
	err := tx.QueryRow(ctx, `SELECT e.key,COALESCE(a.owner_team_id,'') FROM application_environments e
	 JOIN applications a ON a.id=e.application_id AND NOT a.archived
	 JOIN v3_application_environments ve ON ve.application_id=e.application_id AND ve.environment_id=e.key
	 WHERE e.id=$1 AND e.application_id=$2 AND NOT e.archived AND e.key=ANY($3::text[])
	 FOR SHARE OF e,a,ve`, environmentID, app, envs).Scan(&env, &team)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	decision := rbac.V3Decision{Module: "services", Action: "view", EnvironmentID: env, ApplicationID: app}
	err = s.policy.AuthorizeWithTx(ctx, tx, user, decision)
	if errors.Is(err, store.ErrForbidden) && team != "" {
		decision.ApplicationID, decision.TeamID = "", team
		err = s.policy.AuthorizeWithTx(ctx, tx, user, decision)
	}
	if errors.Is(err, store.ErrForbidden) {
		return false, nil
	}
	return err == nil, err
}
