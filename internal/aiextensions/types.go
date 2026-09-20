// Package aiextensions implements product-owned, versioned AI extensions.
// Skills are reviewed instructions, never executable scripts or system prompts.
package aiextensions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/store"
)

var ErrInvalid = errors.New("invalid extension configuration")
var ErrUnavailable = errors.New("extension dependency unavailable")
var ident = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,100}$`)

type Binding struct {
	Name              string         `json:"name"`
	Tool              string         `json:"tool"`
	Description       string         `json:"description"`
	Arguments         map[string]any `json:"arguments"`
	ReadOnlyConfirmed bool           `json:"read_only_confirmed"`
}
type MCPConfig struct {
	Endpoint      string    `json:"endpoint"`
	CredentialRef string    `json:"credential_ref"`
	AllowHTTP     bool      `json:"allow_http"`
	Bindings      []Binding `json:"bindings"`
}
type Record struct {
	ID            string     `json:"id"`
	ApplicationID string     `json:"application_id"`
	Kind          string     `json:"kind"`
	Title         string     `json:"title"`
	Body          string     `json:"body"`
	Maintainer    string     `json:"maintainer"`
	RequiredTools []string   `json:"required_tools"`
	Enabled       bool       `json:"enabled"`
	Version       int        `json:"version"`
	ContentHash   string     `json:"content_hash"`
	UpdatedAt     time.Time  `json:"updated_at"`
	UpdatedBy     string     `json:"updated_by"`
	MCP           *MCPConfig `json:"mcp,omitempty"`
}
type Reference struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
}
type Composition struct {
	ApplicationID string      `json:"application_id"`
	References    []Reference `json:"references"`
	Question      string      `json:"question"`
}
type Composed struct {
	Content    string      `json:"content"`
	References []Reference `json:"references"`
}
type Repository interface {
	List(context.Context, string, string) ([]Record, error)
	Get(context.Context, string, int) (Record, error)
	Save(context.Context, Record, int) (Record, error)
}
type Service struct {
	Repository     Repository
	CredentialRoot string
	Authorize      func(context.Context, core.User, string, string) error
}

func (s *Service) authorize(ctx context.Context, app, permission string) error {
	if app == "" || s.Authorize == nil || store.Principal(ctx).ID == "" {
		return store.ErrForbidden
	}
	return s.Authorize(ctx, store.Principal(ctx), app, permission)
}

// Workspace instructions are readable by members but maintained only by admins.
// This must not replace authorize: composing and tool calls require an app target.
func (s *Service) authorizeRecord(ctx context.Context, r Record, permission string) error {
	if r.ApplicationID != "" {
		return s.authorize(ctx, r.ApplicationID, permission)
	}
	user := store.Principal(ctx)
	if user.ID == "" || (user.Role != "admin" && user.Role != "operator" && user.Role != "viewer") || (r.Kind != "skill" && r.Kind != "prompt") {
		return store.ErrForbidden
	}
	if permission != "view" && user.Role != "admin" {
		return store.ErrForbidden
	}
	return nil
}
func validate(r Record) error {
	if strings.TrimSpace(r.Title) == "" || len(r.Title) > 160 || strings.TrimSpace(r.Maintainer) == "" || len(r.Maintainer) > 160 || len(r.Body) > 24000 || len(r.RequiredTools) > 16 {
		return ErrInvalid
	}
	for _, t := range r.RequiredTools {
		if !ident.MatchString(t) {
			return ErrInvalid
		}
	}
	if r.Kind == "skill" || r.Kind == "prompt" {
		if r.MCP != nil || strings.TrimSpace(r.Body) == "" {
			return ErrInvalid
		}
		return nil
	}
	if r.Kind != "mcp" || r.ApplicationID == "" || r.MCP == nil || len(r.MCP.Bindings) > 8 {
		return ErrInvalid
	}
	c := r.MCP
	u, e := url.Parse(c.Endpoint)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(c.Endpoint) > 2000 {
		return ErrInvalid
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && c.AllowHTTP && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1")) {
		return ErrInvalid
	}
	if c.CredentialRef != "" && (!ident.MatchString(c.CredentialRef) || c.CredentialRef == "." || c.CredentialRef == "..") {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, b := range c.Bindings {
		if !ident.MatchString(b.Name) || !ident.MatchString(b.Tool) || !b.ReadOnlyConfirmed || seen[b.Name] || len(b.Description) > 1000 || b.Arguments == nil {
			return ErrInvalid
		}
		seen[b.Name] = true
		raw, e := json.Marshal(b.Arguments)
		if e != nil || len(raw) > 16384 {
			return ErrInvalid
		}
	}
	return nil
}
func hash(r Record) string {
	r.ContentHash = ""
	r.UpdatedAt = time.Time{}
	r.UpdatedBy = ""
	raw, _ := json.Marshal(r)
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}
