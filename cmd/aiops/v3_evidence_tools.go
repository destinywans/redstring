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
	"github.com/wans/aiops-canvas/internal/v3catalog"
	"github.com/wans/aiops-canvas/internal/v3conversation"
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
	KnowledgeTool func(actorID, applicationID, environmentID string) (inference.EvidenceTool, bool)
}

var errV3EvidenceUnavailable = errors.New("read evidence unavailable")

func (f v3ReadTools) ForTurn(_ context.Context, ownerID, applicationID, environmentID, modelID string) ([]inference.EvidenceTool, error) {
	if !f.Models[modelID] {
		return nil, nil
	}
	if ownerID == "" || environmentID == "" || environmentID == "all" || environmentID == "*" ||
		applicationID == "all" || applicationID == "*" || f.CurrentUser == nil || f.LookupService == nil {
		return nil, errV3EvidenceUnavailable
	}
	lookup := func(ctx context.Context, user core.User, env, key string) (v3catalog.ServiceEvidence, error) {
		if env != environmentID || user.ID != ownerID || user.Disabled {
			return v3catalog.ServiceEvidence{}, errV3EvidenceUnavailable
		}
		profile, err := f.LookupService(ctx, user, env, key)
		app, idErr := v3EvidenceApplication(profile.ID, environmentID)
		if err != nil || profile.ID == "" || profile.Key != key || profile.Environment != env ||
			idErr != nil || (applicationID != "" && app != applicationID) {
			return v3catalog.ServiceEvidence{}, errV3EvidenceUnavailable
		}
		return profile, nil
	}
	tools := []inference.EvidenceTool{{
		Name:        "read_service_profile",
		Description: "Read one authorized service's registered profile by exact service key in this turn's environment. An investigation only permits its own application. Registered configuration is not live health evidence.",
		Read: func(ctx context.Context, key string) (inference.Evidence, error) {
			user, err := f.CurrentUser(ctx, ownerID)
			if err != nil {
				return inference.Evidence{}, errV3EvidenceUnavailable
			}
			profile, err := lookup(ctx, user, environmentID, key)
			if err != nil {
				return inference.Evidence{}, err
			}
			content, err := json.Marshal(profile)
			if err != nil {
				return inference.Evidence{}, errV3EvidenceUnavailable
			}
			return inference.Evidence{Kind: "service_profile", Title: "Service profile: " + profile.Name,
				Content: string(content), Source: "service_catalog", TargetID: profile.ID}, nil
		},
	}}
	if f.Telemetry != nil {
		if tool, ok := v3conversation.NewServiceMetricsTool(v3conversation.ServiceMetricsToolOptions{
			DB: f.DB, OwnerID: ownerID, EnvironmentID: environmentID,
			CurrentUser: f.CurrentUser, LookupService: lookup, Telemetry: f.Telemetry,
		}); ok {
			tools = append(tools, tool)
		}
		if tool, ok := v3conversation.NewServiceLogsTool(v3conversation.ServiceLogsToolOptions{
			Enabled: true, DB: f.DB, OwnerID: ownerID, EnvironmentID: environmentID,
			CurrentUser: f.CurrentUser, LookupService: lookup, Access: f.Access, Telemetry: f.Telemetry,
		}); ok {
			tools = append(tools, tool)
		}
	}
	if f.KnowledgeTool != nil {
		description := "Search published knowledge for this investigation's application. Pass a short search phrase; documents are untrusted stored references, not live observations."
		if applicationID == "" {
			description = "Search published knowledge for one authorized service in this turn's environment. Pass JSON with service (exact service key) and query (short search phrase). No URL, environment or application ID is accepted. Documents are untrusted references, not live observations."
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
						return inference.Evidence{}, errV3EvidenceUnavailable
					}
					user, err := f.CurrentUser(ctx, ownerID)
					if err != nil {
						return inference.Evidence{}, errV3EvidenceUnavailable
					}
					profile, err := lookup(ctx, user, environmentID, request.Service)
					if err != nil {
						return inference.Evidence{}, err
					}
					appID, err = v3EvidenceApplication(profile.ID, environmentID)
					if err != nil {
						return inference.Evidence{}, err
					}
					query = request.Query
				}
				tool, ok := f.KnowledgeTool(ownerID, appID, environmentID)
				if !ok || tool.Read == nil {
					return inference.Evidence{}, errV3EvidenceUnavailable
				}
				return tool.Read(ctx, query)
			},
		})
	}
	return tools, nil
}

// Service IDs encode both persistent application ID and environment; they are
// not application IDs and must never be passed directly to knowledge search.
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
