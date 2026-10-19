package operations

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
)

func (s *Service) enqueueNotification(ctx context.Context, tx pgx.Tx, v Incident, key, body string) error {
	for _, ch := range s.Config.Channels {
		if ch.ApplicationID != v.ApplicationID || ch.EnvironmentID != v.EnvironmentID {
			continue
		}
		// Duplicate state notifications share an event key for the occurrence. This is
		// stronger than the default cooldown, while changed states receive new keys.
		deep := strings.TrimRight(s.Config.PublicURL, "/") + "/?incident=" + url.QueryEscape(v.ID)
		if v.InvestigationID != "" {
			deep = strings.TrimRight(s.Config.PublicURL, "/") + "/?investigation=" + url.QueryEscape(v.InvestigationID)
		}
		_, e := tx.Exec(ctx, "INSERT INTO ops_notifications(id,incident_id,application_id,environment_id,channel,event_key,body) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(channel,event_key) DO NOTHING", store.ID(), v.ID, v.ApplicationID, v.EnvironmentID, ch.ID, key, short(body, 1500)+"\n"+deep)
		if e != nil {
			return e
		}
	}
	return nil
}
func (s *Service) Notifications(ctx context.Context, u core.User) ([]Notification, error) {
	ids, e := s.API.VisibleApplicationIDs(ctx, u)
	if e != nil {
		return nil, e
	}
	rows, e := s.DB.Query(ctx, "SELECT id,incident_id,application_id,environment_id,channel,status,attempts,next_attempt_at,last_error,created_at FROM ops_notifications WHERE application_id=ANY($1::text[]) AND (cardinality($2::text[])=0 OR application_id=ANY($2::text[])) AND (cardinality($3::text[])=0 OR environment_id=ANY($3::text[])) ORDER BY created_at DESC LIMIT 200", ids, readScopeArrays(ctx).ApplicationIDs, readScopeArrays(ctx).EnvironmentIDs)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Notification{}
	for rows.Next() {
		var v Notification
		if e = rows.Scan(&v.ID, &v.IncidentID, &v.ApplicationID, &v.EnvironmentID, &v.Channel, &v.Status, &v.Attempts, &v.NextAttemptAt, &v.LastError, &v.CreatedAt); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Service) deliverOne(ctx context.Context) error {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var id, channel, body, app, env string
	var attempt int
	e = tx.QueryRow(ctx, "SELECT id,channel,body,application_id,environment_id,attempts FROM ops_notifications WHERE (status IN('pending','retrying') AND next_attempt_at<=now()) OR (status='sending' AND lease_until<now()) ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1").Scan(&id, &channel, &body, &app, &env, &attempt)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	token := store.ID()
	attempt++
	if _, e = tx.Exec(ctx, "UPDATE ops_notifications SET status='sending',attempts=$2,lease_token=$3,lease_until=now()+interval '30 seconds' WHERE id=$1", id, attempt, token); e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	var ch *Channel
	for i := range s.Config.Channels {
		if s.Config.Channels[i].ID == channel && s.Config.Channels[i].ApplicationID == app && s.Config.Channels[i].EnvironmentID == env {
			ch = &s.Config.Channels[i]
		}
	}
	status, msg := "sent", ""
	if ch == nil {
		e = errors.New("notification channel mapping removed")
	} else {
		e = s.sendFeishu(ctx, *ch, body)
	}
	next := time.Now().Add(time.Duration(1<<min(attempt, 8)) * time.Minute)
	if e != nil {
		status = "retrying"
		msg = "Delivery failed; check the server-managed channel credentials and reachability"
		if attempt >= 5 {
			status = "failed"
		}
	}
	// A timeout may mean the receiver already accepted the message. The outbox is
	// at-least-once; an external webhook without idempotency cannot promise more.
	_, e = s.DB.Exec(ctx, "UPDATE ops_notifications SET status=$3,last_error=$4,next_attempt_at=$5,lease_token=NULL,lease_until=NULL WHERE id=$1 AND lease_token=$2", id, token, status, msg, next)
	return e
}
func (s *Service) sendFeishu(ctx context.Context, ch Channel, body string) error {
	b, e := os.ReadFile(ch.WebhookFile)
	if e != nil {
		return errors.New("channel unavailable")
	}
	endpoint := strings.TrimSpace(string(b))
	u, e := url.Parse(endpoint)
	if e != nil || u.Scheme != "https" || u.User != nil || (u.Host != "open.feishu.cn" && u.Host != "open.larksuite.com") || !strings.HasPrefix(u.Path, "/open-apis/bot/v2/hook/") {
		return errors.New("invalid Feishu webhook endpoint")
	}
	payload := map[string]any{"msg_type": "text", "content": map[string]string{"text": body}}
	if ch.SecretFile != "" {
		secret, e := os.ReadFile(ch.SecretFile)
		if e != nil {
			return errors.New("channel signature unavailable")
		}
		timestamp := strconv.FormatInt(time.Now().Unix(), 10)
		mac := hmac.New(sha256.New, []byte(timestamp+"\n"+strings.TrimSpace(string(secret))))
		payload["timestamp"] = timestamp
		payload["sign"] = base64.StdEncoding.EncodeToString(mac.Sum(nil))
	}
	raw, e := json.Marshal(payload)
	if e != nil {
		return e
	}
	req, e := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(raw))
	if e != nil {
		return e
	}
	req.Header.Set("Content-Type", "application/json")
	resp, e := s.Client.Do(req)
	if e != nil {
		return errors.New("notification transport failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("notification HTTP %d", resp.StatusCode)
	}
	var result struct {
		Code       *int `json:"code"`
		StatusCode *int `json:"StatusCode"`
	}
	if e = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&result); e != nil {
		return errors.New("invalid notification response")
	}
	if result.Code != nil && *result.Code == 0 {
		return nil
	}
	if result.StatusCode != nil && *result.StatusCode == 0 {
		return nil
	}
	return errors.New("notification rejected")
}

// Domain events may commit out of global sequence order. A per-event receipt,
// rather than a high-watermark cursor, prevents late commits from being missed.
func (s *Service) relayEvents(ctx context.Context) error {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	rows, e := tx.Query(ctx, `SELECT e.seq,e.type,e.investigation_id,i.application_id,COALESCE(i.environment_id,''),i.title,COALESCE(e.payload->>'run_id','')
 FROM events e JOIN investigations i ON i.id=e.investigation_id
 WHERE e.type IN('action.proposed','execution.succeeded','execution.failed','execution.needs_attention','run.completed','investigation.closed')
 AND NOT EXISTS(SELECT 1 FROM ops_event_receipts r WHERE r.seq=e.seq) ORDER BY e.seq LIMIT 100`)
	if e != nil {
		return e
	}
	type event struct {
		seq                             int64
		kind, inv, app, env, title, run string
	}
	events := []event{}
	for rows.Next() {
		var v event
		if e = rows.Scan(&v.seq, &v.kind, &v.inv, &v.app, &v.env, &v.title, &v.run); e != nil {
			rows.Close()
			return e
		}
		events = append(events, v)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return e
	}
	for _, v := range events {
		tag, e := tx.Exec(ctx, "INSERT INTO ops_event_receipts(seq) VALUES($1) ON CONFLICT DO NOTHING", v.seq)
		if e != nil {
			return e
		}
		if tag.RowsAffected() == 0 {
			continue
		}
		if v.kind == "investigation.closed" {
			if _, e = tx.Exec(ctx, "UPDATE ops_incidents SET status='resolved',updated_at=now() WHERE investigation_id=$1", v.inv); e != nil {
				return e
			}
		}
		var incidentID string
		_ = tx.QueryRow(ctx, "SELECT id FROM ops_incidents WHERE investigation_id=$1 LIMIT 1", v.inv).Scan(&incidentID)
		// Only automatic initial runs generate an analysis notification, not every chat round.
		if v.kind == "run.completed" {
			var automatic bool
			if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM ops_automation_runs a JOIN runs r ON r.id=$2 WHERE a.investigation_id=$1 AND a.kind='initial' AND (a.run_id=r.id OR r.request_id='automation:'||a.id))", v.inv, v.run).Scan(&automatic); e != nil {
				return e
			}
			if !automatic {
				continue
			}
		}
		item := Incident{ID: incidentID, ApplicationID: v.app, EnvironmentID: v.env, InvestigationID: v.inv}
		if e = s.enqueueNotification(ctx, tx, item, "event:"+strconv.FormatInt(v.seq, 10), "AIOps Canvas · "+short(v.title, 200)+"\n"+v.kind); e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}
