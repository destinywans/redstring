package v3settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

var errWorkspaceUnavailable = errors.New("workspace settings record is missing or invalid")

func validEditableWorkspace(v workspace) bool {
	return strings.TrimSpace(v.Name) != "" && utf8.RuneCountInString(v.Name) <= 120 &&
		slugPattern.MatchString(v.Slug) && validTimezone(v.Timezone) &&
		(v.Locale == "zh-CN" || v.Locale == "en-US") &&
		(v.WeekStart == "monday" || v.WeekStart == "sunday") &&
		v.RetentionDays >= 1 && v.RetentionDays <= 3650
}

func validStoredWorkspace(v workspace) bool {
	return validEditableWorkspace(v) && strings.TrimSpace(v.DataRegion) != "" &&
		strings.TrimSpace(v.Brand.Name) != "" &&
		(v.Brand.Logo == nil || strings.TrimSpace(*v.Brand.Logo) != "")
}

// The shared decoder writes English errors. Keep this route's body errors
// localized while retaining its size, unknown-field and single-object rules.
func decodeWorkspace(w http.ResponseWriter, r *http.Request) (workspace, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	decoder := json.NewDecoder(r.Body)
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_body", localize(r, "请求正文不是有效的 JSON 对象", "Invalid JSON body"))
		return workspace{}, false
	}
	if decoder.Decode(new(any)) != io.EOF {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_body", localize(r, "请求正文只能包含一个 JSON 对象", "Expected one JSON object"))
		return workspace{}, false
	}
	if len(raw) == 0 || bytes.TrimSpace(raw)[0] != '{' {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_body", localize(r, "请求正文必须是工作区对象", "Expected a workspace object"))
		return workspace{}, false
	}
	var v workspace
	strict := json.NewDecoder(bytes.NewReader(raw))
	strict.DisallowUnknownFields()
	if err := strict.Decode(&v); err != nil {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_body", localize(r, "工作区字段无效或未知", "Invalid or unknown workspace field"))
		return workspace{}, false
	}
	return v, true
}

func respondWorkspace(w http.ResponseWriter, r *http.Request, v workspace, err error) {
	if err == nil {
		httpapi.WriteJSON(w, http.StatusOK, v)
		return
	}
	switch {
	case errors.Is(err, errInvalid):
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_settings", localize(r, "请检查提交的工作区设置；部署管理字段不能在此修改", "Check the workspace settings; deployment-managed fields cannot be changed here"))
	case errors.Is(err, store.ErrForbidden):
		httpapi.Error(w, http.StatusForbidden, "forbidden", localize(r, "没有修改工作区设置的权限", "Permission to change workspace settings denied"))
	case errors.Is(err, errWorkspaceUnavailable), errors.Is(err, pgx.ErrNoRows):
		httpapi.Error(w, http.StatusServiceUnavailable, "workspace_unavailable", localize(r, "工作区设置记录缺失或损坏", "Workspace settings record is missing or invalid"))
	default:
		httpapi.Error(w, http.StatusServiceUnavailable, "settings_unavailable", localize(r, "工作区设置暂时不可用", "Workspace settings unavailable"))
	}
}
