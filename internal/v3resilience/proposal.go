package v3resilience

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3proposals"
)

type proposalRequest struct {
	Kind   string `json:"kind"`
	Params struct {
		InvestigationID string          `json:"investigation_id"`
		Reason          string          `json:"reason"`
		Desired         json.RawMessage `json:"desired"`
	} `json:"params"`
}

func textValue(s string, max int) bool {
	return s != "" && len([]rune(s)) <= max && strings.TrimSpace(s) == s &&
		!strings.ContainsAny(s, "\x00\r\n")
}

func exactJSON(raw []byte, out any) bool {
	if len(raw) == 0 || len(raw) > 1800 {
		return false
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return false
	}
	// Reject duplicate keys before decoding to a typed request. Otherwise the
	// stored human diff could display two conflicting values for one operation.
	keys := json.NewDecoder(bytes.NewReader(raw))
	first, err := keys.Token()
	if err != nil || first != json.Delim('{') {
		return false
	}
	seen := map[string]bool{}
	for keys.More() {
		k, err := keys.Token()
		name, ok := k.(string)
		if err != nil || !ok || seen[name] {
			return false
		}
		seen[name] = true
		var value json.RawMessage
		if keys.Decode(&value) != nil {
			return false
		}
	}
	last, err := keys.Token()
	if err != nil || last != json.Delim('}') {
		return false
	}
	var trailing any
	if keys.Decode(&trailing) != io.EOF {
		return false
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		return false
	}
	var extra any
	return d.Decode(&extra) == io.EOF
}

// The request is a human-authored change suggestion. It carries no source
// state, connector binding or executable action. A future producer must build
// and verify an independent action snapshot before any executor can use it.
func proposedChange(req proposalRequest) (string, string, bool) {
	switch req.Kind {
	case "rollout.pause":
		var v struct {
			RolloutID string `json:"rollout_id"`
		}
		if !exactJSON(req.Params.Desired, &v) || !textValue(v.RolloutID, 128) {
			return "", "", false
		}
		return v.RolloutID, string(req.Params.Desired), true
	case "rollout.rollback":
		var v struct {
			RolloutID string `json:"rollout_id"`
			Revision  string `json:"revision"`
		}
		if !exactJSON(req.Params.Desired, &v) || !textValue(v.RolloutID, 128) || !textValue(v.Revision, 128) {
			return "", "", false
		}
		return v.RolloutID, string(req.Params.Desired), true
	case "breaker.threshold":
		var v struct {
			BreakerID        string   `json:"breaker_id"`
			ThresholdPercent *float64 `json:"threshold_percent"`
		}
		if !exactJSON(req.Params.Desired, &v) || !textValue(v.BreakerID, 128) || v.ThresholdPercent == nil ||
			*v.ThresholdPercent <= 0 || *v.ThresholdPercent > 100 {
			return "", "", false
		}
		return v.BreakerID, string(req.Params.Desired), true
	case "limit.threshold":
		var v struct {
			Route             string `json:"route"`
			RequestsPerSecond *int   `json:"requests_per_second"`
		}
		if !exactJSON(req.Params.Desired, &v) || !textValue(v.Route, 256) || v.RequestsPerSecond == nil ||
			*v.RequestsPerSecond <= 0 || *v.RequestsPerSecond > 1000000 {
			return "", "", false
		}
		return v.Route, string(req.Params.Desired), true
	case "flag.set":
		var v struct {
			FlagKey string `json:"flag_key"`
			Enabled *bool  `json:"enabled"`
		}
		if !exactJSON(req.Params.Desired, &v) || !textValue(v.FlagKey, 128) || v.Enabled == nil {
			return "", "", false
		}
		return v.FlagKey, string(req.Params.Desired), true
	case "hpa.bounds":
		var v struct {
			HPAID string `json:"hpa_id"`
			Min   *int   `json:"min"`
			Max   *int   `json:"max"`
		}
		if !exactJSON(req.Params.Desired, &v) || !textValue(v.HPAID, 128) || v.Min == nil || v.Max == nil ||
			*v.Min < 1 || *v.Max < *v.Min || *v.Max > 10000 {
			return "", "", false
		}
		return v.HPAID, string(req.Params.Desired), true
	default:
		return "", "", false
	}
}

func (s *Service) Propose(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.DB == nil || s.Access == nil {
		writeError(w, nil)
		return
	}
	if r.URL.RawQuery != "" {
		httpapi.Error(w, 422, "invalid_filter", "Proposal creation accepts no filters")
		return
	}
	t, ok := s.selected(r)
	if !ok {
		httpapi.Error(w, 404, "not_found", "Service not found in selected environment")
		return
	}
	var req proposalRequest
	if !httpapi.Decode(w, r, &req) {
		return
	}
	if !textValue(req.Params.InvestigationID, 128) || !textValue(req.Params.Reason, 500) {
		httpapi.Error(w, 422, "invalid_resilience_proposal", "Provide a scoped investigation and reason")
		return
	}
	targetName, desired, valid := proposedChange(req)
	if !valid {
		httpapi.Error(w, 422, "invalid_resilience_proposal", "Choose a supported kind with exact desired parameters")
		return
	}
	user := httpapi.CurrentUser(r)
	if user.ID == "" || user.Disabled {
		httpapi.Error(w, 403, "forbidden", "An active human account is required")
		return
	}
	cookie, err := r.Cookie("aiops_session")
	if err != nil {
		httpapi.Error(w, 401, "unauthorized", "Login required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		writeError(w, err)
		return
	}
	defer tx.Rollback(ctx)
	t, err = s.loadTarget(ctx, tx, t)
	if err == nil {
		err = store.LockActiveSession(ctx, tx, cookie.Value, user.ID)
	}
	if err == nil {
		err = lockGrantSources(ctx, tx, user.ID)
	}
	if err == nil {
		err = s.authorize(ctx, tx, user, t, "services", "view")
	}
	if err == nil {
		err = s.authorize(ctx, tx, user, t, "proposals", "change")
	}
	if err == nil {
		err = s.authorize(ctx, tx, user, t, "canvas", "view")
	}
	if err != nil {
		writeError(w, err)
		return
	}
	var investigationUpdatedAt time.Time
	err = tx.QueryRow(ctx, `SELECT i.updated_at FROM investigations i
 JOIN application_environments e ON e.id=i.environment_id AND e.application_id=i.application_id
 WHERE i.id=$1 AND i.application_id=$2 AND i.status='open'
 AND e.key=$3 AND NOT e.archived FOR SHARE OF i,e`,
		req.Params.InvestigationID, t.ApplicationID, t.EnvironmentID).Scan(&investigationUpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, store.ErrNotFound)
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	content := v3proposals.Content{
		Title:            "Review resilience request: " + req.Kind,
		Risk:             "high", // conservative review priority; no observed risk score
		Impact:           "Unknown until the target source and affected traffic are reviewed",
		Reversible:       false, // no executor or reverse operation has been verified
		ProposedBy:       user.Name,
		InvestigationID:  req.Params.InvestigationID,
		Target:           fmt.Sprintf("%s / %s / %s", t.Name, t.EnvironmentID, targetName),
		Diff:             v3proposals.Diff{Path: "resilience/" + req.Kind, Lines: []string{desired}},
		Method:           "Human-requested change for review only. This record has no action snapshot, approval, merge request or executor.",
		Evidence:         []v3proposals.Evidence{{N: 1, Icon: "user", Title: "Human request", Desc: req.Params.Reason}},
		Verification:     []string{"Verify the current exact target and environment in its authoritative source", "Confirm expected impact, rollback and a separate authorized execution path"},
		VerificationNote: "Operational state and execution binding are unverified; no change has been applied.",
		Checklist:        []string{"Review target ownership and current source revision", "Review impact and independent approval requirements"},
		Requirement:      fmt.Sprintf("Requested kind %s for %s; investigation last updated %s", req.Kind, targetName, investigationUpdatedAt.UTC().Format(time.RFC3339Nano)),
	}
	if !textValue(content.ProposedBy, 300) {
		content.ProposedBy = user.ID
	}
	if !textValue(content.Target, 1000) || !textValue(content.Requirement, 2000) ||
		!textValue(content.ProposedBy, 300) {
		httpapi.Error(w, 422, "invalid_resilience_proposal", "Requested target exceeds the review record limit")
		return
	}
	raw, err := json.Marshal(content)
	if err != nil {
		writeError(w, err)
		return
	}
	id := store.ID()
	_, err = tx.Exec(ctx, `INSERT INTO v3_proposals
 (id,investigation_id,application_id,environment_id,content,created_by,creator_kind)
 VALUES($1,$2,$3,$4,$5::jsonb,$6,'user')`,
		id, req.Params.InvestigationID, t.ApplicationID, t.EnvironmentID, raw, user.ID)
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,actor_kind,application_id,action,target,outcome,changes)
 VALUES($1,'user',$2,'v3.resilience.proposal_requested',$3,'success',
 jsonb_build_object('environment_id',$4::text,'investigation_id',$5::text,'kind',$6::text,'executable',false))`,
			user.ID, t.ApplicationID, id, t.EnvironmentID, req.Params.InvestigationID, req.Kind)
	}
	if err == nil {
		err = sessionCurrent(ctx, tx, cookie.Value, user.ID)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, 201, map[string]string{"proposal_id": id})
}

func lockGrantSources(ctx context.Context, tx pgx.Tx, actor string) error {
	var human bool
	err := tx.QueryRow(ctx, `SELECT v3_identity_kind='human' AND source<>'settings_demo' FROM users
 WHERE id=$1 AND NOT disabled AND deleted_at IS NULL FOR SHARE`, actor).Scan(&human)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !human) {
		return store.ErrForbidden
	}
	if err != nil {
		return err
	}
	return lockAuthorityTables(ctx, tx)
}
