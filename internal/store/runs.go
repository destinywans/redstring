package store

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"time"
)

func runRow(row pgx.Row) (core.Run, error) {
	var r core.Run
	e := row.Scan(&r.ID, &r.BranchID, &r.Status, &r.ModelID, &r.Attempt, &r.Error, &r.CreatedAt)
	if errors.Is(e, pgx.ErrNoRows) {
		e = ErrNotFound
	}
	return r, e
}

const runColumns = "id,branch_id,status,model_id,attempt,error,created_at"

func (s *Store) Run(ctx context.Context, id string) (core.Run, error) {
	return runRow(s.DB.QueryRow(ctx, "SELECT "+runColumns+" FROM runs WHERE id=$1", id))
}
func (s *Store) Enqueue(ctx context.Context, bid, content, model, request string) (core.Run, error) {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return core.Run{}, e
	}
	defer tx.Rollback(ctx)
	b, e := branch(ctx, tx, bid, true)
	if e != nil {
		return core.Run{}, e
	}
	hash := Hash(content + "\x00" + model)
	var existingID, existingHash string
	e = tx.QueryRow(ctx, "SELECT id,request_hash FROM runs WHERE branch_id=$1 AND request_id=$2", bid, request).Scan(&existingID, &existingHash)
	if e == nil {
		if hash != existingHash {
			return core.Run{}, ErrConflict
		}
		return runRow(tx.QueryRow(ctx, "SELECT "+runColumns+" FROM runs WHERE id=$1", existingID))
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return core.Run{}, e
	}
	if e = requireOpen(ctx, tx, b.InvestigationID); e != nil {
		return core.Run{}, e
	}
	if b.Status == "running" {
		return core.Run{}, ErrConflict
	}
	b.ModelID = model
	b.Messages = append(b.Messages, core.Message{ID: ID(), Role: "user", Content: content, CreatedAt: time.Now().UTC()})
	if e = revision(ctx, tx, &b, core.RevisionProvenance{Kind: "message"}); e != nil {
		return core.Run{}, e
	}
	frozen, e := referencedEvidence(ctx, tx, b.InvestigationID, b.Messages)
	if e != nil {
		return core.Run{}, e
	}
	frozenJSON, e := json.Marshal(frozen)
	if e != nil {
		return core.Run{}, e
	}
	input, e := json.Marshal(b.Messages)
	if e != nil {
		return core.Run{}, e
	}
	r, e := runRow(tx.QueryRow(ctx, "INSERT INTO runs(id,branch_id,request_id,request_hash,status,model_id,input,context_revision,input_evidence) VALUES($1,$2,$3,$4,'queued',$5,$6,$7,$8) RETURNING "+runColumns, ID(), bid, request, hash, model, input, b.Revision, frozenJSON))
	if e != nil {
		return r, e
	}
	if e = event(ctx, tx, b.InvestigationID, bid, "run.queued", r); e != nil {
		return r, e
	}
	return r, tx.Commit(ctx)
}

type Claim struct {
	Run             core.Run
	Token           string
	Messages        []core.Message
	InvestigationID string
	ApplicationID   string
	ContextRevision int64
	FrozenEvidence  []core.Evidence
}

func (s *Store) Claim(ctx context.Context) (Claim, error) {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return Claim{}, e
	}
	defer tx.Rollback(ctx)
	var c Claim
	var raw, frozenRaw []byte
	e = tx.QueryRow(ctx, "SELECT id,branch_id,status,model_id,attempt,error,created_at,input,context_revision,input_evidence FROM runs WHERE (status='queued' OR (status='running' AND lease_until<now())) ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1").Scan(&c.Run.ID, &c.Run.BranchID, &c.Run.Status, &c.Run.ModelID, &c.Run.Attempt, &c.Run.Error, &c.Run.CreatedAt, &raw, &c.ContextRevision, &frozenRaw)
	if errors.Is(e, pgx.ErrNoRows) {
		return c, ErrNoWork
	}
	if e != nil {
		return c, e
	}
	if c.Run.Attempt >= 3 {
		_, e = tx.Exec(ctx, "UPDATE runs SET status='failed',error='retry limit after interrupted attempts',lease_token=NULL WHERE id=$1", c.Run.ID)
		if e != nil {
			return c, e
		}
		_, e = tx.Exec(ctx, "UPDATE attempts SET status='interrupted',finished_at=now() WHERE run_id=$1 AND status='running'", c.Run.ID)
		if e != nil {
			return c, e
		}
		if e = tx.QueryRow(ctx, "SELECT investigation_id FROM branches WHERE id=$1", c.Run.BranchID).Scan(&c.InvestigationID); e != nil {
			return c, e
		}
		if e = event(ctx, tx, c.InvestigationID, c.Run.BranchID, "run.failed", map[string]string{"run_id": c.Run.ID}); e != nil {
			return c, e
		}
		if e = tx.Commit(ctx); e != nil {
			return c, e
		}
		return c, ErrNoWork
	}
	if e = json.Unmarshal(frozenRaw, &c.FrozenEvidence); e != nil {
		return c, e
	}
	c.Token = ID()
	c.Run.Attempt++
	c.Run.Status = "running"
	if e = json.Unmarshal(raw, &c.Messages); e != nil {
		return c, e
	}
	if _, e = tx.Exec(ctx, "UPDATE attempts SET status='interrupted',finished_at=now() WHERE run_id=$1 AND status='running'", c.Run.ID); e != nil {
		return c, e
	}
	_, e = tx.Exec(ctx, "UPDATE runs SET status='running',attempt=$2,lease_token=$3,lease_until=now()+interval '30 seconds' WHERE id=$1", c.Run.ID, c.Run.Attempt, c.Token)
	if e != nil {
		return c, e
	}
	_, e = tx.Exec(ctx, "INSERT INTO attempts(id,run_id,number,status) VALUES($1,$2,$3,'running')", c.Token, c.Run.ID, c.Run.Attempt)
	if e != nil {
		return c, e
	}
	e = tx.QueryRow(ctx, "SELECT b.investigation_id,i.application_id FROM branches b JOIN investigations i ON i.id=b.investigation_id WHERE b.id=$1", c.Run.BranchID).Scan(&c.InvestigationID, &c.ApplicationID)
	if e != nil {
		return c, e
	}
	if e = event(ctx, tx, c.InvestigationID, c.Run.BranchID, "run.started", c.Run); e != nil {
		return c, e
	}
	return c, tx.Commit(ctx)
}
func (s *Store) Renew(ctx context.Context, c Claim) error {
	tag, e := s.DB.Exec(ctx, "UPDATE runs SET lease_until=now()+interval '30 seconds' WHERE id=$1 AND lease_token=$2 AND lease_until>now() AND status='running' AND NOT cancel_requested", c.Run.ID, c.Token)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return ErrLease
	}
	return nil
}
func fence(ctx context.Context, tx pgx.Tx, c Claim) error {
	var id string
	e := tx.QueryRow(ctx, "SELECT id FROM runs WHERE id=$1 AND lease_token=$2 AND status='running' AND lease_until>now() AND NOT cancel_requested FOR UPDATE", c.Run.ID, c.Token).Scan(&id)
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrLease
	}
	return e
}
func (s *Store) SaveEvidence(ctx context.Context, c Claim, v core.Evidence) error {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = fence(ctx, tx, c); e != nil {
		return e
	}
	v.ID = ID()
	v.BranchID = c.Run.BranchID
	v.CreatedAt = time.Now().UTC()
	_, e = tx.Exec(ctx, "INSERT INTO evidence(id,branch_id,run_id,kind,title,content,source) VALUES($1,$2,$3,$4,$5,$6,$7)", v.ID, v.BranchID, c.Run.ID, v.Kind, v.Title, v.Content, v.Source)
	if e != nil {
		return e
	}
	if e = event(ctx, tx, c.InvestigationID, c.Run.BranchID, "evidence.created", v); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Store) Finish(ctx context.Context, c Claim, output string, runErr error) error {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx) // lock branch before run: same ordering as enqueue/cancel to avoid deadlocks.
	b, e := branch(ctx, tx, c.Run.BranchID, true)
	if e != nil {
		return e
	}
	if e = fence(ctx, tx, c); e != nil {
		return e
	}
	status, msg := "completed", ""
	if runErr != nil {
		status = "failed"
		msg = runErr.Error()
		if len(msg) > 2000 {
			msg = msg[:2000]
		}
	} else {
		ids := []string{}
		rows, e := tx.Query(ctx, "SELECT id FROM evidence WHERE run_id=$1 ORDER BY created_at", c.Run.ID)
		if e != nil {
			return e
		}
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				rows.Close()
				return e
			}
			ids = append(ids, id)
		}
		rows.Close()
		if e = rows.Err(); e != nil {
			return e
		}
		b.Messages = append(b.Messages, core.Message{ID: ID(), Role: "assistant", Content: output, CreatedAt: time.Now().UTC(), ModelID: c.Run.ModelID, EvidenceIDs: ids})
		if e = revision(ctx, tx, &b, core.RevisionProvenance{Kind: "assistant"}); e != nil {
			return e
		}
	}
	_, e = tx.Exec(ctx, "UPDATE runs SET status=$2,error=$3,lease_token=NULL,lease_until=NULL WHERE id=$1", c.Run.ID, status, msg)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, "UPDATE attempts SET status=$2,error=$3,finished_at=now() WHERE id=$1", c.Token, status, msg)
	if e != nil {
		return e
	}
	if e = event(ctx, tx, c.InvestigationID, b.ID, "run."+status, map[string]string{"run_id": c.Run.ID, "error": msg}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Store) Cancel(ctx context.Context, id string) (core.Run, error) {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return core.Run{}, e
	}
	defer tx.Rollback(ctx)
	var bid string
	e = tx.QueryRow(ctx, "SELECT branch_id FROM runs WHERE id=$1", id).Scan(&bid)
	if e != nil {
		return core.Run{}, e
	}
	b, e := branch(ctx, tx, bid, true)
	if e != nil {
		return core.Run{}, e
	}
	r, e := runRow(tx.QueryRow(ctx, "UPDATE runs SET status='cancelled',cancel_requested=true,lease_token=NULL,lease_until=NULL WHERE id=$1 AND status IN('queued','running') RETURNING "+runColumns, id))
	if errors.Is(e, ErrNotFound) {
		return runRow(tx.QueryRow(ctx, "SELECT "+runColumns+" FROM runs WHERE id=$1", id))
	}
	if e != nil {
		return r, e
	}
	if _, e = tx.Exec(ctx, "UPDATE attempts SET status='cancelled',finished_at=now() WHERE run_id=$1 AND status='running'", id); e != nil {
		return r, e
	}
	if e = event(ctx, tx, b.InvestigationID, b.ID, "run.cancelled", r); e != nil {
		return r, e
	}
	return r, tx.Commit(ctx)
}

// AppendDelta durably records visible draft output; it never changes canonical context.
func (s *Store) AppendDelta(ctx context.Context, c Claim, text string) error {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = fence(ctx, tx, c); e != nil {
		return e
	}
	if e = event(ctx, tx, c.InvestigationID, c.Run.BranchID, "attempt.delta", map[string]string{"run_id": c.Run.ID, "attempt_id": c.Token, "delta": text}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
