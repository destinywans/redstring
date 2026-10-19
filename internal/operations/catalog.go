package operations

import (
	"context"
	"sort"
	"time"

	"github.com/destinywans/redstring/internal/core"
)

// Catalog contains presentation metadata only. Never serialize Config itself:
// credential file locations and notification endpoints are not public settings.
type Catalog struct {
	Sources  []CatalogSource  `json:"sources"`
	Channels []CatalogChannel `json:"channels"`
}
type CatalogSource struct {
	ID       string    `json:"id"`
	Type     string    `json:"type"`
	State    string    `json:"state"`
	Mappings []Mapping `json:"mappings"`
}
type CatalogScope struct {
	ApplicationID string `json:"application_id"`
	EnvironmentID string `json:"environment_id"`
}
type CatalogChannel struct {
	ID    string       `json:"id"`
	Type  string       `json:"type"`
	State string       `json:"state"`
	Scope CatalogScope `json:"scope"`
}

func (s *Service) Catalog() Catalog {
	out := Catalog{Sources: []CatalogSource{}, Channels: []CatalogChannel{}}
	for _, source := range s.Config.Sources {
		item := CatalogSource{ID: source.ID, Type: "alertmanager", State: "configured_unverified", Mappings: []Mapping{}}
		for _, mapping := range source.Mappings {
			labels := map[string]string{}
			for key, value := range mapping.Labels {
				labels[key] = value
			}
			item.Mappings = append(item.Mappings, Mapping{Labels: labels, ApplicationID: mapping.ApplicationID, EnvironmentID: mapping.EnvironmentID})
		}
		out.Sources = append(out.Sources, item)
	}
	for _, channel := range s.Config.Channels {
		out.Channels = append(out.Channels, CatalogChannel{ID: channel.ID, Type: "feishu", State: "configured_unverified", Scope: CatalogScope{ApplicationID: channel.ApplicationID, EnvironmentID: channel.EnvironmentID}})
	}
	sort.Slice(out.Sources, func(i, j int) bool { return out.Sources[i].ID < out.Sources[j].ID })
	sort.Slice(out.Channels, func(i, j int) bool { return out.Channels[i].ID < out.Channels[j].ID })
	return out
}

// An alert receipt is an accepted webhook occurrence, not an action audit event
// or a historical snapshot of the incident's effective state. Late firing
// receipts may coexist with a currently resolved incident.
type IncidentEvent struct {
	ID         int64     `json:"id"`
	Status     string    `json:"status"`
	ReceivedAt time.Time `json:"received_at"`
}
type IncidentEvents struct {
	Kind      string          `json:"kind"`
	Items     []IncidentEvent `json:"items"`
	Limit     int             `json:"limit"`
	Truncated bool            `json:"truncated"`
}

func (s *Service) IncidentEvents(ctx context.Context, u core.User, id string) (IncidentEvents, error) {
	out := IncidentEvents{Kind: "alert_receipts", Items: []IncidentEvent{}, Limit: 100}
	v, e := s.incident(ctx, id)
	if e != nil {
		return out, e
	}
	if e = s.incidentAccess(ctx, u, v, "view"); e != nil {
		return out, e
	}
	rows, e := s.DB.Query(ctx, "SELECT id,status,received_at FROM ops_alert_occurrences WHERE incident_id=$1 ORDER BY received_at DESC,id DESC LIMIT $2", id, out.Limit+1)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		var item IncidentEvent
		if e = rows.Scan(&item.ID, &item.Status, &item.ReceivedAt); e != nil {
			return out, e
		}
		if len(out.Items) == out.Limit {
			out.Truncated = true
			break
		}
		out.Items = append(out.Items, item)
	}
	return out, rows.Err()
}
