package v3telemetry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/v3integrations"
)

// LegacyBinding is one entry of application_environments.bindings for a V3
// service environment.
type LegacyBinding struct {
	ApplicationID string
	EnvironmentID string
	Kind          string
	ConnectorID   string
	Resource      string
}

// ExistingConnector and ExistingTarget are current registry rows.
type ExistingConnector struct {
	ID            string
	EnvironmentID string
	Provider      string
	Endpoint      string
	AuthMethod    string
	TenantID      string
	Username      string
	OptionsValid  bool
}

type ExistingTarget struct {
	ID            string
	ApplicationID string
	EnvironmentID string
	ConnectorID   string
	Provider      string
	TargetKind    string
	ExternalID    string
}

// State is the database half of the importer's input.
type State struct {
	Bindings   []LegacyBinding
	Connectors []ExistingConnector
	Targets    []ExistingTarget
}

const maxStateRows = 10000

// ErrSchema means migration 163 has not run on this database yet.
var ErrSchema = errors.New("migration 163_v3_source_registry_import is not applied; start the upgraded server once first")

// LoadState reads bindings and registry rows of the given environments.
func LoadState(ctx context.Context, q Querier, environments []string) (State, error) {
	var state State
	rows, err := q.Query(ctx, `SELECT count(*) FROM information_schema.columns
 WHERE table_schema=current_schema() AND table_name='v3_connectors' AND column_name='options'`)
	if err != nil {
		return state, err
	}
	var columns int
	for rows.Next() {
		err = rows.Scan(&columns)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return state, err
	}
	if columns != 1 {
		return state, ErrSchema
	}
	err = scanAll(ctx, q, `SELECT a.id,ve.environment_id,COALESCE(b.kind,''),COALESCE(b.connector_id,''),COALESCE(b.resource,'')
 FROM applications a JOIN v3_application_environments ve ON ve.application_id=a.id
 JOIN application_environments e ON e.application_id=a.id AND e.key=ve.environment_id AND NOT e.archived
 JOIN LATERAL jsonb_to_recordset(e.bindings) AS b(kind text, connector_id text, resource text) ON true
 WHERE NOT a.archived AND ve.environment_id=ANY($1::text[])
 ORDER BY a.id,ve.environment_id,b.kind,b.resource LIMIT 10001`, environments, func(rows scanner) error {
		var b LegacyBinding
		err := rows.Scan(&b.ApplicationID, &b.EnvironmentID, &b.Kind, &b.ConnectorID, &b.Resource)
		state.Bindings = append(state.Bindings, b)
		return err
	})
	if err != nil {
		return state, err
	}
	err = scanAll(ctx, q, `SELECT id,environment_id,provider,endpoint,auth_method,options
 FROM v3_connectors WHERE environment_id=ANY($1::text[]) ORDER BY id LIMIT 10001`, environments, func(rows scanner) error {
		var c ExistingConnector
		var options []byte
		err := rows.Scan(&c.ID, &c.EnvironmentID, &c.Provider, &c.Endpoint, &c.AuthMethod, &options)
		parsed, valid := parseOptions(options)
		c.TenantID, c.Username, c.OptionsValid = parsed.TenantID, parsed.Username, valid
		state.Connectors = append(state.Connectors, c)
		return err
	})
	if err != nil {
		return state, err
	}
	err = scanAll(ctx, q, `SELECT t.id,t.application_id,t.environment_id,t.connector_id,c.provider,t.target_kind,t.external_id
 FROM v3_connector_targets t JOIN v3_connectors c ON c.id=t.connector_id AND c.environment_id=t.environment_id
 WHERE t.environment_id=ANY($1::text[]) ORDER BY t.id LIMIT 10001`, environments, func(rows scanner) error {
		var t ExistingTarget
		err := rows.Scan(&t.ID, &t.ApplicationID, &t.EnvironmentID, &t.ConnectorID, &t.Provider, &t.TargetKind, &t.ExternalID)
		state.Targets = append(state.Targets, t)
		return err
	})
	return state, err
}

type scanner interface{ Scan(...any) error }

func scanAll(ctx context.Context, q Querier, sql string, environments []string, scan func(scanner) error) error {
	rows, err := q.Query(ctx, sql, environments)
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		if count++; count > maxStateRows {
			return fmt.Errorf("more than %d rows; import one environment at a time", maxStateRows)
		}
		if err = scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// ImportInput is the file half of the importer's input.
type ImportInput struct {
	Tier         string
	Environments []string
	Telemetry    connectors.TelemetryConfig
	Catalog      Catalog
	Approvals    Approvals
	// Workloads are the AIOPS_KUBERNETES_REGISTRY entries by registration ID.
	Workloads map[string]LegacyWorkload
}

// LegacyWorkload is one AIOPS_KUBERNETES_REGISTRY entry, reduced to what the
// import maps. File paths are not carried: the token and the CA move to Vault
// (the CA in the "ca" field next to the token).
type LegacyWorkload struct {
	Endpoint   string
	Namespace  string
	Deployment string
	Token      bool
	CustomCA   bool
	Demo       bool
	// VerificationQueries are the legacy counter and freshness query IDs.
	// Their meaning differs from the rollback verification (rate, ratio,
	// age), so they are listed for review and never converted.
	VerificationQueries []string
}

// demoKubernetesID is the built-in demo connector, removed in P4.
const demoKubernetesID = "demo-kubernetes"

// Plan is the reviewed output of a dry run. Applying it requires the same
// digest to be recomputed from the then-current state.
type Plan struct {
	Version            int                 `json:"version"`
	Tier               string              `json:"tier"`
	Environments       []string            `json:"environments"`
	Digest             string              `json:"digest"`
	Summary            PlanSummary         `json:"summary"`
	Connectors         []PlannedConnector  `json:"connectors"`
	Targets            []PlannedTarget     `json:"targets"`
	CredentialBindings []PlannedCredential `json:"credential_bindings"`
	Catalog            CatalogFile         `json:"catalog"`
	Deferred           []PlanIssue         `json:"deferred"`
	Conflicts          []PlanIssue         `json:"conflicts"`
	// Reviews are imported bindings that need a deployer decision before
	// they do everything the legacy registration did (a rollback policy).
	Reviews []PlanIssue `json:"reviews"`
}

type PlanSummary struct {
	Bindings          int `json:"bindings"`
	Imported          int `json:"imported"`
	Deferred          int `json:"deferred"`
	Conflicts         int `json:"conflicts"`
	Reviews           int `json:"reviews"`
	ConnectorsCreated int `json:"connectors_created"`
	TargetsCreated    int `json:"targets_created"`
	CatalogAdded      int `json:"catalog_entries_added"`
}

type PlannedConnector struct {
	Action        string   `json:"action"`
	ID            string   `json:"id"`
	EnvironmentID string   `json:"environment_id"`
	Provider      string   `json:"provider"`
	Name          string   `json:"name,omitempty"`
	Category      string   `json:"category,omitempty"`
	Endpoint      string   `json:"endpoint"`
	AuthMethod    string   `json:"auth_method"`
	TenantID      string   `json:"tenant_id,omitempty"`
	Username      string   `json:"username,omitempty"`
	Tools         []string `json:"tools,omitempty"`
}

type PlannedTarget struct {
	Action         string `json:"action"`
	ID             string `json:"id"`
	ApplicationID  string `json:"application_id"`
	EnvironmentID  string `json:"environment_id"`
	ConnectorID    string `json:"connector_id"`
	TargetKind     string `json:"target_kind"`
	ExternalID     string `json:"external_id"`
	LegacyResource string `json:"legacy_resource"`
	// Effect names readers that use this target as soon as it exists.
	Effect string `json:"effect,omitempty"`
}

// PlannedCredential is a credential-use approval the operator must add (with
// a real Vault reference) before activating the connector. Approved is true
// when some reference is already approved for it.
type PlannedCredential struct {
	EnvironmentID string `json:"environment_id"`
	ConnectorID   string `json:"connector_id"`
	Provider      string `json:"provider"`
	Endpoint      string `json:"endpoint"`
	AuthMethod    string `json:"auth_method"`
	Approved      bool   `json:"approved"`
	// CAField asks for the cluster CA in the "ca" field of the same secret.
	CAField bool `json:"ca_field,omitempty"`
}

type PlanIssue struct {
	ApplicationID string   `json:"application_id"`
	EnvironmentID string   `json:"environment_id"`
	Kind          string   `json:"kind"`
	Resource      string   `json:"resource"`
	Reasons       []string `json:"reasons"`
}

const (
	actionCreate = "create"
	actionReuse  = "reuse"
	// metric_resource targets are read by the dashboard and service views
	// already, so creating one changes those pages immediately.
	effectMetricReaders = "dashboard_and_service_views_read_this_target"
)

// importID derives stable IDs, so a dry run shows the IDs apply will write
// and the generated catalog is final before anything is applied.
func importID(parts ...string) string {
	sum := sha256.Sum256([]byte("pa19-import\x00" + strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:16])
}

// BuildPlan maps every telemetry binding to a connector, a target and catalog
// entries. Each binding is atomic: any conflict skips the whole binding.
func BuildPlan(input ImportInput, state State) Plan {
	plan := Plan{Version: 1, Tier: input.Tier, Environments: append([]string(nil), input.Environments...),
		Connectors: []PlannedConnector{}, Targets: []PlannedTarget{}, CredentialBindings: []PlannedCredential{},
		Deferred: []PlanIssue{}, Conflicts: []PlanIssue{}, Reviews: []PlanIssue{}}
	sort.Strings(plan.Environments)
	catalog := input.Catalog.Clone()
	bindings := append([]LegacyBinding(nil), state.Bindings...)
	sort.Slice(bindings, func(i, j int) bool { return bindingKey(bindings[i]) < bindingKey(bindings[j]) })

	perSurface := map[string]int{}
	for _, b := range bindings {
		if surface, ok := SurfaceOf(b.Kind); ok {
			perSurface[b.ApplicationID+"\x00"+b.EnvironmentID+"\x00"+string(surface)]++
		}
	}
	connectorsByKey := map[string]ExistingConnector{}
	connectorIDs := map[string]bool{}
	for _, c := range state.Connectors {
		connectorsByKey[c.EnvironmentID+"\x00"+c.Provider+"\x00"+canonicalEndpoint(c.Endpoint)] = c
		connectorIDs[c.ID] = true
	}
	owners := map[string]string{}
	targetIDs := map[string]bool{}
	for _, t := range state.Targets {
		owners[t.EnvironmentID+"\x00"+t.ConnectorID+"\x00"+t.TargetKind+"\x00"+t.ExternalID] = t.ApplicationID
		targetIDs[t.ID] = true
	}
	planned := map[string]bool{}
	plannedTargets := map[string]bool{}
	credentials := map[string]bool{}

	for _, b := range bindings {
		plan.Summary.Bindings++
		issue := PlanIssue{ApplicationID: b.ApplicationID, EnvironmentID: b.EnvironmentID, Kind: b.Kind, Resource: b.Resource}
		conflict := func(reasons ...string) {
			issue.Reasons = reasons
			plan.Conflicts = append(plan.Conflicts, issue)
			plan.Summary.Conflicts++
		}
		deferIssue := func(reason string) {
			issue.Reasons = []string{reason}
			plan.Deferred = append(plan.Deferred, issue)
			plan.Summary.Deferred++
		}
		if b.Kind == WorkloadProvider {
			legacy, registered := input.Workloads[b.ConnectorID]
			switch {
			case b.ConnectorID == demoKubernetesID || (registered && legacy.Demo):
				deferIssue("demo_connector_not_imported")
				continue
			case !registered:
				conflict("resource_not_registered")
				continue
			}
			info, _ := v3integrations.Provider(WorkloadProvider)
			endpoint := canonicalEndpoint(legacy.Endpoint)
			var reasons []string
			if !v3integrations.ValidEndpoint(endpoint) {
				reasons = append(reasons, "endpoint_not_allowed")
			}
			if !legacy.Token {
				reasons = append(reasons, "auth_method_unsupported")
			}
			if !kubernetesName.MatchString(legacy.Namespace) || !kubernetesName.MatchString(legacy.Deployment) {
				reasons = append(reasons, "workload_name_invalid")
			}
			if len(reasons) > 0 {
				conflict(reasons...)
				continue
			}
			connector := PlannedConnector{Action: actionCreate, EnvironmentID: b.EnvironmentID, Provider: WorkloadProvider,
				Name: info.Name, Category: info.Category, Endpoint: endpoint, AuthMethod: "bearer", Tools: []string{WorkloadTool}}
			if existing, found := connectorsByKey[b.EnvironmentID+"\x00"+WorkloadProvider+"\x00"+endpoint]; found {
				if existing.AuthMethod != "bearer" {
					reasons = append(reasons, "existing_connector_auth_differs")
				}
				connector.Action, connector.ID, connector.Name, connector.Endpoint = actionReuse, existing.ID, "", existing.Endpoint
				connector.Category, connector.Tools = "", nil
			} else {
				connector.ID = importID("connector", b.EnvironmentID, WorkloadProvider, endpoint)
				if connectorIDs[connector.ID] {
					reasons = append(reasons, "connector_id_collision")
				}
			}
			// One workload per service environment (the resolver refuses
			// more), keyed in the catalog by the Deployment name.
			target := PlannedTarget{Action: actionCreate, ApplicationID: b.ApplicationID, EnvironmentID: b.EnvironmentID,
				ConnectorID: connector.ID, TargetKind: WorkloadTargetKind, ExternalID: legacy.Deployment, LegacyResource: b.Resource}
			var current []ExistingTarget
			for _, t := range state.Targets {
				if t.TargetKind == WorkloadTargetKind && t.ApplicationID == b.ApplicationID && t.EnvironmentID == b.EnvironmentID {
					current = append(current, t)
				}
			}
			switch {
			case len(current) > 1:
				reasons = append(reasons, "existing_targets_ambiguous")
			case len(current) == 1 && current[0].ConnectorID != connector.ID:
				reasons = append(reasons, "existing_target_other_source")
			case len(current) == 1:
				target.Action, target.ID, target.ExternalID = actionReuse, current[0].ID, current[0].ExternalID
			default:
				target.ID = importID("target", b.EnvironmentID, connector.ID, WorkloadTargetKind, target.ExternalID)
				if owner, taken := owners[b.EnvironmentID+"\x00"+connector.ID+"\x00"+WorkloadTargetKind+"\x00"+target.ExternalID]; taken && owner != b.ApplicationID {
					reasons = append(reasons, "target_owned_by_other_service")
				}
				if targetIDs[target.ID] || plannedTargets[target.ID] {
					reasons = append(reasons, "target_id_collision")
				}
			}
			key := ScopeKey{ConnectorID: connector.ID, ApplicationID: b.ApplicationID, EnvironmentID: b.EnvironmentID, ResourceID: target.ExternalID}
			var scope *WorkloadScope
			if current, exists := catalog.Workloads[key]; exists {
				if current.Namespace != legacy.Namespace || current.Deployment != legacy.Deployment {
					reasons = append(reasons, "scope_conflict")
				}
			} else {
				// Rollback stays off until the deployer reviews a policy.
				scope = &WorkloadScope{Namespace: legacy.Namespace, Deployment: legacy.Deployment}
			}
			if len(reasons) > 0 {
				conflict(reasons...)
				continue
			}
			plan.Summary.Imported++
			if !planned[connector.ID] {
				planned[connector.ID] = true
				plan.Connectors = append(plan.Connectors, connector)
				if connector.Action == actionCreate {
					plan.Summary.ConnectorsCreated++
					connectorsByKey[b.EnvironmentID+"\x00"+WorkloadProvider+"\x00"+endpoint] = ExistingConnector{ID: connector.ID,
						EnvironmentID: b.EnvironmentID, Provider: WorkloadProvider, Endpoint: endpoint, AuthMethod: "bearer", OptionsValid: true}
				}
			}
			if !plannedTargets[target.ID] {
				plannedTargets[target.ID] = true
				plan.Targets = append(plan.Targets, target)
				if target.Action == actionCreate {
					plan.Summary.TargetsCreated++
					owners[b.EnvironmentID+"\x00"+connector.ID+"\x00"+WorkloadTargetKind+"\x00"+target.ExternalID] = b.ApplicationID
				}
			}
			if scope != nil {
				catalog.Workloads[key] = *scope
				plan.Summary.CatalogAdded++
			}
			if !credentials[connector.ID] {
				credentials[connector.ID] = true
				plan.CredentialBindings = append(plan.CredentialBindings, PlannedCredential{EnvironmentID: b.EnvironmentID,
					ConnectorID: connector.ID, Provider: WorkloadProvider, Endpoint: endpoint, AuthMethod: "bearer",
					Approved: input.Approvals.AnyFor(input.Tier, b.EnvironmentID, connector.ID, WorkloadProvider, endpoint),
					CAField:  legacy.CustomCA})
			}
			if catalog.Workloads[key].Rollback == nil {
				review := issue
				review.Reasons = []string{"rollback_policy_requires_review"}
				for _, id := range legacy.VerificationQueries {
					review.Reasons = append(review.Reasons, "legacy_verification_query:"+id)
				}
				plan.Reviews = append(plan.Reviews, review)
				plan.Summary.Reviews++
			}
			continue
		}
		surface, telemetry := SurfaceOf(b.Kind)
		if !telemetry {
			deferIssue("not_telemetry")
			continue
		}
		if perSurface[b.ApplicationID+"\x00"+b.EnvironmentID+"\x00"+string(surface)] > 1 {
			conflict("surface_ambiguous")
			continue
		}
		legacy, ok := LegacySource(input.Telemetry, b.Kind, b.Resource)
		if !ok {
			conflict("resource_not_registered")
			continue
		}
		info, _ := v3integrations.Provider(b.Kind)
		var reasons []string
		if !v3integrations.ValidEndpoint(legacy.Endpoint) {
			reasons = append(reasons, "endpoint_not_allowed")
		}
		if legacy.CustomCA {
			reasons = append(reasons, "custom_ca_unsupported")
		}
		if !slices.Contains(info.Methods, legacy.AuthMethod) {
			reasons = append(reasons, "auth_method_unsupported")
		} else if legacy.AuthMethod == "basic" && (legacy.Username == "" || !validUsername(legacy.Username)) {
			// The options hold the user; the password is the credential.
			reasons = append(reasons, "basic_username_invalid")
		}
		if len(reasons) > 0 {
			conflict(reasons...)
			continue
		}

		// Connector: reuse the registered one with this exact endpoint.
		connector := PlannedConnector{Action: actionCreate, EnvironmentID: b.EnvironmentID, Provider: b.Kind,
			Name: info.Name, Category: info.Category, Endpoint: legacy.Endpoint, AuthMethod: legacy.AuthMethod,
			TenantID: legacy.TenantID, Username: legacy.Username, Tools: toolsOf(b.Kind, info)}
		if existing, found := connectorsByKey[b.EnvironmentID+"\x00"+b.Kind+"\x00"+legacy.Endpoint]; found {
			switch {
			case existing.AuthMethod != legacy.AuthMethod:
				reasons = append(reasons, "existing_connector_auth_differs")
			case !existing.OptionsValid || existing.TenantID != legacy.TenantID:
				reasons = append(reasons, "existing_connector_tenant_differs")
			case existing.Username != legacy.Username:
				reasons = append(reasons, "existing_connector_username_differs")
			}
			connector.Action, connector.ID, connector.Name, connector.Endpoint = actionReuse, existing.ID, "", existing.Endpoint
			connector.Category, connector.Tools = "", nil
		} else {
			connector.ID = importID("connector", b.EnvironmentID, b.Kind, legacy.Endpoint)
			if connectorIDs[connector.ID] {
				reasons = append(reasons, "connector_id_collision")
			}
		}

		// Target: one per surface. An existing target on this connector is
		// reused; any other existing source for the surface is a conflict.
		kind := TargetKind(b.Kind)
		target := PlannedTarget{Action: actionCreate, ApplicationID: b.ApplicationID, EnvironmentID: b.EnvironmentID,
			ConnectorID: connector.ID, TargetKind: kind, ExternalID: b.Resource, LegacyResource: b.Resource}
		var current []ExistingTarget
		for _, t := range state.Targets {
			if s, ok := SurfaceOf(t.Provider); ok && s == surface && TargetKind(t.Provider) == t.TargetKind &&
				t.ApplicationID == b.ApplicationID && t.EnvironmentID == b.EnvironmentID {
				current = append(current, t)
			}
		}
		switch {
		case len(current) > 1:
			reasons = append(reasons, "existing_targets_ambiguous")
		case len(current) == 1 && (current[0].ConnectorID != connector.ID || current[0].TargetKind != kind):
			reasons = append(reasons, "existing_target_other_source")
		case len(current) == 1:
			target.Action, target.ID, target.ExternalID = actionReuse, current[0].ID, current[0].ExternalID
		default:
			if !v3integrations.ResourceIDValid(b.Resource) {
				reasons = append(reasons, "resource_id_invalid")
			}
			target.ID = importID("target", b.EnvironmentID, connector.ID, kind, b.Resource)
			if owner, taken := owners[b.EnvironmentID+"\x00"+connector.ID+"\x00"+kind+"\x00"+b.Resource]; taken && owner != b.ApplicationID {
				reasons = append(reasons, "target_owned_by_other_service")
			}
			if targetIDs[target.ID] {
				reasons = append(reasons, "target_id_collision")
			}
			if kind == "metric_resource" {
				target.Effect = effectMetricReaders
			}
		}

		// Catalog: the legacy scope under the target's resource ID.
		key := ScopeKey{ConnectorID: connector.ID, ApplicationID: b.ApplicationID, EnvironmentID: b.EnvironmentID, ResourceID: target.ExternalID}
		queries := map[v3integrations.PrometheusQueryKey]string{}
		var scope *Scope
		if b.Kind == "prometheus" {
			ids := make([]string, 0, len(legacy.Queries))
			for id := range legacy.Queries {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			for _, id := range ids {
				qk := v3integrations.PrometheusQueryKey{ConnectorID: key.ConnectorID, ApplicationID: key.ApplicationID,
					EnvironmentID: key.EnvironmentID, ResourceID: key.ResourceID, QueryID: id}
				expression := legacy.Queries[id]
				if !v3integrations.PrometheusQueryValid(qk, expression) {
					reasons = append(reasons, "query_invalid:"+id)
				} else if current, exists := catalog.Queries[qk]; exists && current != expression {
					reasons = append(reasons, "query_conflict:"+id)
				} else if !exists {
					queries[qk] = expression
				}
			}
		} else if current, exists := catalog.Scopes[key]; exists {
			if current.Provider != b.Kind || len(scopeDifferences(b.Kind, legacy.Scope, current.ApplicationTelemetry)) > 0 {
				reasons = append(reasons, "scope_conflict")
			}
		} else {
			scope = &Scope{Provider: b.Kind, ApplicationTelemetry: legacy.Scope}
		}
		if len(reasons) > 0 {
			conflict(reasons...)
			continue
		}

		// Commit the binding.
		plan.Summary.Imported++
		if !planned[connector.ID] {
			planned[connector.ID] = true
			plan.Connectors = append(plan.Connectors, connector)
			if connector.Action == actionCreate {
				plan.Summary.ConnectorsCreated++
				connectorsByKey[b.EnvironmentID+"\x00"+b.Kind+"\x00"+legacy.Endpoint] = ExistingConnector{ID: connector.ID,
					EnvironmentID: b.EnvironmentID, Provider: b.Kind, Endpoint: legacy.Endpoint, AuthMethod: legacy.AuthMethod,
					TenantID: legacy.TenantID, Username: legacy.Username, OptionsValid: true}
			}
		}
		if !plannedTargets[target.ID] {
			plannedTargets[target.ID] = true
			plan.Targets = append(plan.Targets, target)
			if target.Action == actionCreate {
				plan.Summary.TargetsCreated++
				owners[b.EnvironmentID+"\x00"+connector.ID+"\x00"+kind+"\x00"+target.ExternalID] = b.ApplicationID
			}
		}
		for qk, expression := range queries {
			catalog.Queries[qk] = expression
			plan.Summary.CatalogAdded++
		}
		// PA-19 P4: the file's search attestation is per Tempo connector.
		if b.Kind == "tempo" && input.Telemetry.TempoSearchFailClosed {
			catalog.TempoSearchFailClosed[connector.ID] = true
		}
		if scope != nil {
			catalog.Scopes[key] = *scope
			plan.Summary.CatalogAdded++
		}
		if legacy.AuthMethod != "none" && !credentials[connector.ID] {
			credentials[connector.ID] = true
			plan.CredentialBindings = append(plan.CredentialBindings, PlannedCredential{EnvironmentID: b.EnvironmentID,
				ConnectorID: connector.ID, Provider: b.Kind, Endpoint: legacy.Endpoint, AuthMethod: legacy.AuthMethod,
				Approved: input.Approvals.AnyFor(input.Tier, b.EnvironmentID, connector.ID, b.Kind, legacy.Endpoint)})
		}
	}
	// PA-19 P4: a V3 instance takes its client limits from the catalog. A
	// catalog that already has limits keeps them.
	if catalog.Limits == (Limits{}) {
		catalog.Limits = LimitsOf(input.Telemetry)
	}
	plan.Catalog = catalog.File()
	plan.Digest = plan.digest()
	return plan
}

func bindingKey(b LegacyBinding) string {
	return b.ApplicationID + "\x00" + b.EnvironmentID + "\x00" + b.Kind + "\x00" + b.Resource + "\x00" + b.ConnectorID
}

func toolsOf(provider string, info v3integrations.ProviderInfo) []string {
	tool := telemetryProviders[provider].tool
	if slices.Contains(info.Tools, tool) {
		return []string{tool}
	}
	return []string{}
}

// digest covers everything apply writes or the operator must install.
func (p Plan) digest() string {
	raw, _ := json.Marshal(struct {
		Tier         string
		Environments []string
		Connectors   []PlannedConnector
		Targets      []PlannedTarget
		Catalog      CatalogFile
	}{p.Tier, p.Environments, p.Connectors, p.Targets, p.Catalog})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
