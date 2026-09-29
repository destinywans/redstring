package v3alertmanager

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// InhibitionHistorySource is a trusted, read-only history provider. A live
// Alertmanager alert snapshot does not implement this interface: it cannot
// establish complete seven-day coverage.
type InhibitionHistorySource interface {
	ReadInhibitionHistory(context.Context, string, time.Time, time.Time) (InhibitionHistory, error)
}

type InhibitionHistory struct {
	EnvironmentID string                   `json:"environment_id"`
	SourceID      string                   `json:"source_id"`
	Complete      bool                     `json:"complete"`
	CoverageStart time.Time                `json:"coverage_start"`
	CoverageEnd   time.Time                `json:"coverage_end"`
	Alerts        []InhibitionHistoryAlert `json:"alerts"`
}

type InhibitionHistoryAlert struct {
	ID             string            `json:"id"`
	Labels         map[string]string `json:"labels"`
	StartsAt       time.Time         `json:"starts_at"`
	EndsAt         *time.Time        `json:"ends_at"`
	FiringInterval bool              `json:"firing_interval"`
}

// HistoryEndpoint is operator-owned configuration for a provider that
// explicitly attests complete occurrence coverage. The source_id binds the
// response to the selected environment's configured ingestion stream.
type HistoryEndpoint struct {
	Endpoint
	SourceID string
}

type InhibitionHistoryClient struct {
	client  *Client
	sources map[string]string
}

// NewInhibitionHistoryClient reuses the bounded HTTPS, bearer-file, timeout,
// and no-redirect transport used by the Alertmanager reader. It only sends GET.
func NewInhibitionHistoryClient(endpoints map[string]HistoryEndpoint, httpClient *http.Client) (*InhibitionHistoryClient, error) {
	readerEndpoints := make(map[string]Endpoint, len(endpoints))
	sources := make(map[string]string, len(endpoints))
	for env, endpoint := range endpoints {
		if endpoint.SourceID == "" || len(endpoint.SourceID) > 128 || strings.TrimSpace(endpoint.SourceID) != endpoint.SourceID {
			return nil, errors.New("invalid inhibition history source binding")
		}
		readerEndpoints[env] = endpoint.Endpoint
		sources[env] = endpoint.SourceID
	}
	reader, err := New(readerEndpoints, httpClient)
	if err != nil {
		return nil, err
	}
	return &InhibitionHistoryClient{client: reader, sources: sources}, nil
}

func (c *InhibitionHistoryClient) ReadInhibitionHistory(ctx context.Context, env string, from, through time.Time) (InhibitionHistory, error) {
	if c == nil || c.client == nil || c.sources[env] == "" {
		return InhibitionHistory{}, ErrNotConfigured
	}
	if from.IsZero() || through.IsZero() || !from.Before(through) || through.Sub(from) != 7*24*time.Hour {
		return InhibitionHistory{}, errors.New("invalid inhibition history window")
	}
	query := url.Values{"from": {from.UTC().Format(time.RFC3339Nano)}, "through": {through.UTC().Format(time.RFC3339Nano)}}
	body, err := c.client.get(ctx, env, "/api/v1/inhibition-history?"+query.Encode())
	if err != nil {
		return InhibitionHistory{}, err
	}
	var history InhibitionHistory
	if decodeOne(body, &history) != nil || history.EnvironmentID != env || history.SourceID != c.sources[env] ||
		!history.Complete || history.CoverageStart.IsZero() || history.CoverageEnd.IsZero() ||
		!history.CoverageStart.Before(history.CoverageEnd) ||
		history.CoverageStart.After(from) || history.CoverageEnd.Before(through) ||
		history.Alerts == nil || len(history.Alerts) > maxRecords {
		return InhibitionHistory{}, errors.New("inhibition history coverage is incomplete")
	}
	seen := make(map[string]bool, len(history.Alerts))
	for _, alert := range history.Alerts {
		if alert.ID == "" || len(alert.ID) > 256 || seen[alert.ID] || !alert.FiringInterval || alert.StartsAt.IsZero() ||
			alert.StartsAt.After(through) || len(alert.Labels) == 0 || len(alert.Labels) > 100 ||
			alert.EndsAt != nil && !alert.EndsAt.After(alert.StartsAt) {
			return InhibitionHistory{}, errors.New("invalid inhibition history occurrence")
		}
		seen[alert.ID] = true
		for name, value := range alert.Labels {
			if !alertLabelName.MatchString(name) || len(value) > 256 || strings.ContainsAny(value, "\x00\r\n") {
				return InhibitionHistory{}, errors.New("invalid inhibition history labels")
			}
		}
	}
	return history, nil
}
