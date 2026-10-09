package v3alertmanager

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// AlertState is the reconciliation view of one upstream alert (PA-47): its
// identity and whether Alertmanager currently suppresses it. Labels,
// annotations and silence IDs are not returned.
type AlertState struct {
	Fingerprint string
	StartsAt    time.Time
	// State is Alertmanager's own: active, suppressed or unprocessed.
	State     string
	Silenced  bool
	Inhibited bool
}

// ListAlertStates reads GET /api/v2/alerts for one environment. Any record
// without a fingerprint, start time or known state makes the whole read
// invalid, so a caller never reconciles against a partial upstream view.
func (c *Client) ListAlertStates(ctx context.Context, environment string) ([]AlertState, error) {
	body, err := c.get(ctx, environment, "/api/v2/alerts")
	if err != nil {
		return nil, err
	}
	var alerts []struct {
		Fingerprint string    `json:"fingerprint"`
		StartsAt    time.Time `json:"startsAt"`
		Status      struct {
			State       string   `json:"state"`
			SilencedBy  []string `json:"silencedBy"`
			InhibitedBy []string `json:"inhibitedBy"`
		} `json:"status"`
	}
	if err := decodeOne(body, &alerts); err != nil {
		return nil, fmt.Errorf("invalid Alertmanager alerts: %w", err)
	}
	if alerts == nil {
		return nil, errors.New("invalid Alertmanager alerts: expected an array")
	}
	out := make([]AlertState, 0, len(alerts))
	for _, alert := range alerts {
		state := alert.Status.State
		if alert.Fingerprint == "" || len(alert.Fingerprint) > 256 || alert.StartsAt.IsZero() ||
			(state != "active" && state != "suppressed" && state != "unprocessed") {
			return nil, errors.New("invalid Alertmanager alert record")
		}
		out = append(out, AlertState{Fingerprint: alert.Fingerprint, StartsAt: alert.StartsAt, State: state,
			Silenced: len(alert.Status.SilencedBy) > 0, Inhibited: len(alert.Status.InhibitedBy) > 0})
	}
	return out, nil
}
