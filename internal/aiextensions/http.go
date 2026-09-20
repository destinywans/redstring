package aiextensions

import (
	"errors"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
	"net/http"
	"os"
	"strconv"
)

func (s *Service) Register(api *httpapi.Server) {
	s.Authorize = api.AuthorizeApplication
	api.Register("GET /api/v1/ai-extensions/mcp-capabilities", "admin", func(w http.ResponseWriter, r *http.Request) {
		info, err := os.Stat(s.CredentialRoot)
		ready := err == nil && info.IsDir() && info.Mode().Perm()&0077 == 0
		respond(w, map[string]any{"transport": "streamable_http", "credential_storage_configured": ready, "fixed_arguments_only": true, "maximum_tools": 8}, nil)
	})
	api.Register("GET /api/v1/ai-extensions/examples", "viewer", func(w http.ResponseWriter, r *http.Request) { respond(w, map[string]any{"items": Examples()}, nil) })
	api.Register("GET /api/v1/ai-extensions", "viewer", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.List(r.Context(), r.URL.Query().Get("application_id"), r.URL.Query().Get("kind"))
		respond(w, map[string]any{"items": v}, e)
	})
	api.Register("GET /api/v1/ai-extensions/{id}", "viewer", func(w http.ResponseWriter, r *http.Request) {
		version := 0
		if v := r.URL.Query().Get("version"); v != "" {
			n, e := strconv.Atoi(v)
			if e != nil || n < 1 {
				respond(w, nil, ErrInvalid)
				return
			}
			version = n
		}
		v, e := s.Get(r.Context(), r.PathValue("id"), version)
		respond(w, v, e)
	})
	api.Register("POST /api/v1/ai-extensions", "operator", func(w http.ResponseWriter, r *http.Request) {
		var in Record
		if !httpapi.Decode(w, r, &in) {
			return
		}
		if in.ID != "" {
			respond(w, nil, ErrInvalid)
			return
		}
		v, e := s.Save(r.Context(), in)
		respond(w, v, e)
	})
	api.Register("PUT /api/v1/ai-extensions/{id}", "operator", func(w http.ResponseWriter, r *http.Request) {
		var in Record
		if !httpapi.Decode(w, r, &in) {
			return
		}
		if in.ID != "" && in.ID != r.PathValue("id") {
			respond(w, nil, ErrInvalid)
			return
		}
		in.ID = r.PathValue("id")
		v, e := s.Save(r.Context(), in)
		respond(w, v, e)
	})
	api.Register("POST /api/v1/ai-extensions/compose", "operator", func(w http.ResponseWriter, r *http.Request) {
		var in Composition
		if !httpapi.Decode(w, r, &in) {
			return
		}
		v, e := s.Compose(r.Context(), in)
		respond(w, v, e)
	})
	api.Register("POST /api/v1/ai-extensions/{id}/discover", "admin", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.Discover(r.Context(), r.PathValue("id"))
		respond(w, map[string]any{"items": v}, e)
	})
	api.Register("POST /api/v1/ai-extensions/{id}/test", "admin", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Binding string `json:"binding"`
		}
		if !httpapi.Decode(w, r, &in) {
			return
		}
		v, e := s.Test(r.Context(), r.PathValue("id"), in.Binding)
		respond(w, v, e)
	})
}
func respond(w http.ResponseWriter, v any, e error) {
	if e == nil {
		httpapi.WriteJSON(w, 200, v)
		return
	}
	status, code, message := 503, "extension_unavailable", "扩展服务不可用，请检查管理员登记的服务与凭据。"
	switch {
	case errors.Is(e, store.ErrForbidden):
		status, code, message = 403, "forbidden", "没有该应用或扩展的权限。"
	case errors.Is(e, store.ErrNotFound):
		status, code, message = 404, "not_found", "扩展或版本不存在。"
	case errors.Is(e, store.ErrConflict):
		status, code, message = 409, "version_conflict", "扩展版本或启用状态已变化，请刷新。"
	case errors.Is(e, ErrInvalid):
		status, code, message = 422, "invalid_extension", "请检查字段、固定工具参数和技能要求的工具是否已配置。"
	}
	httpapi.Error(w, status, code, message)
}
