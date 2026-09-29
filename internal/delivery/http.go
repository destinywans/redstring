package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

func (s *Service) Register(api *httpapi.Server) {
	if s.Authorize == nil {
		s.Authorize = func(ctx context.Context, actor, app, permission string) error {
			return api.AuthorizeApplication(ctx, core.User{ID: actor}, app, permission)
		}
	}
	if s.ValidateEnvironment == nil {
		s.ValidateEnvironment = func(ctx context.Context, app, env string) error {
			var allowed bool
			err := s.Repository.DB.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM application_environments WHERE id=$1 AND application_id=$2 AND NOT archived)", env, app).Scan(&allowed)
			if err != nil {
				return err
			}
			if !allowed {
				return store.ErrForbidden
			}
			return nil
		}
	}
	api.Register("GET /api/v1/delivery/catalog", "viewer", func(w http.ResponseWriter, r *http.Request) {
		sources := []map[string]any{}
		for id, c := range s.GitLabs {
			bindings := []GitLabBinding{}
			for _, b := range c.Config.Bindings {
				if s.access(r.Context(), httpapi.CurrentUser(r).ID, b.ApplicationID, "view") == nil {
					bindings = append(bindings, b)
				}
			}
			if len(bindings) > 0 {
				sources = append(sources, map[string]any{"id": id, "name": c.Config.Name, "bindings": bindings, "configuration_status": "configured_unverified"})
			}
		}
		sort.Slice(sources, func(i, j int) bool { return fmt.Sprint(sources[i]["id"]) < fmt.Sprint(sources[j]["id"]) })
		httpapi.WriteJSON(w, 200, map[string]any{"gitlab_sources": sources, "native_enabled": s.Argo != nil, "artifact_store_configured": s.Artifacts != nil, "native_deployment_enabled": s.Release != nil, "templates": Templates()})
	})
	api.Register("GET /api/v1/delivery/pipelines", "viewer", func(w http.ResponseWriter, r *http.Request) {
		apps, err := selectedApps(api, r)
		if err != nil {
			respond(w, nil, err)
			return
		}
		scope, _ := httpapi.ParseReadScope(r.URL.Query())
		v, err := s.Repository.Pipelines(r.Context(), apps, strings.Join(scope.EnvironmentIDs, ","))
		respond(w, map[string]any{"items": v}, err)
	})
	api.Register("GET /api/v1/delivery/pipelines/{id}", "viewer", func(w http.ResponseWriter, r *http.Request) {
		version, err := intQuery(r, "version", 0)
		if err != nil {
			respond(w, nil, err)
			return
		}
		v, err := s.Repository.Pipeline(r.Context(), r.PathValue("id"), version)
		if err == nil {
			err = s.access(r.Context(), httpapi.CurrentUser(r).ID, v.ApplicationID, "view")
		}
		respond(w, v, err)
	})
	save := func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ApplicationID   string     `json:"application_id"`
			EnvironmentID   string     `json:"environment_id"`
			Name            string     `json:"name"`
			Definition      Definition `json:"definition"`
			ExpectedVersion int64      `json:"expected_version"`
		}
		if !httpapi.Decode(w, r, &in) {
			return
		}
		v, err := s.SavePipeline(r.Context(), Pipeline{ID: r.PathValue("id"), ApplicationID: in.ApplicationID, EnvironmentID: in.EnvironmentID, Name: in.Name, Definition: in.Definition}, in.ExpectedVersion, httpapi.CurrentUser(r).ID)
		respond(w, v, err)
	}
	api.Register("POST /api/v1/delivery/pipelines", "operator", save)
	api.Register("POST /api/v1/delivery/pipelines/{id}/versions", "operator", save)
	api.Register("GET /api/v1/delivery/runs", "viewer", func(w http.ResponseWriter, r *http.Request) {
		apps, err := selectedApps(api, r)
		if err != nil {
			respond(w, nil, err)
			return
		}
		scope, _ := httpapi.ParseReadScope(r.URL.Query())
		v, err := s.Repository.Runs(r.Context(), apps, strings.Join(scope.EnvironmentIDs, ","))
		respond(w, map[string]any{"items": v}, err)
	})
	api.Register("POST /api/v1/delivery/runs", "operator", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			PipelineID string `json:"pipeline_id"`
			Version    int64  `json:"version"`
		}
		if !httpapi.Decode(w, r, &in) {
			return
		}
		v, err := s.Start(r.Context(), in.PipelineID, in.Version, httpapi.CurrentUser(r).ID, r.Header.Get("Idempotency-Key"))
		respond(w, v, err)
	})
	api.Register("GET /api/v1/delivery/runs/{id}", "viewer", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Get(r.Context(), r.PathValue("id"), httpapi.CurrentUser(r).ID, "view")
		respond(w, v, err)
	})
	api.Register("GET /api/v1/delivery/runs/{id}/events", "viewer", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Get(r.Context(), r.PathValue("id"), httpapi.CurrentUser(r).ID, "view")
		if err != nil {
			respond(w, nil, err)
			return
		}
		after, err := intQuery(r, "after", 0)
		if err != nil {
			respond(w, nil, err)
			return
		}
		events, err := s.Repository.Events(r.Context(), v.ID, after)
		respond(w, map[string]any{"items": events}, err)
	})
	api.Register("GET /api/v1/delivery/runs/{id}/commands", "viewer", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Get(r.Context(), r.PathValue("id"), httpapi.CurrentUser(r).ID, "view")
		if err != nil {
			respond(w, nil, err)
			return
		}
		commands, err := s.Repository.Commands(r.Context(), v.ID)
		respond(w, map[string]any{"items": commands}, err)
	})
	api.Register("GET /api/v1/delivery/runs/{id}/gates", "viewer", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Get(r.Context(), r.PathValue("id"), httpapi.CurrentUser(r).ID, "view")
		if err != nil {
			respond(w, nil, err)
			return
		}
		gates, err := s.Repository.Gates(r.Context(), v.ID)
		respond(w, map[string]any{"items": gates}, err)
	})
	api.Register("GET /api/v1/delivery/pipelines/{id}/versions", "viewer", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Repository.Pipeline(r.Context(), r.PathValue("id"), 0)
		if err == nil {
			err = s.access(r.Context(), httpapi.CurrentUser(r).ID, v.ApplicationID, "view")
		}
		if err != nil {
			respond(w, nil, err)
			return
		}
		versions, err := s.Repository.Versions(r.Context(), v.ID)
		respond(w, map[string]any{"items": versions}, err)
	})
	api.Register("POST /api/v1/delivery/runs/{id}/commands", "operator", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Kind  string `json:"kind"`
			JobID int64  `json:"job_id"`
		}
		if !httpapi.Decode(w, r, &in) {
			return
		}
		v, err := s.Command(r.Context(), r.PathValue("id"), in.Kind, in.JobID, httpapi.CurrentUser(r).ID, r.Header.Get("Idempotency-Key"))
		respond(w, v, err)
	})
	api.Register("GET /api/v1/delivery/runs/{id}/logs", "viewer", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Get(r.Context(), r.PathValue("id"), httpapi.CurrentUser(r).ID, "view")
		if err != nil {
			respond(w, nil, err)
			return
		}
		if v.Provider != "native" || s.Argo == nil {
			respond(w, nil, ErrUnsupported)
			return
		}
		cursor, err := intQuery(r, "cursor", 0)
		if err != nil {
			respond(w, nil, err)
			return
		}
		logs, err := s.Logs(r.Context(), v, r.URL.Query().Get("node_id"), cursor)
		respond(w, logs, err)
	})
	s.registerGitLab(api)
	s.registerEvents(api)
	s.registerRetention(api)
}
func selectedApps(api *httpapi.Server, r *http.Request) ([]string, error) {
	visible, err := api.VisibleApplicationIDs(r.Context(), httpapi.CurrentUser(r))
	if err != nil {
		return nil, err
	}
	query := r.URL.Query()
	if _, ok := query["applications"]; !ok {
		if ids, exists := query["application_ids"]; exists {
			query["applications"] = ids
		}
	}
	scope, err := httpapi.ParseReadScope(query)
	if err != nil {
		return nil, ErrInvalid
	}
	if len(scope.ApplicationIDs) == 0 {
		return visible, nil
	}
	selected := scope.ApplicationIDs
	for _, id := range selected {
		if !contains(visible, id) {
			return nil, store.ErrForbidden
		}
	}
	return selected, nil
}
func intQuery(r *http.Request, name string, fallback int64) (int64, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 0 {
		return 0, ErrInvalid
	}
	return v, nil
}
func pathID(r *http.Request, name string) (int64, error) {
	v, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || v <= 0 {
		return 0, ErrInvalid
	}
	return v, nil
}
func (s *Service) gitlabAccess(r *http.Request, permission string) (*GitLabClient, GitLabBinding, error) {
	c := s.GitLabs[r.PathValue("source")]
	if c == nil {
		return nil, GitLabBinding{}, store.ErrNotFound
	}
	project, err := pathID(r, "project")
	if err != nil {
		return nil, GitLabBinding{}, err
	}
	b, ok := c.Binding(project)
	if !ok {
		return nil, b, store.ErrNotFound
	}
	if err = s.access(r.Context(), httpapi.CurrentUser(r).ID, b.ApplicationID, permission); err != nil {
		return nil, b, err
	}
	return c, b, nil
}
func (s *Service) registerGitLab(api *httpapi.Server) {
	api.Register("GET /api/v1/delivery/gitlab/{source}/projects/{project}", "viewer", func(w http.ResponseWriter, r *http.Request) {
		c, b, err := s.gitlabAccess(r, "view")
		if err != nil {
			respond(w, nil, err)
			return
		}
		v, err := c.Project(r.Context(), b.ProjectID)
		respond(w, v, err)
	})
	api.Register("GET /api/v1/delivery/gitlab/{source}/projects/{project}/pipelines", "viewer", func(w http.ResponseWriter, r *http.Request) {
		c, b, err := s.gitlabAccess(r, "view")
		if err != nil {
			respond(w, nil, err)
			return
		}
		page, err := intQuery(r, "page", 1)
		if err != nil {
			respond(w, nil, err)
			return
		}
		v, err := c.Pipelines(r.Context(), b.ProjectID, int(page))
		respond(w, v, err)
	})
	api.Register("GET /api/v1/delivery/gitlab/{source}/projects/{project}/pipelines/{pipeline}", "viewer", func(w http.ResponseWriter, r *http.Request) {
		c, b, err := s.gitlabAccess(r, "view")
		if err != nil {
			respond(w, nil, err)
			return
		}
		id, err := pathID(r, "pipeline")
		if err != nil {
			respond(w, nil, err)
			return
		}
		v, err := c.Pipeline(r.Context(), b.ProjectID, id)
		respond(w, v, err)
	})
	api.Register("GET /api/v1/delivery/gitlab/{source}/projects/{project}/pipelines/{pipeline}/jobs", "viewer", func(w http.ResponseWriter, r *http.Request) {
		c, b, err := s.gitlabAccess(r, "view")
		if err != nil {
			respond(w, nil, err)
			return
		}
		id, err := pathID(r, "pipeline")
		if err != nil {
			respond(w, nil, err)
			return
		}
		page, err := intQuery(r, "page", 1)
		if err != nil {
			respond(w, nil, err)
			return
		}
		v, err := c.Jobs(r.Context(), b.ProjectID, id, int(page))
		respond(w, v, err)
	})
	api.Register("GET /api/v1/delivery/gitlab/{source}/projects/{project}/jobs/{job}/logs", "viewer", func(w http.ResponseWriter, r *http.Request) {
		c, b, err := s.gitlabAccess(r, "view")
		if err != nil {
			respond(w, nil, err)
			return
		}
		job, err := pathID(r, "job")
		if err != nil {
			respond(w, nil, err)
			return
		}
		cursor, err := intQuery(r, "cursor", 0)
		if err != nil {
			respond(w, nil, err)
			return
		}
		v, err := c.Trace(r.Context(), b.ProjectID, job, cursor)
		respond(w, v, err)
	})
	api.Register("POST /api/v1/delivery/gitlab/{source}/projects/{project}/pipelines/{pipeline}/import", "operator", func(w http.ResponseWriter, r *http.Request) {
		c, b, err := s.gitlabAccess(r, "operate")
		if err != nil {
			respond(w, nil, err)
			return
		}
		id, err := pathID(r, "pipeline")
		if err != nil {
			respond(w, nil, err)
			return
		}
		v, err := s.ImportGitLab(r.Context(), c, b, id, httpapi.CurrentUser(r).ID)
		respond(w, v, err)
	})
	api.RegisterPublic("POST /api/v1/webhooks/gitlab/{source}", s.webhook)
}
func (s *Service) ImportGitLab(ctx context.Context, c *GitLabClient, b GitLabBinding, id int64, actor string) (Run, error) {
	if err := s.access(ctx, actor, b.ApplicationID, "operate"); err != nil {
		return Run{}, err
	}
	if err := s.validTarget(ctx, b.ApplicationID, b.EnvironmentID); err != nil {
		return Run{}, err
	}
	existing, err := s.Repository.GitLabRuns(ctx, c.Config.ID, b.ProjectID, strconv.FormatInt(id, 10))
	if err != nil {
		return Run{}, err
	}
	if len(existing) > 0 {
		return existing[0], nil
	}
	remote, err := c.Pipeline(ctx, b.ProjectID, id)
	if err != nil {
		return Run{}, err
	}
	projection, err := c.Observe(ctx, b.ProjectID, id)
	if err != nil {
		return Run{}, err
	}
	pipe, err := s.Repository.SavePipeline(ctx, Pipeline{ApplicationID: b.ApplicationID, EnvironmentID: b.EnvironmentID, Name: fmt.Sprintf("GitLab #%d", id), CreatedBy: actor, Definition: Definition{Provider: "gitlab", SourceID: c.Config.ID, ProjectID: b.ProjectID, Ref: remote.Ref}}, 0)
	if err != nil {
		return Run{}, err
	}
	return s.Repository.ImportRun(ctx, pipe, projection, actor, fmt.Sprintf("import:%s:%d:%d", c.Config.ID, b.ProjectID, id))
}
func (s *Service) webhook(w http.ResponseWriter, r *http.Request) {
	c := s.GitLabs[r.PathValue("source")]
	if c == nil || !c.AcceptWebhook(r.Header.Get("X-Gitlab-Token")) {
		httpapi.Error(w, 401, "unauthorized", "Invalid webhook identity")
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		respond(w, nil, ErrInvalid)
		return
	}
	var in struct {
		ObjectKind string `json:"object_kind"`
		Project    struct {
			ID int64 `json:"id"`
		} `json:"project"`
		ProjectID        int64 `json:"project_id"`
		ObjectAttributes struct {
			ID int64 `json:"id"`
		} `json:"object_attributes"`
		PipelineID int64 `json:"pipeline_id"`
	}
	if json.Unmarshal(raw, &in) != nil {
		respond(w, nil, ErrInvalid)
		return
	}
	if in.ObjectKind != "pipeline" && in.ObjectKind != "build" {
		httpapi.WriteJSON(w, 202, map[string]bool{"ignored": true})
		return
	}
	project := in.Project.ID
	if project == 0 {
		project = in.ProjectID
	}
	binding, ok := c.Binding(project)
	if !ok {
		httpapi.Error(w, 403, "forbidden", "Project is not registered")
		return
	}
	external := in.ObjectAttributes.ID
	if in.ObjectKind == "build" {
		external = in.PipelineID
	}
	if external <= 0 {
		respond(w, nil, ErrInvalid)
		return
	}
	event := r.Header.Get("X-Gitlab-Event-UUID")
	if event == "" {
		event = hash(string(raw))
	}
	if len(event) > 200 {
		respond(w, nil, ErrInvalid)
		return
	}
	var fresh bool
	if len(s.V3WebhookEnvironments) > 0 {
		fresh, err = s.Repository.RecordV3Webhook(r.Context(), c.Config.ID, event, project,
			strconv.FormatInt(external, 10), binding.ApplicationID, binding.EnvironmentID, s.V3WebhookEnvironments)
	} else {
		if err = s.validTarget(r.Context(), binding.ApplicationID, binding.EnvironmentID); err == nil {
			fresh, err = s.Repository.RecordWebhook(r.Context(), c.Config.ID, event, project, strconv.FormatInt(external, 10))
		}
	}
	if err != nil {
		respond(w, nil, err)
		return
	}
	// Persist only identity; the worker obtains authoritative status from GitLab.
	// A forged/out-of-order status in a webhook never regresses product state.
	httpapi.WriteJSON(w, 202, map[string]bool{"accepted": true, "duplicate": !fresh})
}
func respond(w http.ResponseWriter, v any, err error) {
	if err != nil {
		switch {
		case errors.Is(err, store.ErrForbidden):
			httpapi.Error(w, 403, "forbidden", "Application permission required")
		case errors.Is(err, pgx.ErrNoRows) || errors.Is(err, store.ErrNotFound):
			httpapi.Error(w, 404, "not_found", "Delivery resource not found")
		case errors.Is(err, store.ErrConflict):
			httpapi.Error(w, 409, "conflict", "Version, idempotency key or run state changed")
		case errors.Is(err, ErrInvalid):
			httpapi.Error(w, 422, "invalid_delivery_input", "Invalid delivery definition or request")
		case errors.Is(err, ErrUnsupported):
			httpapi.Error(w, 409, "capability_unavailable", "Registered provider or policy does not support this operation")
		default:
			httpapi.Error(w, 503, "delivery_unavailable", "Delivery provider or storage is unavailable")
		}
		return
	}
	httpapi.WriteJSON(w, 200, v)
}
