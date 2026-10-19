package v3memory

import (
	"encoding/base64"
	"net/http"
	"sort"
	"strings"

	"github.com/destinywans/redstring/internal/httpapi"
)

type memoryLayerEvidence struct {
	Kind    string   `json:"kind"`
	FactIDs []string `json:"fact_ids"`
}

type memoryLayer struct {
	ID          string              `json:"id"`
	Name        string              `json:"name"`
	Count       int                 `json:"count"`
	Description string              `json:"description"`
	Evidence    memoryLayerEvidence `json:"evidence"`
}

type memoryService struct {
	ID            string   `json:"id"`
	ApplicationID string   `json:"application_id"`
	EnvironmentID string   `json:"environment_id"`
	Name          string   `json:"name"`
	Key           string   `json:"key"`
	Count         int      `json:"count"`
	FactIDs       []string `json:"fact_ids"`
}

type memoryFactEvidence struct {
	DocumentID string `json:"document_id"`
	Version    int    `json:"version"`
}

type memoryViewFact struct {
	Fact
	// SourceTitle is the source document's title, null when it has none (PA-166).
	SourceTitle *string            `json:"source_title"`
	Confidence  *string            `json:"confidence"`
	Recalls     *int               `json:"recalls"`
	Expires     *string            `json:"expires"`
	Evidence    memoryFactEvidence `json:"evidence"`
}

type memoryConflictSide struct {
	ID          string  `json:"id"`
	Text        string  `json:"text"`
	Source      string  `json:"source"`
	SourceTitle *string `json:"source_title"`
}

type memoryViewConflict struct {
	ID            string             `json:"id"`
	State         string             `json:"state"`
	EnvironmentID string             `json:"environment_id"`
	ApplicationID string             `json:"application_id,omitempty"`
	TeamID        string             `json:"team_id,omitempty"`
	A             memoryConflictSide `json:"a"`
	B             memoryConflictSide `json:"b"`
}

type memoryViewResponse struct {
	Layers            []memoryLayer        `json:"layers"`
	Services          []memoryService      `json:"services"`
	Service           *string              `json:"service"`
	Facts             []memoryViewFact     `json:"facts"`
	Conflicts         []memoryViewConflict `json:"conflicts"`
	Conflict          *memoryViewConflict  `json:"conflict"`
	Policy            *string              `json:"policy"`
	Partial           bool                 `json:"partial"`
	UnavailableFields []string             `json:"unavailable_fields"`
}

// sourceTitle is a source document's title, or nil when it has none.
func (s memorySnapshot) sourceTitle(id string) *string {
	if title, ok := s.SourceTitles[id]; ok {
		return &title
	}
	return nil
}

func memoryServiceID(appID, env string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(appID + "\x00" + env))
}

func (s *Service) view(w http.ResponseWriter, r *http.Request) {
	sources, confidence, valid := memoryFacets(r)
	if !valid {
		failure(w, ErrInvalid)
		return
	}
	values := r.URL.Query()["service"]
	if len(values) > 1 || len(values) == 1 &&
		(values[0] == "" || len(values[0]) > 1024 || strings.TrimSpace(values[0]) != values[0]) {
		httpapi.Error(w, 422, "invalid_service", "Select one exact service reference")
		return
	}
	snapshot, err := s.readSnapshot(r, false, true)
	if err != nil {
		readFailure(w, err)
		return
	}
	response := memoryViewResponse{
		Layers: []memoryLayer{}, Services: []memoryService{}, Facts: []memoryViewFact{},
		Conflicts: []memoryViewConflict{}, Partial: true,
		UnavailableFields: []string{"working_layer", "episodic_layer", "semantic_layer", "confidence", "recalls", "expires", "policy"},
	}
	byService := map[string]*memoryService{}
	for _, fact := range snapshot.Facts {
		if fact.ApplicationID == "" {
			continue
		}
		app, found := snapshot.Applications[fact.ApplicationID]
		if !found {
			unavailable(w)
			return
		}
		id := memoryServiceID(fact.ApplicationID, fact.EnvironmentID)
		service := byService[id]
		if service == nil {
			service = &memoryService{ID: id, ApplicationID: fact.ApplicationID,
				EnvironmentID: fact.EnvironmentID, Name: app.Name, Key: app.Key, FactIDs: []string{}}
			byService[id] = service
		}
		service.FactIDs = append(service.FactIDs, fact.ID)
		service.Count++
	}
	for _, service := range byService {
		response.Services = append(response.Services, *service)
	}
	sort.Slice(response.Services, func(i, j int) bool {
		a, b := response.Services[i], response.Services[j]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.EnvironmentID != b.EnvironmentID {
			return a.EnvironmentID < b.EnvironmentID
		}
		return a.ID < b.ID
	})
	var selected *memoryService
	if len(values) == 1 {
		// A stable ID identifies one application/environment pair. The app ID
		// and key aliases are accepted only when no stable ID matches.
		for i := range response.Services {
			service := &response.Services[i]
			if values[0] == service.ID {
				selected = service
				break
			}
		}
		if selected == nil {
			for i := range response.Services {
				service := &response.Services[i]
				if values[0] != service.ApplicationID && values[0] != service.Key {
					continue
				}
				if selected != nil {
					httpapi.Error(w, 409, "ambiguous_service", "Service reference is not unique in this scope")
					return
				}
				selected = service
			}
		}
		if selected == nil {
			httpapi.Error(w, 404, "not_found", "Service has no visible source-backed facts")
			return
		}
		response.Service = &selected.ID
	}

	visible := map[string]Fact{}
	serviceIDs, workspaceIDs := []string{}, []string{}
	for _, fact := range snapshot.Facts {
		if selected != nil && (fact.ApplicationID != selected.ApplicationID || fact.EnvironmentID != selected.EnvironmentID) {
			continue
		}
		if len(sources) > 0 && !has(sources, fact.Source) {
			continue
		}
		// No confidence producer exists; unknown is the only matching facet.
		if len(confidence) > 0 && !has(confidence, "unknown") {
			continue
		}
		visible[fact.ID] = fact
		if fact.ApplicationID == "" {
			workspaceIDs = append(workspaceIDs, fact.ID)
		} else {
			serviceIDs = append(serviceIDs, fact.ID)
		}
		response.Facts = append(response.Facts, memoryViewFact{Fact: fact, SourceTitle: snapshot.sourceTitle(fact.Source),
			Evidence: memoryFactEvidence{DocumentID: fact.Source, Version: fact.SourceVersion}})
	}
	response.Layers = []memoryLayer{
		{ID: "service", Name: "Service facts", Count: len(serviceIDs),
			Description: "Current published facts attributed to visible services",
			Evidence:    memoryLayerEvidence{Kind: "current_published_facts", FactIDs: serviceIDs}},
		{ID: "workspace", Name: "Workspace facts", Count: len(workspaceIDs),
			Description: "Current published facts explicitly scoped to the workspace",
			Evidence:    memoryLayerEvidence{Kind: "current_published_facts", FactIDs: workspaceIDs}},
	}
	// Conflicts are human-recorded pairs. Both exact source-backed facts must
	// survive this request's authorization and optional service selection.
	for _, conflict := range snapshot.Conflicts {
		a, aVisible := visible[conflict.FactAID]
		b, bVisible := visible[conflict.FactBID]
		if !aVisible || !bVisible || a.EnvironmentID != conflict.EnvironmentID || b.EnvironmentID != conflict.EnvironmentID ||
			a.ApplicationID != conflict.ApplicationID || b.ApplicationID != conflict.ApplicationID ||
			a.TeamID != conflict.TeamID || b.TeamID != conflict.TeamID {
			continue
		}
		response.Conflicts = append(response.Conflicts, memoryViewConflict{
			ID: conflict.ID, State: "open", EnvironmentID: conflict.EnvironmentID,
			ApplicationID: conflict.ApplicationID, TeamID: conflict.TeamID,
			A: memoryConflictSide{ID: a.ID, Text: a.Fact, Source: a.Source, SourceTitle: snapshot.sourceTitle(a.Source)},
			B: memoryConflictSide{ID: b.ID, Text: b.Fact, Source: b.Source, SourceTitle: snapshot.sourceTitle(b.Source)},
		})
	}
	if len(response.Conflicts) > 0 {
		response.Conflict = &response.Conflicts[0]
	}
	httpapi.WriteJSON(w, 200, response)
}
