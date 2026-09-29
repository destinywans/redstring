package v3serviceviews

import (
	"context"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
)

type relationRow struct{ id, kind, mineResource, otherResource, direction string }

// Only explicit, non-mock depends_on edges between registered service
// resources have a direction and service-level status suitable for this DTO.
// Other visible relation kinds fail closed instead of disappearing as zero
// dependencies. This keeps the displayed count equal to the displayed edges.
func (s *Service) dependencies(ctx context.Context, scope httpapi.V3EnvironmentScope, user core.User, row serviceRow) (dependencySides, []serviceRow, error) {
	out := dependencySides{Upstream: []dependency{}, Downstream: []dependency{}}
	visible, err := s.canView(ctx, user, row, "resources")
	if err != nil {
		return out, nil, err
	}
	if !visible {
		return out, nil, errIncomplete
	}
	rows, err := s.Store.DB.Query(ctx, `WITH mine AS (
 SELECT DISTINCT cra.resource_id FROM cmdb_resource_applications cra
 JOIN application_environments e ON e.id=cra.environment_id AND e.application_id=cra.application_id AND NOT e.archived
 WHERE cra.application_id=$1 AND e.key=$2
 )
 SELECT DISTINCT r.id,r.kind,m.resource_id,
 CASE WHEN m.resource_id=r.source_resource_id THEN r.target_resource_id ELSE r.source_resource_id END,
 CASE WHEN m.resource_id=r.source_resource_id THEN 'downstream' ELSE 'upstream' END
 FROM mine m JOIN cmdb_resource_relations r
 ON r.source_resource_id=m.resource_id OR r.target_resource_id=m.resource_id
 JOIN cmdb_resources own ON own.id=m.resource_id AND NOT own.is_mock
 JOIN cmdb_resources other ON other.id=CASE WHEN m.resource_id=r.source_resource_id THEN r.target_resource_id ELSE r.source_resource_id END AND NOT other.is_mock
 WHERE NOT r.is_mock ORDER BY r.id LIMIT 101`, row.AppID, row.Env)
	if err != nil {
		return out, nil, err
	}
	relations := []relationRow{}
	for rows.Next() {
		var v relationRow
		if err = rows.Scan(&v.id, &v.kind, &v.mineResource, &v.otherResource, &v.direction); err != nil {
			break
		}
		relations = append(relations, v)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return out, nil, err
	}
	if len(relations) > 100 {
		return out, nil, errLimit
	}
	otherRows := []serviceRow{}
	seen := map[string]bool{}
	seenEdges := map[string]bool{}
	statusCache := map[string]struct {
		status string
		seal   metricSourceSeal
	}{}
	for _, rel := range relations {
		if rel.kind != "depends_on" {
			return out, nil, errIncomplete
		}
		mineApp, err := s.resourceOwner(ctx, rel.mineResource, row.Env)
		if err != nil {
			return out, nil, err
		}
		otherApp, err := s.resourceOwner(ctx, rel.otherResource, row.Env)
		if err != nil {
			return out, nil, err
		}
		if mineApp != row.AppID || otherApp == row.AppID {
			return out, nil, errIncomplete
		}
		edgeKey := rel.direction + "\x00" + otherApp
		if seenEdges[edgeKey] {
			continue
		}
		matched, e := s.rows(ctx, user, []string{row.Env}, otherApp, row.Env, filters{})
		if e != nil {
			return out, nil, e
		}
		if len(matched) != 1 {
			return out, nil, errIncomplete
		}
		other := matched[0]
		resourceAllowed, e := s.canView(ctx, user, other, "resources")
		if e != nil {
			return out, nil, e
		}
		if !resourceAllowed {
			return out, nil, errIncomplete
		}
		current := statusCache[otherApp]
		if current.status == "" {
			current.status, current.seal, e = statusOnly(ctx, s, scope, user, other, nowOf(s))
			if e != nil {
				return out, nil, e
			}
			statusCache[otherApp] = current
		}
		item := dependency{Name: other.Name, Status: current.status, Meta: "CMDB depends_on", row: other, seal: current.seal}
		if rel.direction == "downstream" {
			out.Downstream = append(out.Downstream, item)
		} else {
			out.Upstream = append(out.Upstream, item)
		}
		if !seen[other.AppID] {
			otherRows = append(otherRows, other)
			seen[other.AppID] = true
		}
		seenEdges[edgeKey] = true
	}
	return out, otherRows, nil
}

func (s *Service) resourceOwner(ctx context.Context, resourceID, env string) (string, error) {
	var owners []string
	err := s.Store.DB.QueryRow(ctx, `SELECT array_agg(DISTINCT cra.application_id)
 FROM cmdb_resource_applications cra
 JOIN application_environments e ON e.id=cra.environment_id AND e.application_id=cra.application_id AND NOT e.archived
 WHERE cra.resource_id=$1 AND e.key=$2`, resourceID, env).Scan(&owners)
	if err != nil {
		return "", err
	}
	if len(owners) != 1 {
		return "", errIncomplete
	}
	return owners[0], nil
}
