// Package v3webhook receives Alertmanager events without a browser session.
// A source secret and a fixed, current application/environment mapping are
// required before an alert can enter the existing operations incident tables.
package v3webhook

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/operations"
	"github.com/wans/aiops-canvas/internal/store"
)

const Pattern = "POST /api/v1/webhooks/alertmanager/{source}"

var ErrInvalidConfiguration = errors.New("invalid V3 Alertmanager source configuration")

type Service struct {
	db                 *pgxpool.Pool
	sources            map[string]operations.Source
	outboundConfigured bool
	envs               map[string]bool
	// persist stores alerts and returns how many it ignored for an
	// environment label that differs from their mapping (D32).
	persist func(context.Context, operations.Source, []mappedAlert) (int, error)
}

type mappedAlert struct {
	alert   operations.Alert
	mapping operations.Mapping
}

// New reuses the existing source token files and label mappings. Mapping
// EnvironmentID is the application_environments primary key; its key must be
// one of this instance's V3 environment IDs at ingest time.
func New(db *pgxpool.Pool, instance httpapi.V3InstanceConfig, config operations.Config) (*Service, error) {
	if db == nil || (instance.Tier != "production" && instance.Tier != "nonprod") || len(instance.EnvironmentIDs) == 0 {
		return nil, ErrInvalidConfiguration
	}
	s := &Service{db: db, sources: make(map[string]operations.Source), outboundConfigured: len(config.Channels) > 0, envs: make(map[string]bool)}
	for _, id := range instance.EnvironmentIDs {
		if id == "" || id == "all" || s.envs[id] || (instance.Tier == "production" && id != "prod") || (instance.Tier == "nonprod" && id == "prod") {
			return nil, ErrInvalidConfiguration
		}
		s.envs[id] = true
	}
	for _, source := range config.Sources {
		if source.ID == "" || strings.TrimSpace(source.ID) != source.ID || source.TokenFile == "" || len(source.Mappings) == 0 {
			return nil, ErrInvalidConfiguration
		}
		if _, exists := s.sources[source.ID]; exists {
			return nil, ErrInvalidConfiguration
		}
		for _, mapping := range source.Mappings {
			if len(mapping.Labels) == 0 || mapping.ApplicationID == "" || mapping.EnvironmentID == "" {
				return nil, ErrInvalidConfiguration
			}
		}
		s.sources[source.ID] = source
	}
	s.persist = s.persistAlerts
	return s, nil
}

func selectMapping(source operations.Source, labels map[string]string) (operations.Mapping, bool) {
	var selected operations.Mapping
	matches := 0
	for _, mapping := range source.Mappings {
		match := true
		for name, expected := range mapping.Labels {
			actual, present := labels[name]
			if !present || actual != expected {
				match = false
				break
			}
		}
		if match {
			selected = mapping
			matches++
		}
	}
	return selected, matches == 1
}

func (s *Service) Handler(w http.ResponseWriter, r *http.Request) {
	source, ok := s.sources[r.PathValue("source")]
	if !ok {
		httpapi.Error(w, http.StatusNotFound, "not_found", "Unknown alert source")
		return
	}
	file, err := os.Open(source.TokenFile)
	if err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "source_unavailable", "Alert source authentication unavailable")
		return
	}
	secret, err := io.ReadAll(io.LimitReader(file, 4097))
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "source_unavailable", "Alert source authentication unavailable")
		return
	}
	expected := strings.TrimSpace(string(secret))
	values := r.Header.Values("Authorization")
	got := ""
	if len(values) == 1 && strings.HasPrefix(values[0], "Bearer ") {
		got = strings.TrimPrefix(values[0], "Bearer ")
	}
	if len(expected) < 24 || len(expected) > 4096 || subtle.ConstantTimeCompare([]byte(expected), []byte(got)) != 1 {
		httpapi.Error(w, http.StatusUnauthorized, "unauthorized", "Invalid alert source token")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	defer r.Body.Close()
	var envelope operations.AlertEnvelope
	decoder := json.NewDecoder(r.Body)
	if err = decoder.Decode(&envelope); err != nil || len(envelope.Alerts) == 0 || len(envelope.Alerts) > 200 || decoder.Decode(new(any)) != io.EOF {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_alerts", "Expected 1 to 200 alerts in one JSON object")
		return
	}
	alerts := make([]mappedAlert, 0, len(envelope.Alerts))
	for _, alert := range envelope.Alerts {
		if alert.Fingerprint == "" || len(alert.Fingerprint) > 256 || alert.StartsAt.IsZero() || (alert.Status != "firing" && alert.Status != "resolved") || (alert.Status == "resolved" && (alert.EndsAt.IsZero() || alert.EndsAt.Before(alert.StartsAt))) {
			httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_alert", "Fingerprint, start time and valid status are required")
			return
		}
		if _, err = operations.MarshalAlertLabels(alert.Labels); err != nil {
			httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_alert_labels", "Alert labels exceed limits or contain invalid text")
			return
		}
		mapping, unique := selectMapping(source, alert.Labels)
		if !unique {
			httpapi.Error(w, http.StatusUnprocessableEntity, "unmapped_alert", "Alert must match exactly one configured application and environment")
			return
		}
		alerts = append(alerts, mappedAlert{alert: alert, mapping: mapping})
	}
	ignored, err := s.persist(r.Context(), source, alerts)
	if err != nil {
		if errors.Is(err, errStaleMapping) {
			httpapi.Error(w, http.StatusServiceUnavailable, "mapping_unavailable", "Alert source mapping is not active in this instance")
		} else if errors.Is(err, operations.ErrAlertLabelsConflict) {
			httpapi.Error(w, http.StatusConflict, "alert_identity_conflict", "Alert occurrence identity conflicts with stored labels or scope")
		} else {
			httpapi.Error(w, http.StatusServiceUnavailable, "unavailable", "Alert persistence failed; retry the envelope")
		}
		return
	}
	delivery := "not_configured"
	if s.outboundConfigured {
		// Legacy ops.Run is disabled under V3 authorization. Do not insert a
		// pending notification into a queue with no delivery worker.
		delivery = "not_connected"
	}
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"accepted": len(alerts) - ignored, "ignored": ignored, "notification_delivery": delivery})
}

var errStaleMapping = errors.New("inactive or cross-instance alert mapping")

// persistAlerts keeps environments apart (D32): an alert whose own
// "environment" label names another environment than its mapping is not
// stored, so a test rule's alert routed here cannot become a prod incident.
// It is logged and counted; the rest of the envelope is stored, so
// Alertmanager does not resend it.
func (s *Service) persistAlerts(ctx context.Context, source operations.Source, alerts []mappedAlert) (int, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	ignored := 0
	for _, item := range alerts {
		var environmentKey string
		// Lock all mapping rows until commit so an archive/reassignment cannot
		// race between validation and incident persistence.
		err = tx.QueryRow(ctx, `SELECT e.key FROM application_environments e
		 JOIN applications a ON a.id=e.application_id AND NOT a.archived
		 JOIN v3_application_environments ve ON ve.application_id=e.application_id AND ve.environment_id=e.key
		 WHERE e.id=$1 AND e.application_id=$2 AND NOT e.archived
		 FOR SHARE OF e,a,ve`, item.mapping.EnvironmentID, item.mapping.ApplicationID).Scan(&environmentKey)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && !s.envs[environmentKey]) {
			return 0, errStaleMapping
		}
		if err != nil {
			return 0, err
		}
		if label, ok := item.alert.Labels["environment"]; ok && label != environmentKey {
			ignored++
			slog.Warn("V3 alert ignored: its environment label differs from its mapping", "source", source.ID,
				"alertname", short(item.alert.Labels["alertname"], 128), "environment_label", short(label, 128), "mapped_environment", environmentKey)
			continue
		}
		if err = s.ingestOne(ctx, tx, source.ID, item); err != nil {
			return 0, err
		}
	}
	return ignored, tx.Commit(ctx)
}

func short(value string, max int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) > max {
		runes = runes[:max]
	}
	return string(runes)
}

// storedSeverity is the severity label as sent, cut to the column width. An
// alert without one is stored without one (""), so the views can say the
// severity is unknown instead of showing a default (PA-240).
func storedSeverity(labels map[string]string) string { return short(labels["severity"], 32) }

func (s *Service) ingestOne(ctx context.Context, tx pgx.Tx, sourceID string, item mappedAlert) error {
	a := item.alert
	m := item.mapping
	labels, err := operations.MarshalAlertLabels(a.Labels)
	if err != nil {
		return err
	}
	title := short(a.Labels["alertname"], 300)
	if title == "" {
		title = "Alert"
	}
	severity := storedSeverity(a.Labels)
	summary := short(a.Annotations["summary"], 1000)
	var ends *time.Time
	if a.Status == "resolved" {
		ends = &a.EndsAt
	}
	_, err = tx.Exec(ctx, `INSERT INTO ops_incidents(id,source_id,application_id,environment_id,fingerprint,title,severity,alert_status,summary,labels,starts_at,ends_at)
	 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb,$11,$12)
	 ON CONFLICT(source_id,fingerprint,starts_at) DO NOTHING`, store.ID(), sourceID, m.ApplicationID, m.EnvironmentID, a.Fingerprint, title, severity, a.Status, summary, string(labels), a.StartsAt, ends)
	if err != nil {
		return err
	}
	var incidentID, existingApp, existingEnv, state string
	var labelsKnown, labelsMatch bool
	err = tx.QueryRow(ctx, `SELECT id,application_id,environment_id,alert_status,labels IS NOT NULL,COALESCE(labels=$4::jsonb,false) FROM ops_incidents
	 WHERE source_id=$1 AND fingerprint=$2 AND starts_at=$3 FOR UPDATE`, sourceID, a.Fingerprint, a.StartsAt, string(labels)).Scan(&incidentID, &existingApp, &existingEnv, &state, &labelsKnown, &labelsMatch)
	if err != nil {
		return err
	}
	// Source mapping changes cannot silently move an existing occurrence into
	// a different application or environment.
	if existingApp != m.ApplicationID || existingEnv != m.EnvironmentID || (labelsKnown && !labelsMatch) {
		return operations.ErrAlertLabelsConflict
	}
	payload, err := json.Marshal(a)
	if err != nil {
		return err
	}
	// Historical incidents retain NULL labels. This occurrence can still
	// advance their alert state, without claiming to know the original labels.
	tag, err := tx.Exec(ctx, `INSERT INTO ops_alert_occurrences(incident_id,status,payload_hash)
	 VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, incidentID, a.Status, store.Hash(string(payload)))
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	if state != "resolved" {
		state = a.Status
	}
	_, err = tx.Exec(ctx, `UPDATE ops_incidents
	 SET alert_status=$2,ends_at=CASE WHEN $2='resolved' THEN COALESCE(ends_at,$3) ELSE NULL END,updated_at=now()
	 WHERE id=$1`, incidentID, state, ends)
	if err != nil {
		return err
	}
	return nil
}
