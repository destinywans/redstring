package v3integrations

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
)

type settingsRequest struct {
	ExpectedRevision int64  `json:"expected_revision"`
	Address          string `json:"address"`
	Scope            string `json:"scope"`
}

func validInteractionReason(reason string) bool {
	return utf8.ValidString(reason) && len(reason) <= 1000 && !strings.ContainsAny(reason, "\x00\r\n")
}

func (s *Service) settings(w http.ResponseWriter, r *http.Request) {
	var in settingsRequest
	if !httpapi.Decode(w, r, &in) {
		return
	}
	if in.ExpectedRevision < 1 {
		httpapi.Error(w, 422, "invalid_revision", "A positive expected revision is required")
		return
	}
	if _, err := validEndpoint(in.Address); err != nil {
		httpapi.Error(w, 422, "invalid_endpoint", "Use an HTTPS or loopback HTTP endpoint")
		return
	}
	s.changeRegistration(w, r, &in, "")
}
func (s *Service) disable(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Reason string `json:"reason"`
	}
	if r.Body != nil && r.ContentLength != 0 && !httpapi.Decode(w, r, &in) {
		return
	}
	if !validInteractionReason(in.Reason) {
		httpapi.Error(w, 422, "invalid_reason", "Reason exceeds its bounds")
		return
	}
	s.changeRegistration(w, r, nil, in.Reason)
}
func (s *Service) changeRegistration(w http.ResponseWriter, r *http.Request, in *settingsRequest, reason string) {
	env, ok := s.scope(w, r, true)
	if !ok {
		return
	}
	if s.DB == nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return
	}
	defer rollbackConnectorWrite(tx)
	c, err := scanConnector(tx.QueryRow(ctx, "SELECT "+columns+" FROM v3_connectors WHERE id=$1 AND environment_id=$2 FOR UPDATE", r.PathValue("id"), env[0]))
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, 404, "not_found", "Connector not found")
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector storage is unavailable")
		return
	}
	if !s.authorizeLockedWriteTx(w, r, tx, c.EnvironmentID) {
		return
	}
	if !requireInteractionHuman(w, r, tx) {
		return
	}
	if !validConnectorProjection(c) {
		httpapi.Error(w, 503, "connector_state_unavailable", "Connector configuration is inconsistent")
		return
	}
	action := "v3.connector.disabled"
	changed := c.Enabled
	if in != nil {
		if c.Revision != in.ExpectedRevision {
			httpapi.Error(w, 409, "connector_revision_changed", "Reload connector settings")
			return
		}
		// scope is the persisted environment identity, not a user-controlled ACL.
		if in.Scope != c.EnvironmentID {
			httpapi.Error(w, 422, "invalid_scope", "Connector scope must remain its registered environment")
			return
		}
		address := strings.TrimRight(strings.TrimSpace(in.Address), "/")
		changed = address != c.Endpoint
		c.Endpoint = address
		action = "v3.connector.settings_updated"
	}
	if in == nil || changed {
		c.Enabled = false
		c.Status = "unverified"
		c.Checked = nil
		c.LatencyMS = nil
		c.ErrorCode = ""
		for i := range c.Tools {
			c.Tools[i].Enabled = false
		}
		raw, _ := json.Marshal(c.Tools)
		err = tx.QueryRow(ctx, `UPDATE v3_connectors SET endpoint=$2,enabled=false,tools=$3,status='unverified',last_checked_at=NULL,last_latency_ms=NULL,last_error_code='',updated_at=now() WHERE id=$1 RETURNING revision`, c.ID, c.Endpoint, raw).Scan(&c.Revision)
		if err != nil {
			httpapi.Error(w, 503, "storage_unavailable", "Connector settings could not be saved")
			return
		}
	}
	services, err := s.connectorServicesTx(ctx, tx, c, httpapi.CurrentUser(r))
	if err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector bindings are unavailable")
		return
	}
	if _, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes) VALUES($1,$2,$3,'success',jsonb_build_object('environment_id',$4::text,'revision',$5::bigint,'changed',$6::boolean,'reason_supplied',$7::boolean))`, httpapi.CurrentUser(r).ID, action, c.ID, c.EnvironmentID, c.Revision, changed, reason != ""); err != nil {
		httpapi.Error(w, 503, "audit_unavailable", "Connector audit is unavailable")
		return
	}
	cookie, _ := r.Cookie("aiops_session")
	var active bool
	err = tx.QueryRow(ctx, `SELECT expires_at>clock_timestamp() FROM sessions WHERE token_hash=$1`, store.Hash(cookie.Value)).Scan(&active)
	if err != nil || !active {
		httpapi.Error(w, 403, "forbidden", "Human session expired")
		return
	}
	if err = tx.Commit(ctx); err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Connector settings could not be committed")
		return
	}
	httpapi.WriteJSON(w, 200, s.detailProjection(c, services, r.Header.Get("Accept-Language")))
}

// Static deployment sources have no safe dynamic registration mapping. Refuse
// before minting a secret or URL until exact application/source admission exists.
func (s *Service) createWebhook(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	env, ok := s.scope(w, r, true)
	if !ok {
		return
	}
	var in struct {
		Name   string `json:"name"`
		Type   string `json:"type"`
		Verify string `json:"verify"`
	}
	if !httpapi.Decode(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Name) == "" || !validInteractionReason(in.Name) || (in.Type != "change" && in.Type != "alert" && in.Type != "incident") || (in.Verify != "hmac" && in.Verify != "token") {
		httpapi.Error(w, 422, "invalid_webhook", "Choose a name, event type and verification method")
		return
	}
	if s.DB == nil {
		httpapi.Error(w, 503, "webhook_registration_unavailable", "Webhook registration is not configured")
		return
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		httpapi.Error(w, 503, "storage_unavailable", "Webhook storage is unavailable")
		return
	}
	defer rollbackConnectorWrite(tx)
	if !s.authorizeLockedWriteTx(w, r, tx, env[0]) {
		return
	}
	if !requireInteractionHuman(w, r, tx) {
		return
	}
	httpapi.Error(w, 503, "webhook_registration_unavailable", "Deployment-owned exact application and source binding is required before a consumable Webhook can be registered")
}

func requireInteractionHuman(w http.ResponseWriter, r *http.Request, tx pgx.Tx) bool {
	var human bool
	err := tx.QueryRow(r.Context(), `SELECT v3_identity_kind='human' FROM users WHERE id=$1`, httpapi.CurrentUser(r).ID).Scan(&human)
	if err != nil {
		httpapi.Error(w, 503, "authorization_unavailable", "Human identity is unavailable")
		return false
	}
	if !human {
		httpapi.Error(w, 403, "human_session_required", "A human session is required")
		return false
	}
	return true
}
