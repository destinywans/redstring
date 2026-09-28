package v3observe

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type discoveredService struct {
	// The current registration request uses ID as the service name. Keep the
	// original name only when it is unambiguous among visible workloads.
	ID             string   `json:"id"`
	ResourceID     string   `json:"resource_id"`
	UID            string   `json:"uid"`
	EnvironmentID  string   `json:"environment_id"`
	Cluster        string   `json:"cluster"`
	Namespace      string   `json:"namespace"`
	Kind           string   `json:"kind"`
	Replicas       *int     `json:"replicas"`
	Image          string   `json:"image"`
	OwnerHint      string   `json:"owner_hint"`
	OwnerLabelHint string   `json:"owner_label_hint,omitempty"`
	TierHint       string   `json:"tier_hint"`
	Matched        []string `json:"matched"`
	Missing        []string `json:"missing"`
}

type workloadCandidate struct {
	ResourceID    string
	Name          string
	ExternalID    string
	EnvironmentID string
	SourceID      string
	Cluster       string
	TeamID        string
	TeamName      string
	Attributes    []byte
}

type workloadAttributes struct {
	Namespace    string            `json:"namespace"`
	ResourceType string            `json:"resource_type"`
	Cluster      string            `json:"cluster"`
	Images       []string          `json:"images"`
	Replicas     *int              `json:"replicas"`
	Labels       map[string]string `json:"labels"`
}

type workloadNameScope struct {
	name, namespace, environment string
}

type rawWorkloadIdentity struct {
	count int
	id    string
}

var errRegisteredLimit = errors.New("service registry scan exceeds bound")

// discover reads the inventory snapshot, not live Kubernetes. A resource is
// exposed only after both service and resource policies permit its collector
// environment/team.
// Connector matches and tier are never inferred from an
// image or label except when a valid tier label is explicitly present.
func (s *Service) discover(w http.ResponseWriter, r *http.Request) {
	// Environment access is checked by the V3 middleware. A team-scoped
	// services grant is checked only after resolving each resource's owner.
	envs, ok := s.allowedEnvironments(w, r, "")
	if !ok {
		return
	}
	if len(r.URL.Query()["cluster"]) > 1 {
		httpapi.Error(w, 422, "invalid_cluster", "Select one cluster")
		return
	}
	cluster := strings.TrimSpace(r.URL.Query().Get("cluster"))
	if len(cluster) > 200 {
		httpapi.Error(w, 422, "invalid_cluster", "Cluster filter is too long")
		return
	}
	user := httpapi.CurrentUser(r)
	english := strings.HasPrefix(strings.ToLower(r.Header.Get("Accept-Language")), "en")
	visible := []discoveredService{}
	names := map[string]int{}
	clusters := map[string]bool{}
	cursor := ""
	scanned := 0
	for {
		// Bound the selected-cluster inventory walk. A second bounded-name
		// query below checks cross-cluster namesakes before exposing a row to
		// today's name-only registration page.
		batch, err := s.workloadBatch(r.Context(), envs, cluster, cursor)
		if err != nil {
			httpapi.Error(w, 503, "catalog_unavailable", "Resource catalog unavailable")
			return
		}
		if len(batch) == 0 {
			break
		}
		scanned += len(batch)
		if scanned > 2000 {
			httpapi.Error(w, 422, "result_limit", "Narrow the cluster or environment filter")
			return
		}
		for _, candidate := range batch {
			cursor = candidate.ResourceID
			names[strings.ToLower(candidate.Name)]++
			if cluster != "" && candidate.Cluster != cluster {
				continue
			}
			allowed, err := s.canDiscoverWorkload(r.Context(), user, candidate)
			if err != nil {
				httpapi.Error(w, 503, "catalog_unavailable", "Resource policy unavailable")
				return
			}
			if !allowed {
				continue
			}
			var attrs workloadAttributes
			if err := json.Unmarshal(candidate.Attributes, &attrs); err != nil {
				httpapi.Error(w, 503, "catalog_unavailable", "Resource catalog contains invalid attributes")
				return
			}
			item, valid := projectWorkload(candidate, attrs, english)
			if !valid {
				continue
			}
			visible = append(visible, item)
			clusters[item.Cluster] = true
			if len(visible) > 500 {
				httpapi.Error(w, 422, "result_limit", "Narrow the cluster or environment filter")
				return
			}
		}
		if len(batch) < 200 {
			break
		}
	}
	if cluster != "" && len(visible) > 0 {
		// The list scan can now stay within the selected cluster. Resolve
		// namesakes across every selected environment and cluster before
		// offering a name-only registration to the current web client.
		var err error
		names, err = s.workloadNamesakeCounts(r.Context(), envs, visible)
		if err != nil {
			httpapi.Error(w, 503, "catalog_unavailable", "Resource catalog unavailable")
			return
		}
	}
	raw, err := s.rawWorkloadIdentities(r.Context(), envs, visible)
	if err != nil {
		httpapi.Error(w, 503, "catalog_unavailable", "Resource catalog unavailable")
		return
	}
	// The current POST /services DTO cannot select cluster or CMDB UID. Never
	// offer a workload that its name-only lookup could reject or bind wrongly.
	items := make([]discoveredService, 0, len(visible))
	ambiguous := 0
	for _, item := range visible {
		identity := raw[workloadNameScope{strings.ToLower(item.ID), item.Namespace, item.EnvironmentID}]
		if names[strings.ToLower(item.ID)] > 1 || identity.count != 1 || identity.id != item.ResourceID {
			ambiguous++
			continue
		}
		items = append(items, item)
	}
	if cluster == "" && len(clusters) == 1 {
		for key := range clusters {
			cluster = key
		}
	}
	registered, err := s.registeredServiceCount(r.Context(), user, envs)
	if err != nil {
		if errors.Is(err, errRegisteredLimit) {
			httpapi.Error(w, 422, "result_limit", "Narrow the environment filter")
			return
		}
		httpapi.Error(w, 503, "catalog_unavailable", "Service registry unavailable")
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{
		"cluster": cluster, "items": items, "registered": registered,
		"ambiguous_workloads": ambiguous,
	})
}

// Registration checks raw same-name identities after its fresh-workload
// lookup. This includes stale, missing, malformed-source, and linked records.
// Match that boundary for each visible item without disclosing the blockers.
func (s *Service) rawWorkloadIdentities(ctx context.Context, envs []string, visible []discoveredService) (map[workloadNameScope]rawWorkloadIdentity, error) {
	result := make(map[workloadNameScope]rawWorkloadIdentity, len(visible))
	if len(visible) == 0 {
		return result, nil
	}
	names := make([]string, 0, len(visible))
	namespaces := make([]string, 0, len(visible))
	seenNames := map[string]bool{}
	seenNamespaces := map[string]bool{}
	for _, item := range visible {
		name := strings.ToLower(item.ID)
		result[workloadNameScope{name, item.Namespace, item.EnvironmentID}] = rawWorkloadIdentity{}
		if !seenNames[name] {
			names = append(names, name)
			seenNames[name] = true
		}
		if !seenNamespaces[item.Namespace] {
			namespaces = append(namespaces, item.Namespace)
			seenNamespaces[item.Namespace] = true
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	rows, err := s.Store.DB.Query(ctx, `SELECT lower(r.name),r.attributes->>'namespace',
 COALESCE(src.environment_key,''),COALESCE(r.attributes->>'environment_key',''),count(*),min(r.id)
 FROM cmdb_resources r
 LEFT JOIN cmdb_sync_sources src ON src.id=r.source_id AND src.kind=r.source_kind
 WHERE r.kind='deployment' AND r.source_kind='kubernetes'
 AND lower(r.name)=ANY($1::text[]) AND r.attributes->>'namespace'=ANY($2::text[])
 AND (src.environment_key=ANY($3::text[]) OR r.attributes->>'environment_key'=ANY($3::text[]))
 GROUP BY lower(r.name),r.attributes->>'namespace',src.environment_key,r.attributes->>'environment_key'`, names, namespaces, envs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name, namespace, sourceEnv, attributeEnv, id string
		var count int
		if err := rows.Scan(&name, &namespace, &sourceEnv, &attributeEnv, &count, &id); err != nil {
			return nil, err
		}
		for index, env := range []string{sourceEnv, attributeEnv} {
			// The second occurrence of a matching source/attribute environment
			// represents the same raw resource group.
			if env == "" || (index == 1 && env == sourceEnv) {
				continue
			}
			key := workloadNameScope{name, namespace, env}
			identity, tracked := result[key]
			if !tracked {
				continue
			}
			identity.count += count
			if identity.count == 1 {
				identity.id = id
			}
			result[key] = identity
		}
	}
	return result, rows.Err()
}

// Fetch one bounded page and release its database connection before policy
// calls, so discovery also works with a single-connection pool.
const trustedWorkloadFilter = `r.kind='deployment' AND r.source_kind='kubernetes' AND NOT r.is_mock
 AND r.observed_at BETWEEN now()-interval '5 minutes' AND now()+interval '1 minute'
 AND src.last_success_at BETWEEN now()-interval '5 minutes' AND now()+interval '1 minute'
 AND src.environment_key=ANY($1::text[]) AND src.environment_key<>''
 AND r.attributes->>'environment_key'=src.environment_key
 AND r.attributes->>'inventory_presence'='present'
 AND r.external_id<>'' AND r.attributes->>'uid'=r.external_id
 AND r.attributes->>'resource_type' IN ('Deployment','StatefulSet','DaemonSet')
 AND r.attributes->>'namespace'<>'' AND r.attributes->>'cluster'<>''
 AND NOT EXISTS (SELECT 1 FROM cmdb_resource_applications l WHERE l.resource_id=r.id)
 AND NOT EXISTS (SELECT 1 FROM applications a WHERE lower(a.key)=lower(r.name))`

func (s *Service) workloadBatch(ctx context.Context, envs []string, cluster, cursor string) ([]workloadCandidate, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	rows, err := s.Store.DB.Query(ctx, `SELECT r.id,r.name,r.external_id,src.environment_key,
 src.id,r.attributes->>'cluster',COALESCE(r.owner_team_id,''),COALESCE(t.name,''),r.attributes
 FROM cmdb_resources r
 JOIN cmdb_sync_sources src ON src.id=r.source_id AND src.kind=r.source_kind
 LEFT JOIN teams t ON t.id=r.owner_team_id
 WHERE r.id>$2 AND ($3='' OR r.attributes->>'cluster'=$3) AND `+trustedWorkloadFilter+`
 ORDER BY r.id LIMIT 200`, envs, cursor, cluster)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	batch := []workloadCandidate{}
	for rows.Next() {
		var item workloadCandidate
		if err = rows.Scan(&item.ResourceID, &item.Name, &item.ExternalID, &item.EnvironmentID,
			&item.SourceID, &item.Cluster, &item.TeamID, &item.TeamName, &item.Attributes); err != nil {
			return nil, err
		}
		batch = append(batch, item)
	}
	return batch, rows.Err()
}

// Only visible names are queried, but the count includes trusted namesakes
// outside the selected cluster and behind other teams' grants. The response
// never contains those namesake identities.
func (s *Service) workloadNamesakeCounts(ctx context.Context, envs []string, visible []discoveredService) (map[string]int, error) {
	keys := make([]string, 0, len(visible))
	seen := map[string]bool{}
	for _, item := range visible {
		key := strings.ToLower(item.ID)
		if !seen[key] {
			keys = append(keys, key)
			seen[key] = true
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	rows, err := s.Store.DB.Query(ctx, `SELECT lower(r.name),count(*)
 FROM cmdb_resources r
 JOIN cmdb_sync_sources src ON src.id=r.source_id AND src.kind=r.source_kind
 WHERE lower(r.name)=ANY($2::text[]) AND `+trustedWorkloadFilter+`
 GROUP BY lower(r.name)`, envs, keys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := make(map[string]int, len(keys))
	for rows.Next() {
		var name string
		var count int
		if err := rows.Scan(&name, &count); err != nil {
			return nil, err
		}
		counts[name] = count
	}
	return counts, rows.Err()
}

func (s *Service) canDiscoverWorkload(ctx context.Context, user core.User, item workloadCandidate) (bool, error) {
	for _, module := range []string{"services", "resources"} {
		err := s.Access.Authorize(ctx, user, rbac.V3Decision{
			Module: module, Action: "view", EnvironmentID: item.EnvironmentID, TeamID: item.TeamID,
		})
		if errors.Is(err, store.ErrForbidden) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
	}
	return true, nil
}

func projectWorkload(candidate workloadCandidate, attrs workloadAttributes, english bool) (discoveredService, bool) {
	if attrs.Cluster == "" || attrs.Namespace == "" || attrs.ResourceType == "" || candidate.Name == "" || candidate.ExternalID == "" {
		return discoveredService{}, false
	}
	ownerHint := "未识别"
	if english {
		ownerHint = "Unknown"
	}
	item := discoveredService{
		ID: candidate.Name, ResourceID: candidate.ResourceID, UID: candidate.ExternalID, EnvironmentID: candidate.EnvironmentID,
		Cluster: attrs.Cluster, Namespace: attrs.Namespace, Kind: attrs.ResourceType,
		Replicas: attrs.Replicas, OwnerHint: ownerHint, Matched: []string{}, Missing: []string{},
	}
	if len(attrs.Images) > 0 {
		item.Image = attrs.Images[0]
	}
	if candidate.TeamName != "" {
		item.OwnerHint = candidate.TeamName
	} else if label := strings.TrimSpace(attrs.Labels["team"]); label != "" {
		// Kubernetes labels are unverified claims; they cannot select an owner
		// by default or supply an authorization team ID.
		item.OwnerLabelHint = "team=" + label
	}
	if tier := strings.TrimSpace(attrs.Labels["tier"]); tier == "T0" || tier == "T1" || tier == "T2" {
		item.TierHint = tier
	}
	return item, true
}

func (s *Service) registeredServiceCount(ctx context.Context, user core.User, envs []string) (int, error) {
	rows, err := s.Store.DB.Query(ctx, `SELECT DISTINCT a.id,ae.environment_id,COALESCE(a.owner_team_id,'')
 FROM applications a
 JOIN v3_application_environments ae ON ae.application_id=a.id
 JOIN application_environments e ON e.application_id=a.id AND e.key=ae.environment_id AND NOT e.archived
 WHERE NOT a.archived AND ae.environment_id=ANY($1::text[]) LIMIT 1001`, envs)
	if err != nil {
		return 0, err
	}
	type serviceScope struct{ id, env, team string }
	services := []serviceScope{}
	for rows.Next() {
		var item serviceScope
		if err = rows.Scan(&item.id, &item.env, &item.team); err != nil {
			break
		}
		services = append(services, item)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return 0, err
	}
	if len(services) > 1000 {
		return 0, errRegisteredLimit
	}
	seen := map[string]bool{}
	for _, service := range services {
		decision := rbac.V3Decision{Module: "services", Action: "view", EnvironmentID: service.env, ApplicationID: service.id}
		err = s.Access.Authorize(ctx, user, decision)
		if errors.Is(err, store.ErrForbidden) && service.team != "" {
			decision.ApplicationID, decision.TeamID = "", service.team
			err = s.Access.Authorize(ctx, user, decision)
		}
		if err == nil {
			seen[service.id] = true
		} else if !errors.Is(err, store.ErrForbidden) {
			return 0, err
		}
	}
	return len(seen), nil
}
