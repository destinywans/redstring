package v3knowledgesources

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3sourceregistry"
	"github.com/wans/aiops-canvas/internal/v3uploads"
)

// The main route accepts only a public Markdown page or an exact configured
// Git, Confluence or Notion binding per application. Registration does not
// start a read, sync, or vector index.
type mainCreateRequest struct {
	Name          string   `json:"name"`
	Type          string   `json:"type"`
	URL           string   `json:"url"`
	SecretRef     string   `json:"secret_ref"`
	Spaces        []string `json:"spaces"`
	Schedule      string   `json:"schedule"`
	Chunk         string   `json:"chunk"`
	InheritACL    *bool    `json:"inherit_acl"`
	ApplicationID string   `json:"application_id"`
	TeamID        string   `json:"team_id"`
}

var mainSourceID = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)

func validateMainCreate(v mainCreateRequest) (int, string, string) {
	switch v.Type {
	case "git":
		return http.StatusNotImplemented, "source_adapter_unavailable", "This source type has no configured reader and importer"
	case "web", "git_markdown", "confluence", "notion", "feishu", "upload":
	default:
		return http.StatusUnprocessableEntity, "invalid_source_registration", "Unsupported source type"
	}
	if v.Type == "web" && v.SecretRef != "" {
		return http.StatusNotImplemented, "source_adapter_unavailable", "Credentialed Web sources have no configured reader and importer"
	}
	if v.Name == "" || strings.TrimSpace(v.Name) != v.Name || len([]rune(v.Name)) > 160 ||
		v.ApplicationID == "" || !mainSourceID.MatchString(v.ApplicationID) ||
		(v.TeamID != "" && !mainSourceID.MatchString(v.TeamID)) ||
		v.Schedule != "manual" ||
		(v.Chunk != "heading" && v.Chunk != "fixed") ||
		v.InheritACL == nil || !*v.InheritACL {
		return http.StatusUnprocessableEntity, "invalid_source_registration", "Source requires a name, application, manual sync and inherited access"
	}
	if v.Type == "confluence" {
		if len(v.Spaces) != 1 || !externalNumber.MatchString(v.Spaces[0]) {
			return http.StatusUnprocessableEntity, "invalid_source_registration", "Confluence requires one configured numeric space"
		}
	} else if len(v.Spaces) != 0 {
		return http.StatusUnprocessableEntity, "invalid_source_registration", "This source requires an empty spaces list"
	}
	if v.Type == "web" {
		if _, _, _, err := v3sourceregistry.ParsePublicWebURL(v.URL); err != nil {
			return http.StatusUnprocessableEntity, "invalid_source_registration", "Source URL must be a public HTTPS Markdown URL"
		}
	} else if v.Type == "upload" {
		if v.SecretRef != "" {
			return http.StatusUnprocessableEntity, "invalid_source_registration", "Local upload has no Vault credential"
		}
		if _, ok := v3uploads.ParseSourceUploadURL(v.URL); !ok {
			return http.StatusUnprocessableEntity, "invalid_source_registration", "Source URL must identify one stored upload"
		}
	} else if v.SecretRef == "" || !strings.HasPrefix(v.SecretRef, "vault://") || len(v.SecretRef) > 300 {
		return http.StatusUnprocessableEntity, "invalid_source_registration", "Credentialed source requires a vault://mount/path#field reference"
	}
	return 0, "", ""
}

var errMainCreateSession = errors.New("source registration session expired")

// Lock the session and physical grant sources through the registration commit.
// This makes revocation and registration mutually ordered instead of relying
// on a materialized grant view read before the transaction.
func lockMainCreateAuthority(ctx context.Context, tx pgx.Tx, r *http.Request) error {
	cookie, err := r.Cookie("aiops_session")
	if err != nil {
		return errMainCreateSession
	}
	var actor string
	err = tx.QueryRow(ctx, `SELECT u.id FROM users u JOIN sessions s ON s.user_id=u.id
 WHERE s.token_hash=$1 AND u.id=$2 AND s.expires_at>clock_timestamp()
 AND NOT u.disabled AND u.deleted_at IS NULL AND u.source<>'settings_demo'
 FOR SHARE OF u,s`, store.Hash(cookie.Value), httpapi.CurrentUser(r).ID).Scan(&actor)
	if errors.Is(err, pgx.ErrNoRows) {
		return errMainCreateSession
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `LOCK TABLE rbac_user_roles,rbac_department_roles,v3_role_grants IN SHARE MODE`)
	return err
}

func currentMainCreateSession(ctx context.Context, tx pgx.Tx, r *http.Request) error {
	cookie, err := r.Cookie("aiops_session")
	if err != nil {
		return errMainCreateSession
	}
	var valid bool
	err = tx.QueryRow(ctx, `SELECT expires_at>clock_timestamp() FROM sessions
 WHERE token_hash=$1 AND user_id=$2`, store.Hash(cookie.Value), httpapi.CurrentUser(r).ID).Scan(&valid)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !valid {
		return errMainCreateSession
	}
	return err
}

func mainCreateFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errMainCreateSession):
		httpapi.Error(w, http.StatusUnauthorized, "unauthorized", "Session expired")
	case errors.Is(err, store.ErrForbidden):
		httpapi.Error(w, http.StatusForbidden, "forbidden", "Knowledge management permission required")
	default:
		httpapi.Error(w, http.StatusServiceUnavailable, "source_registry_unavailable", "Source registration or policy is unavailable")
	}
}

func (s *Service) createMain(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.db == nil || s.access == nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "source_registry_unavailable", "Source registration or policy is unavailable")
		return
	}
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	user := httpapi.CurrentUser(r)
	if !ok || len(scope.EnvironmentIDs) == 0 || user.ID == "" {
		httpapi.Error(w, http.StatusServiceUnavailable, "environment_scope_unavailable", "Environment scope is unavailable")
		return
	}
	if scope.All || len(scope.EnvironmentIDs) != 1 {
		httpapi.Error(w, http.StatusConflict, "env_view_only", "Select one environment for a source registration")
		return
	}
	var v mainCreateRequest
	if !httpapi.Decode(w, r, &v) {
		return
	}
	if status, code, message := validateMainCreate(v); status != 0 {
		httpapi.Error(w, status, code, message)
		return
	}
	ctx := r.Context()
	env := scope.EnvironmentIDs[0]
	tx, err := s.db.Begin(ctx)
	if err != nil {
		mainCreateFailure(w, err)
		return
	}
	defer tx.Rollback(ctx)
	if err := lockMainCreateAuthority(ctx, tx, r); err != nil {
		mainCreateFailure(w, err)
		return
	}
	// Authorize the requested identifiers before checking whether they exist or
	// match. An object-scoped route has no route-level module grant check, so a
	// caller without manage access must not enumerate applications or teams.
	decision := rbac.V3Decision{Module: "knowledge", Action: "manage", EnvironmentID: env, ApplicationID: v.ApplicationID}
	if err := s.access.AuthorizeWithTx(ctx, tx, user, decision); err != nil {
		mainCreateFailure(w, err)
		return
	}
	if v.TeamID != "" {
		decision.ApplicationID = ""
		decision.TeamID = v.TeamID
		if err := s.access.AuthorizeWithTx(ctx, tx, user, decision); err != nil {
			mainCreateFailure(w, err)
			return
		}
	}
	var ownerTeam string
	err = tx.QueryRow(ctx, `SELECT COALESCE(a.owner_team_id,'') FROM applications a
 JOIN v3_application_environments mapped ON mapped.application_id=a.id AND mapped.environment_id=$2
 JOIN application_environments env ON env.application_id=a.id AND env.key=$2 AND NOT env.archived
 WHERE a.id=$1 AND NOT a.archived
 FOR SHARE OF a,mapped,env`, v.ApplicationID, env).Scan(&ownerTeam)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && v.TeamID != "" && v.TeamID != ownerTeam {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_source_scope", "Application/team is not active in the selected environment")
		return
	}
	if err != nil {
		mainCreateFailure(w, err)
		return
	}
	bindingFingerprint := ""
	bindingSnapshot := "{}"
	if v.Type == "git_markdown" {
		matched := false
		for _, binding := range s.gitMarkdownBindings {
			if BindingMatchesCreate(binding, v.URL, v.SecretRef, env, v.ApplicationID, v.TeamID) {
				snapshot, snapshotErr := SnapshotGitMarkdownBinding(binding)
				if snapshotErr != nil {
					mainCreateFailure(w, snapshotErr)
					return
				}
				bindingFingerprint = GitMarkdownBindingFingerprint(binding)
				bindingSnapshot = string(snapshot)
				matched = true
				break
			}
		}
		if !matched {
			httpapi.Error(w, http.StatusNotImplemented, "source_adapter_unavailable", "No Git Markdown reader is configured for this source scope")
			return
		}
	}
	if v.Type == "confluence" || v.Type == "notion" {
		matched := false
		for _, binding := range s.externalBindings {
			if !ExternalBindingMatchesCreate(binding, v.Type, v.URL, v.SecretRef, env, v.ApplicationID, v.TeamID, v.Spaces) {
				continue
			}
			snapshot, snapshotErr := SnapshotExternalBinding(binding)
			if snapshotErr != nil {
				mainCreateFailure(w, snapshotErr)
				return
			}
			bindingFingerprint = ExternalBindingFingerprint(binding)
			bindingSnapshot = string(snapshot)
			matched = true
			break
		}
		if !matched {
			httpapi.Error(w, http.StatusNotImplemented, "source_adapter_unavailable", "No reader is configured for this source scope")
			return
		}
	}
	if v.Type == "feishu" {
		matched := false
		for _, binding := range s.feishuBindings {
			if binding.SourceURL != v.URL || binding.CredentialRef != v.SecretRef ||
				binding.EnvironmentID != env || binding.ApplicationID != v.ApplicationID ||
				binding.TeamID != v.TeamID {
				continue
			}
			snapshot, snapshotErr := SnapshotFeishuBinding(binding)
			if snapshotErr != nil {
				mainCreateFailure(w, snapshotErr)
				return
			}
			bindingFingerprint = FeishuBindingFingerprint(binding)
			bindingSnapshot = string(snapshot)
			matched = true
			break
		}
		if !matched {
			httpapi.Error(w, http.StatusNotImplemented, "source_adapter_unavailable", "No Feishu Docx reader is configured for this source scope")
			return
		}
	}
	if v.Type == "upload" {
		uploadID, _ := v3uploads.ParseSourceUploadURL(v.URL)
		file, fileErr := v3uploads.LoadSourceFileTx(ctx, tx, uploadID, user.ID, env)
		if fileErr != nil {
			httpapi.Error(w, http.StatusUnprocessableEntity, "source_upload_unavailable", "Owned text upload is unavailable")
			return
		}
		fingerprint, snapshot, matched := NewUploadReader(s.db).BindingEvidenceForCreate(v3sourceregistry.ReadSpec{
			SourceType: "upload", URL: v.URL, EnvironmentID: env,
			ApplicationID: v.ApplicationID, TeamID: v.TeamID, SourceOwnerID: user.ID,
		})
		if !matched {
			httpapi.Error(w, http.StatusUnprocessableEntity, "source_upload_unavailable", "Upload binding is unavailable")
			return
		}
		snapshot, err = v3sourceregistry.WithUploadInitialVersion(snapshot, file.Version)
		if err != nil {
			mainCreateFailure(w, err)
			return
		}
		bindingFingerprint, bindingSnapshot = fingerprint, string(snapshot)
	}
	id := uuid.NewString()
	spacesValue := v.Spaces
	if spacesValue == nil {
		spacesValue = []string{}
	}
	spaces, _ := json.Marshal(spacesValue)
	_, err = tx.Exec(ctx, `INSERT INTO v3_knowledge_source_registrations
 (id,environment_id,application_id,team_id,name,source_type,source_url,secret_ref,
  spaces,schedule,chunk,inherit_acl,created_by,binding_fingerprint,binding_snapshot)
	VALUES($1,$2,$3,NULLIF($4,''),$5,$6,$7,$8,$9::jsonb,'manual',$10,true,$11,$12,$13::jsonb)`,
		id, env, v.ApplicationID, v.TeamID, v.Name, v.Type, v.URL, v.SecretRef, string(spaces), v.Chunk, user.ID,
		bindingFingerprint, bindingSnapshot)
	if err != nil {
		mainCreateFailure(w, err)
		return
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit
 (actor_id,application_id,action,target,outcome,changes)
 VALUES($1,$2,'v3.knowledge_source.registered',$3,'success',
 jsonb_build_object('environment_id',$4::text,'source_type',$5::text,'team_id',$6::text))`,
		user.ID, v.ApplicationID, id, env, v.Type, v.TeamID)
	if err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "audit_unavailable", "Source registration audit is unavailable")
		return
	}
	if err := currentMainCreateSession(ctx, tx, r); err != nil {
		mainCreateFailure(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		mainCreateFailure(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusCreated, map[string]any{
		"id": id, "registration_status": "unverified", "sync_started": false,
	})
}
