package v3discovery

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

var errCommandCatalogUnavailable = errors.New("command catalog is not configured")

// TextCommand is an explicitly configured slash-text suggestion. Selecting it
// inserts Name into the composer; this catalog does not dispatch an action or
// create a canvas, report, proposal, skill, runbook, or template.
type TextCommand struct {
	EnvironmentID string
	ID            string
	Name          string
	Module        string // conversation or canvas
}

type commandItem struct {
	ID    string `json:"id"`
	Group string `json:"group"`
	Name  string `json:"name"`
	Desc  string `json:"desc"`
	Icon  string `json:"icon"`
}

// ConfigureTextCommands replaces the startup catalog after validating and
// copying it. A nil catalog means that no trustworthy source was configured;
// an explicit empty catalog is a valid, empty result.
func (s *Service) ConfigureTextCommands(entries []TextCommand) error {
	if s == nil {
		return errCommandCatalogUnavailable
	}
	s.textCommands = nil
	if entries == nil {
		return nil
	}
	if len(entries) > 32 {
		return fmt.Errorf("command catalog exceeds 32 entries")
	}
	copyOf := make([]TextCommand, len(entries))
	ids, names := make(map[string]bool, len(entries)), make(map[string]bool, len(entries))
	for i, entry := range entries {
		if !validCommandAtom(entry.EnvironmentID, 128) || entry.EnvironmentID == "all" || !validCommandAtom(entry.ID, 80) ||
			!validSlashName(entry.Name) ||
			(entry.Module != "conversation" && entry.Module != "canvas") {
			return fmt.Errorf("invalid text command at index %d", i)
		}
		idKey, nameKey := entry.EnvironmentID+"\x00"+entry.ID, entry.EnvironmentID+"\x00"+entry.Name
		if ids[idKey] || names[nameKey] {
			return fmt.Errorf("duplicate text command at index %d", i)
		}
		ids[idKey], names[nameKey] = true, true
		copyOf[i] = entry
	}
	s.textCommands = copyOf
	return nil
}

func validCommandAtom(value string, max int) bool {
	if len(value) == 0 || len(value) > max {
		return false
	}
	for _, char := range value {
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.') {
			return false
		}
	}
	return true
}

func validSlashName(value string) bool {
	if len(value) < 2 || len(value) > 128 || value[0] != '/' || utf8.RuneCountInString(value[1:]) > 32 {
		return false
	}
	for _, char := range value[1:] {
		if !unicode.IsLetter(char) && !unicode.IsDigit(char) && char != '-' && char != '_' && char != '.' {
			return false
		}
	}
	return true
}

func (s *Service) commandItems(r *http.Request, envs []string) ([]commandItem, error) {
	if s == nil || s.textCommands == nil || s.Access == nil || r == nil || len(envs) != 1 {
		return nil, errCommandCatalogUnavailable
	}
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	user := httpapi.CurrentUser(r)
	if !ok || scope.All || len(scope.EnvironmentIDs) != 1 || scope.EnvironmentIDs[0] != envs[0] ||
		envs[0] == "" || user.ID == "" || user.Disabled {
		return nil, errCommandCatalogUnavailable
	}
	items := []commandItem{}
	desc := "Insert text in the composer; no action is executed."
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Accept-Language")), "en") {
		desc = "仅在输入框插入文字，不会执行操作。"
	}
	for _, entry := range s.textCommands {
		if entry.EnvironmentID != envs[0] {
			continue
		}
		err := s.Access.Authorize(r.Context(), user, rbac.V3Decision{
			Module: entry.Module, Action: "change", EnvironmentID: entry.EnvironmentID,
		})
		if errors.Is(err, store.ErrForbidden) {
			continue
		}
		if err != nil {
			return nil, err
		}
		items = append(items, commandItem{ID: entry.ID, Group: "command", Name: entry.Name, Desc: desc, Icon: "sparkles"})
	}
	return items, nil
}
