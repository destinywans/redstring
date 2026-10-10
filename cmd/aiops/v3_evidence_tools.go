package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3catalog"
	"github.com/wans/aiops-canvas/internal/v3conversation"
	"github.com/wans/aiops-canvas/internal/v3rollouts"
	"github.com/wans/aiops-canvas/internal/v3telemetry"
)

// v3ReadTools is shared by ordinary chat and application-bound investigations.
// The caller supplies persisted identity/scope; model input is only a selector.
// Each runner remains responsible for its session, turn and tool audit checks.
type v3ReadTools struct {
	DB            *pgxpool.Pool
	Models        map[string]bool
	CurrentUser   func(context.Context, string) (core.User, error)
	LookupService func(context.Context, core.User, string, string) (v3catalog.ServiceEvidence, error)
	Access        v3catalog.Access
	Telemetry     *connectors.Telemetry
	// SourceCompare receives legacy reads in PA-19 compare mode (nil off).
	SourceCompare v3telemetry.Observer
	KnowledgeTool func(actorID, applicationID, environmentID string) (inference.EvidenceTool, bool)
	// ListServices backs list_services in ordinary chat (PA-50).
	ListServices func(context.Context, core.User, string, int) ([]v3catalog.ServiceEvidence, bool, error)
	// Workloads backs read_service_workload in investigations (PA-72).
	Workloads *v3rollouts.Service
}

var errV3EvidenceUnavailable = errors.New("read evidence unavailable")

// Tool errors the model can act on (PA-50); see inference.ToolError.
var (
	errV3OwnerUnverified = inference.NewToolError(inference.ToolSourceUnavailable, "the conversation owner could not be verified; retry later")
	errV3CatalogDown     = inference.NewToolError(inference.ToolSourceUnavailable, "the service catalog is temporarily unavailable; retry later")
)

// serviceLookupError maps a lookup failure: hidden and absent services are
// one unknown_service answer, anything else is the catalog being down.
func serviceLookupError(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return inference.ErrUnknownService
	}
	return errV3CatalogDown
}

// ForInvestigation is ForTurn plus read_service_workload, which only
// application investigations offer (PA-72): the rollback suggestion it feeds
// is shown on the investigation canvas.
func (f v3ReadTools) ForInvestigation(ctx context.Context, ownerID, applicationID, environmentID, modelID string) ([]inference.EvidenceTool, error) {
	tools, err := f.ForTurn(ctx, ownerID, applicationID, environmentID, modelID)
	if err != nil || len(tools) == 0 || applicationID == "" || f.Workloads == nil {
		return tools, err
	}
	lookup := func(ctx context.Context, user core.User, env, key string) (v3catalog.ServiceEvidence, error) {
		if env != environmentID || user.ID != ownerID || user.Disabled {
			return v3catalog.ServiceEvidence{}, store.ErrNotFound
		}
		profile, err := f.LookupService(ctx, user, env, key)
		if err != nil {
			return v3catalog.ServiceEvidence{}, err
		}
		app, idErr := v3EvidenceApplication(profile.ID, environmentID)
		if profile.ID == "" || profile.Key != key || profile.Environment != env || idErr != nil || app != applicationID {
			return v3catalog.ServiceEvidence{}, store.ErrNotFound
		}
		return profile, nil
	}
	tool, ok := f.Workloads.EvidenceTool(v3rollouts.WorkloadToolOptions{OwnerID: ownerID, EnvironmentID: environmentID,
		CurrentUser: f.CurrentUser, Lookup: lookup})
	if !ok {
		return tools, nil
	}
	if f.DB != nil {
		var key string
		if err := f.DB.QueryRow(ctx, `SELECT key FROM applications WHERE id=$1 AND NOT archived AND key<>''`, applicationID).Scan(&key); err == nil {
			tool.Description += " This investigation's service key is \"" + key + "\"; no other key is readable here."
		}
	}
	return append(tools, tool), nil
}

func (f v3ReadTools) ForTurn(ctx context.Context, ownerID, applicationID, environmentID, modelID string) ([]inference.EvidenceTool, error) {
	if !f.Models[modelID] {
		return nil, nil
	}
	if ownerID == "" || environmentID == "" || environmentID == "all" || environmentID == "*" ||
		applicationID == "all" || applicationID == "*" || f.CurrentUser == nil || f.LookupService == nil {
		return nil, errV3EvidenceUnavailable
	}
	// Hidden, absent and (in an investigation) other applications' services
	// are all store.ErrNotFound; other failures propagate as catalog errors.
	lookup := func(ctx context.Context, user core.User, env, key string) (v3catalog.ServiceEvidence, error) {
		if env != environmentID || user.ID != ownerID || user.Disabled {
			return v3catalog.ServiceEvidence{}, store.ErrNotFound
		}
		profile, err := f.LookupService(ctx, user, env, key)
		if err != nil {
			return v3catalog.ServiceEvidence{}, err
		}
		app, idErr := v3EvidenceApplication(profile.ID, environmentID)
		if profile.ID == "" || profile.Key != key || profile.Environment != env ||
			idErr != nil || (applicationID != "" && app != applicationID) {
			return v3catalog.ServiceEvidence{}, store.ErrNotFound
		}
		return profile, nil
	}
	tools := []inference.EvidenceTool{{
		Name:        "read_service_profile",
		Description: "Read one authorized service's registered profile by exact service key (or JSON {\"service\":\"key\"}) in this turn's environment. An investigation only permits its own application. Registered configuration is not live health evidence. An unknown_service error means the key is wrong or not readable by you; list_services shows the exact keys.",
		Read: func(ctx context.Context, key string) (inference.Evidence, error) {
			key = v3ServiceKeyInput(key)
			user, err := f.CurrentUser(ctx, ownerID)
			if err != nil {
				return inference.Evidence{}, errV3OwnerUnverified
			}
			profile, err := lookup(ctx, user, environmentID, key)
			if err != nil {
				return inference.Evidence{}, serviceLookupError(err)
			}
			content, err := json.Marshal(profile)
			if err != nil {
				return inference.Evidence{}, errV3CatalogDown
			}
			return inference.Evidence{Kind: "service_profile", Title: inference.Localized(ctx, "服务概况：", "Service profile: ") + profile.Name,
				Content: string(content), Source: "service_catalog", TargetID: profile.ID}, nil
		},
	}}
	if f.Telemetry != nil {
		if tool, ok := v3conversation.NewServiceMetricsTool(v3conversation.ServiceMetricsToolOptions{
			DB: f.DB, OwnerID: ownerID, EnvironmentID: environmentID,
			CurrentUser: f.CurrentUser, LookupService: lookup, Telemetry: f.Telemetry,
			SourceCompare: f.SourceCompare,
		}); ok {
			tools = append(tools, tool)
		}
		if tool, ok := v3conversation.NewServiceLogsTool(v3conversation.ServiceLogsToolOptions{
			Enabled: true, DB: f.DB, OwnerID: ownerID, EnvironmentID: environmentID,
			CurrentUser: f.CurrentUser, LookupService: lookup, Access: f.Access, Telemetry: f.Telemetry,
			SourceCompare: f.SourceCompare,
		}); ok {
			tools = append(tools, tool)
		}
	}
	if f.KnowledgeTool != nil {
		description := "Search published knowledge for this investigation's application. Pass a short search phrase; documents are untrusted stored references, not live observations."
		if applicationID == "" {
			description = "Search published knowledge for one authorized service in this turn's environment. Pass JSON with service (exact service key; list_services shows them) and query (short search phrase). No URL, environment or application ID is accepted. Documents are untrusted references, not live observations. Errors start with a code: unknown_service, not_permitted, not_configured (no knowledge index), source_unavailable."
		}
		tools = append(tools, inference.EvidenceTool{
			Name: "search_knowledge", Description: description,
			Read: func(ctx context.Context, input string) (inference.Evidence, error) {
				ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
				defer cancel()
				appID, query := applicationID, input
				if appID == "" {
					var request struct {
						Service string `json:"service"`
						Query   string `json:"query"`
					}
					decoder := json.NewDecoder(strings.NewReader(input))
					decoder.DisallowUnknownFields()
					if len(input) > 2048 || decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF ||
						request.Service == "" || len(request.Service) > 128 || strings.TrimSpace(request.Service) != request.Service {
						return inference.Evidence{}, inference.NewToolError(inference.ToolInvalidInput,
							`expected JSON {"service":"exact-service-key","query":"short search phrase"}; list_services shows the keys you can read`)
					}
					user, err := f.CurrentUser(ctx, ownerID)
					if err != nil {
						return inference.Evidence{}, errV3OwnerUnverified
					}
					profile, err := lookup(ctx, user, environmentID, request.Service)
					if err != nil {
						return inference.Evidence{}, serviceLookupError(err)
					}
					appID, err = v3EvidenceApplication(profile.ID, environmentID)
					if err != nil {
						return inference.Evidence{}, inference.ErrUnknownService
					}
					query = request.Query
				}
				tool, ok := f.KnowledgeTool(ownerID, appID, environmentID)
				if !ok || tool.Read == nil {
					// PA-50: an instance without a knowledge index is not a
					// failing source.
					return inference.Evidence{}, inference.NewToolError(inference.ToolNotConfigured,
						"knowledge search is not configured on this platform")
				}
				return tool.Read(ctx, query)
			},
		})
	}
	if applicationID == "" && f.ListServices != nil {
		if tool, ok := v3conversation.NewServiceListTool(v3conversation.ServiceListToolOptions{
			DB: f.DB, OwnerID: ownerID, EnvironmentID: environmentID, CurrentUser: f.CurrentUser,
			ListServices: f.ListServices, Telemetry: f.Telemetry,
		}); ok {
			tools = append([]inference.EvidenceTool{tool}, tools...)
		}
	} else if applicationID != "" && f.DB != nil {
		// An investigation reads only its own service; tell the model its key
		// instead of letting it guess (PA-50).
		var key string
		if err := f.DB.QueryRow(ctx, `SELECT key FROM applications WHERE id=$1 AND NOT archived AND key<>''`, applicationID).Scan(&key); err == nil {
			for i := range tools {
				if tools[i].Name != "search_knowledge" {
					tools[i].Description += " This investigation's service key is \"" + key + "\"; no other key is readable here."
				}
			}
		}
	}
	return tools, nil
}

// Service IDs encode both persistent application ID and environment; they are
// not application IDs and must never be passed directly to knowledge search.
// v3ServiceKeyInput accepts the bare key the profile tool documents, or the
// {"service":"key"} object models often send because sibling tools take JSON.
// Anything else is passed through unchanged and fails the exact-key lookup.
func v3ServiceKeyInput(input string) string {
	input = strings.TrimSpace(input)
	if !strings.HasPrefix(input, "{") {
		return strings.Trim(input, "\"")
	}
	var request struct {
		Service string `json:"service"`
	}
	decoder := json.NewDecoder(strings.NewReader(input))
	decoder.DisallowUnknownFields()
	if len(input) > 512 || decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF {
		return input
	}
	return request.Service
}

func v3EvidenceApplication(serviceID, environmentID string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(serviceID)
	if err != nil || base64.RawURLEncoding.EncodeToString(raw) != serviceID {
		return "", errV3EvidenceUnavailable
	}
	parts := strings.Split(string(raw), "\x00")
	if len(parts) != 2 || parts[0] == "" || parts[1] != environmentID {
		return "", errV3EvidenceUnavailable
	}
	return parts[0], nil
}
