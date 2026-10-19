package main

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/destinywans/redstring/internal/httpapi"
)

type containerImage struct {
	Name  string `json:"name"`
	Image string `json:"image"`
}

// The browser needs image choices, not complete Pod templates (which may include
// inline environment credentials). The immutable proposal remains the execution
// authority; this read-only view never accepts a namespace or object from clients.
func templateImages(raw json.RawMessage) []containerImage {
	var template struct {
		Spec struct {
			Containers []containerImage `json:"containers"`
		} `json:"spec"`
	}
	if json.Unmarshal(raw, &template) != nil || template.Spec.Containers == nil {
		return []containerImage{}
	}
	return template.Spec.Containers
}

func registerPlatformDeployment(api *httpapi.Server, registry *platformRegistry) {
	api.Register("GET /api/v1/applications/{id}/deployment", "viewer", func(w http.ResponseWriter, r *http.Request) {
		env, registration, err := registry.resolve(r.Context(), r.PathValue("id"), r.URL.Query().Get("environment_id"))
		if err != nil {
			httpapi.Error(w, 404, "not_configured", "No Deployment registered for this application")
			return
		}
		connector, config := registration.connector, registration.Config
		_ = env
		snapshot, err := connector.Snapshot(r.Context())
		if err != nil {
			httpapi.Error(w, 503, "deployment_unavailable", "Registered Deployment snapshot unavailable")
			return
		}
		type revision struct {
			Revision string           `json:"revision"`
			Digest   string           `json:"digest"`
			Images   []containerImage `json:"images"`
		}
		revisions := []revision{}
		for _, item := range snapshot.Revisions {
			revisions = append(revisions, revision{Revision: item.Revision, Digest: item.Digest, Images: templateImages(item.Template)})
		}
		d := snapshot.Deployment
		httpapi.WriteJSON(w, 200, struct {
			ApplicationID      string           `json:"application_id"`
			Namespace          string           `json:"namespace"`
			Deployment         string           `json:"deployment"`
			UID                string           `json:"uid"`
			TemplateDigest     string           `json:"template_digest"`
			Images             []containerImage `json:"images"`
			Replicas           int              `json:"replicas"`
			ReadyReplicas      int              `json:"ready_replicas"`
			UpdatedReplicas    int              `json:"updated_replicas"`
			Generation         int64            `json:"generation"`
			ObservedGeneration int64            `json:"observed_generation"`
			Revisions          []revision       `json:"revisions"`
			CapturedAt         time.Time        `json:"captured_at"`
		}{r.PathValue("id"), config.Namespace, config.Deployment, d.Metadata.UID, snapshot.TemplateDigest, templateImages(d.Spec.Template), d.Spec.Replicas, d.Status.ReadyReplicas, d.Status.UpdatedReplicas, d.Metadata.Generation, d.Status.ObservedGeneration, revisions, snapshot.CapturedAt})
	})
}
