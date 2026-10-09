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
var errWorkspaceRevisionConflict = errors.New("workspace settings revision changed")

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
	fields, err := requiredWorkspaceFields(raw, []string{"name", "slug", "timezone", "locale", "week_start", "retention_days", "data_region", "brand"})
	if err != nil {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_body", localize(r, "工作区字段缺失、重复或无效", "Missing, duplicate, or invalid workspace field"))
		return workspace{}, false
	}
	if _, err := requiredWorkspaceFields(fields["brand"], []string{"name", "logo", "pending"}); err != nil {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_body", localize(r, "品牌字段缺失、重复或无效", "Missing, duplicate, or invalid brand field"))
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

// The PUT body is the complete frozen DTO, including the read-only deployment
// fields that the server compares against the current row. Explicit presence
// matters: zero values must not silently stand in for omitted fields.
func requiredWorkspaceFields(raw []byte, names []string) (map[string]json.RawMessage, error) {
	return requiredFields(raw, names, nil, map[string]bool{"logo": true})
}

// requiredFields decodes one object that has every name exactly once, may have the
// optional names, and nothing else. Only nullable names may be null.
func requiredFields(raw []byte, names, optional []string, nullable map[string]bool) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	open, err := decoder.Token()
	if err != nil || open != json.Delim('{') {
		return nil, errInvalid
	}
	allowed := make(map[string]bool, len(names)+len(optional))
	for _, name := range names {
		allowed[name] = true
	}
	for _, name := range optional {
		allowed[name] = true
	}
	fields := make(map[string]json.RawMessage, len(names))
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, errInvalid
		}
		key, ok := keyToken.(string)
		if !ok || !allowed[key] || fields[key] != nil {
			return nil, errInvalid
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) && !nullable[key] {
			return nil, errInvalid
		}
		fields[key] = value
	}
	close, err := decoder.Token()
	if err != nil || close != json.Delim('}') {
		return nil, errInvalid
	}
	for _, name := range names {
		if _, ok := fields[name]; !ok {
			return nil, errInvalid
		}
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, errInvalid
	}
	return fields, nil
}

func respondWorkspace(w http.ResponseWriter, r *http.Request, v workspace, err error) {
	if err == nil {
		httpapi.WriteJSON(w, http.StatusOK, v)
		return
	}
	switch {
	case errors.Is(err, errInvalid):
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_settings", localize(r, "请检查提交的工作区设置；部署管理字段不能在此修改", "Check the workspace settings; deployment-managed fields cannot be changed here"))
	case errors.Is(err, errWorkspaceRevisionConflict):
		httpapi.Error(w, http.StatusPreconditionFailed, "workspace_changed", localize(r, "工作区设置已被他人修改，请刷新后重试", "Workspace settings changed; refresh and try again"))
	case errors.Is(err, store.ErrForbidden):
		if r.Method == http.MethodGet {
			httpapi.Error(w, http.StatusForbidden, "forbidden", localize(r, "没有读取工作区设置的权限", "Permission to view workspace settings denied"))
		} else {
			httpapi.Error(w, http.StatusForbidden, "forbidden", localize(r, "没有修改工作区设置的权限", "Permission to change workspace settings denied"))
		}
	case errors.Is(err, errWorkspaceUnavailable), errors.Is(err, pgx.ErrNoRows):
		httpapi.Error(w, http.StatusServiceUnavailable, "workspace_unavailable", localize(r, "工作区设置记录缺失或损坏", "Workspace settings record is missing or invalid"))
	default:
		httpapi.Error(w, http.StatusServiceUnavailable, "settings_unavailable", localize(r, "工作区设置暂时不可用", "Workspace settings unavailable"))
	}
}
