package v3events

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3canvas"
)

const maxIncidentCreateAlerts = 20

var incidentRequestKey = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{7,127}$`)

type incidentCreateRequest struct {
	Title      string   `json:"title"`
	Severity   string   `json:"severity"`
	Impact     *string  `json:"impact"`
	Env        string   `json:"env"`
	Services   []string `json:"services"`
	AlertIDs   []string `json:"alert_ids"`
	Commander  *string  `json:"commander"`
	WarRoom    bool     `json:"war_room"`
	Notify     bool     `json:"notify"`
	StatusPage bool     `json:"status_page"`
	OpenCanvas bool     `json:"open_canvas"`
}

type incidentCreateResult struct {
	ID              string `json:"id"`
	InvestigationID string `json:"investigation_id,omitempty"`
}

func selectedIncidentAlertIDs(ids []string) ([]string, bool) {
	if len(ids) == 0 || len(ids) > maxIncidentCreateAlerts {
		return nil, false
	}
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" || len(id) > 128 || strings.TrimSpace(id) != id || seen[id] {
			return nil, false
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Strings(out)
	return out, true
}

func incidentDraftIDs(r *http.Request) ([]string, bool) {
	values := r.URL.Query()["alerts"]
	if len(values) != 1 || values[0] == "" {
		return nil, false
	}
	return selectedIncidentAlertIDs(strings.Split(values[0], ","))
}

func validateIncidentCreate(in *incidentCreateRequest) ([]string, bool) {
	in.Title = strings.TrimSpace(in.Title)
	if n := utf8.RuneCountInString(in.Title); n < 1 || n > 300 || len(in.Title) > 1200 {
		return nil, false
	}
	if in.Severity != "P1" && in.Severity != "P2" && in.Severity != "P3" && in.Severity != "P4" ||
		in.Env == "" || in.Env == "all" || strings.TrimSpace(in.Env) != in.Env ||
		len(in.Services) != 1 || in.Services[0] == "" || strings.TrimSpace(in.Services[0]) != in.Services[0] {
		return nil, false
	}
	for _, field := range []*string{in.Impact, in.Commander} {
		if field != nil {
			*field = strings.TrimSpace(*field)
		}
	}
	if in.Impact != nil && *in.Impact == "" {
		in.Impact = nil
	}
	if in.Commander != nil && *in.Commander == "" {
		in.Commander = nil
	}
	if in.Impact != nil && (utf8.RuneCountInString(*in.Impact) > 2000 || len(*in.Impact) > 8000) ||
		in.Commander != nil && (utf8.RuneCountInString(*in.Commander) > 200 || len(*in.Commander) > 800) {
		return nil, false
	}
	return selectedIncidentAlertIDs(in.AlertIDs)
}

func incidentCreateHash(in incidentCreateRequest) string {
	data, _ := json.Marshal(in)
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func (s *Service) selectedIncidentAlertsTx(r *http.Request, tx pgx.Tx, ids []string, action string) ([]incidentRecord, error) {
	alerts := make([]incidentRecord, 0, len(ids))
	for _, id := range ids {
		v, err := scanIncident(tx.QueryRow(r.Context(), incidentSelect+` WHERE i.id=$1 AND NOT i.suspected FOR SHARE OF i`, id))
		if err != nil {
			return nil, err
		}
		if v.AlertStatus != "firing" && v.AlertStatus != "pending" || v.Status == "resolved" || v.SourceID == "ai-patrol" {
			return nil, store.ErrConflict
		}
		if _, valid := alertDetailSeverity(v.Severity); !valid || v.ID == "" || v.ApplicationID == "" || v.EnvironmentID == "" || v.EnvironmentKey == "" || strings.TrimSpace(v.Title) == "" {
			return nil, store.ErrInvalidReference
		}
		if err := s.authorizeAlertTx(r, tx, v, action); err != nil {
			return nil, err
		}
		if len(alerts) > 0 && (alerts[0].ApplicationID != v.ApplicationID || alerts[0].EnvironmentID != v.EnvironmentID || alerts[0].EnvironmentKey != v.EnvironmentKey) {
			return nil, store.ErrConflict
		}
		var linked bool
		if err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM v3_incident_case_alerts WHERE alert_id=$1)`, id).Scan(&linked); err != nil {
			return nil, err
		}
		if linked {
			return nil, store.ErrConflict
		}
		alerts = append(alerts, v)
	}
	return alerts, nil
}

func respondIncidentCreateError(w http.ResponseWriter, err error) {
	var pgErr *pgconn.PgError
	switch {
	case errors.Is(err, store.ErrSessionRevoked):
		httpapi.Error(w, 401, "unauthorized", "Session expired")
	case errors.Is(err, pgx.ErrNoRows), errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrForbidden):
		httpapi.Error(w, 404, "not_found", "Selected alert unavailable")
	case errors.Is(err, store.ErrConflict), errors.As(err, &pgErr) && pgErr.Code == "23505":
		httpapi.Error(w, 409, "incident_conflict", "Alert changed or is already linked to an incident")
	case errors.Is(err, store.ErrInvalidReference):
		httpapi.Error(w, 503, "incident_source_invalid", "Persisted alert data is inconsistent")
	default:
		httpapi.Error(w, 503, "events_unavailable", "Event storage unavailable")
	}
}

// IncidentDraft projects only selected, current, authorized persisted alerts.
// No model is called and missing impact, owner or collaborator facts stay null.
func (s *Service) IncidentDraft(w http.ResponseWriter, r *http.Request) {
	ids, valid := incidentDraftIDs(r)
	if !valid {
		httpapi.Error(w, 422, "invalid_alerts", "Select 1–20 distinct alert IDs")
		return
	}
	if s == nil || s.DB == nil {
		respondIncidentCreateError(w, errors.New("storage unavailable"))
		return
	}
	tx, err := s.DB.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		respondIncidentCreateError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	alerts, err := s.selectedIncidentAlertsTx(r, tx, ids, "view")
	if err != nil {
		respondIncidentCreateError(w, err)
		return
	}
	chosenSeverity := "P4"
	items := make([]map[string]any, 0, len(alerts))
	for _, v := range alerts {
		level, _ := alertDetailSeverity(v.Severity)
		if severityRank(level) < severityRank(chosenSeverity) {
			chosenSeverity = level
		}
		items = append(items, map[string]any{"id": v.ID, "severity": level, "text": v.Title, "at": v.StartsAt.UTC().Format(time.RFC3339Nano)})
	}
	if err := activeAlertReadSession(r.Context(), tx, r); err != nil {
		respondIncidentCreateError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		respondIncidentCreateError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, 200, map[string]any{
		"title": alerts[0].Title, "severity": chosenSeverity, "severity_hint": "Highest severity among selected persisted alerts",
		"env": alerts[0].EnvironmentKey, "services": []map[string]any{{"id": alerts[0].ApplicationID, "tier": nil}},
		"impact": nil, "alerts": items, "commander": nil, "participants": []string{}, "war_room": nil,
		"source": "persisted_alerts", "partial": true,
		"unavailable": []string{"impact", "service.tier", "commander", "participants", "war_room", "ai_analysis"},
	})
}

func (s *Service) incidentCreateRetryTx(r *http.Request, tx pgx.Tx, key, hash string) (incidentCreateResult, bool, error) {
	user := httpapi.CurrentUser(r)
	var result incidentCreateResult
	var storedHash, app, envID, envKey string
	err := tx.QueryRow(r.Context(), `SELECT id,request_hash,application_id,environment_id,environment_key,COALESCE(investigation_id,'')
		FROM v3_incident_cases WHERE actor_id=$1 AND request_key=$2 FOR SHARE`, user.ID, key).
		Scan(&result.ID, &storedHash, &app, &envID, &envKey, &result.InvestigationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return incidentCreateResult{}, false, nil
	}
	if err != nil {
		return incidentCreateResult{}, false, err
	}
	v := incidentRecord{ApplicationID: app, EnvironmentID: envID, EnvironmentKey: envKey}
	if err := s.authorizeAlertTx(r, tx, v, "change"); err != nil {
		return incidentCreateResult{}, false, err
	}
	if err := s.authorizeAlertTx(r, tx, v, "view"); err != nil {
		return incidentCreateResult{}, false, err
	}
	if storedHash != hash {
		return incidentCreateResult{}, false, store.ErrConflict
	}
	return result, true, nil
}

func (s *Service) authorizeCanvasCreateTx(r *http.Request, tx pgx.Tx, app, env, ownerTeam string) error {
	policy := &rbac.V3Policy{Store: &store.Store{DB: s.DB}}
	user := httpapi.CurrentUser(r)
	for _, action := range []string{"change", "view"} {
		err := policy.AuthorizeWithTx(r.Context(), tx, user, rbac.V3Decision{
			Module: "canvas", Action: action, EnvironmentID: env, ApplicationID: app,
		})
		if errors.Is(err, store.ErrForbidden) && ownerTeam != "" {
			err = policy.AuthorizeWithTx(r.Context(), tx, user, rbac.V3Decision{
				Module: "canvas", Action: action, EnvironmentID: env, TeamID: ownerTeam,
			})
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// CreateIncident confirms a selected alert set as one durable human incident.
// The case, links, optional blank investigation and audit commit together.
func (s *Service) CreateIncident(w http.ResponseWriter, r *http.Request) {
	var in incidentCreateRequest
	if !httpapi.Decode(w, r, &in) {
		return
	}
	ids, valid := validateIncidentCreate(&in)
	if !valid {
		httpapi.Error(w, 422, "invalid_incident", "Confirm a title, severity, one service and 1–20 distinct alerts")
		return
	}
	in.AlertIDs = ids
	key := r.Header.Get("Idempotency-Key")
	if !incidentRequestKey.MatchString(key) {
		httpapi.Error(w, 422, "idempotency_key_required", "A valid Idempotency-Key header is required")
		return
	}
	if s == nil || s.DB == nil {
		respondIncidentCreateError(w, errors.New("storage unavailable"))
		return
	}
	hash := incidentCreateHash(in)
	user := httpapi.CurrentUser(r)
	if user.ID == "" || user.Disabled {
		httpapi.Error(w, 401, "unauthorized", "Login required")
		return
	}
	tx, err := s.DB.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		respondIncidentCreateError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	if previous, found, err := s.incidentCreateRetryTx(r, tx, key, hash); err != nil {
		respondIncidentCreateError(w, err)
		return
	} else if found {
		if err := activeAlertReadSession(r.Context(), tx, r); err != nil {
			respondIncidentCreateError(w, err)
			return
		}
		if err := tx.Commit(r.Context()); err != nil {
			respondIncidentCreateError(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		httpapi.WriteJSON(w, 200, previous)
		return
	}
	alerts, err := s.selectedIncidentAlertsTx(r, tx, ids, "change")
	if err != nil {
		respondIncidentCreateError(w, err)
		return
	}
	for _, v := range alerts {
		if err := s.authorizeAlertTx(r, tx, v, "view"); err != nil {
			respondIncidentCreateError(w, err)
			return
		}
	}
	first := alerts[0]
	if first.EnvironmentKey != in.Env || first.ApplicationID != in.Services[0] {
		httpapi.Error(w, 409, "incident_scope_changed", "Selected alerts do not match the confirmed environment and service")
		return
	}
	if in.WarRoom || in.Notify || in.StatusPage {
		httpapi.Error(w, 503, "incident_external_action_unavailable", "Requested collaboration or publication action has no configured V3 executor")
		return
	}
	if in.OpenCanvas && in.Env != "prod" && in.Env != "staging" && in.Env != "test" {
		httpapi.Error(w, 422, "canvas_environment_unsupported", "Canvas supports prod, staging and test environments")
		return
	}
	if in.OpenCanvas && utf8.RuneCountInString(in.Title) > 200 {
		httpapi.Error(w, 422, "canvas_title_too_long", "A linked Canvas title may contain at most 200 characters")
		return
	}
	var ownerTeam string
	if err := tx.QueryRow(r.Context(), `SELECT COALESCE(owner_team_id,'') FROM applications WHERE id=$1 AND NOT archived FOR SHARE`, first.ApplicationID).Scan(&ownerTeam); err != nil {
		respondIncidentCreateError(w, err)
		return
	}
	if in.OpenCanvas {
		if err := s.authorizeCanvasCreateTx(r, tx, first.ApplicationID, first.EnvironmentKey, ownerTeam); err != nil {
			respondAlertAuthorityError(w, err)
			return
		}
	}
	result := incidentCreateResult{ID: "INC-" + store.ID()}
	var insertedID string
	err = tx.QueryRow(r.Context(), `INSERT INTO v3_incident_cases
		(id,application_id,environment_id,environment_key,title,severity,impact,commander_text,actor_id,request_key,request_hash,investigation_id)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,NULLIF($12,''))
		ON CONFLICT(actor_id,request_key) DO NOTHING RETURNING id`, result.ID, first.ApplicationID, first.EnvironmentID,
		first.EnvironmentKey, in.Title, in.Severity, in.Impact, in.Commander, user.ID, key, hash, "").Scan(&insertedID)
	if errors.Is(err, pgx.ErrNoRows) {
		previous, found, lookupErr := s.incidentCreateRetryTx(r, tx, key, hash)
		if lookupErr != nil || !found {
			respondIncidentCreateError(w, lookupErr)
			return
		}
		result = previous
		if err := activeAlertReadSession(r.Context(), tx, r); err != nil {
			respondIncidentCreateError(w, err)
			return
		}
		if err := tx.Commit(r.Context()); err != nil {
			respondIncidentCreateError(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		httpapi.WriteJSON(w, 200, result)
		return
	}
	if err != nil {
		respondIncidentCreateError(w, err)
		return
	}
	if in.OpenCanvas {
		result.InvestigationID, err = v3canvas.CreateBlankIncidentInvestigation(r.Context(), tx, user,
			first.EnvironmentKey, first.ApplicationID, first.EnvironmentID, in.Title)
		if err != nil {
			respondIncidentCreateError(w, err)
			return
		}
		if _, err := tx.Exec(r.Context(), `UPDATE v3_incident_cases SET investigation_id=$2 WHERE id=$1`, result.ID, result.InvestigationID); err != nil {
			respondIncidentCreateError(w, err)
			return
		}
	}
	for _, id := range ids {
		if _, err := tx.Exec(r.Context(), `INSERT INTO v3_incident_case_alerts(case_id,alert_id) VALUES($1,$2)`, result.ID, id); err != nil {
			respondIncidentCreateError(w, err)
			return
		}
	}
	if _, err := tx.Exec(r.Context(), `INSERT INTO management_audit(actor_id,actor_kind,application_id,action,target,outcome,changes)
		VALUES($1,'user',$2,'incident.create',$3,'succeeded',jsonb_build_object('environment_id',$4::text,'alert_ids',$5::text[],'investigation_id',NULLIF($6::text,'')))`,
		user.ID, first.ApplicationID, result.ID, first.EnvironmentKey, ids, result.InvestigationID); err != nil {
		respondIncidentCreateError(w, err)
		return
	}
	if err := activeAlertReadSession(r.Context(), tx, r); err != nil {
		respondIncidentCreateError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		respondIncidentCreateError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, 201, result)
}
