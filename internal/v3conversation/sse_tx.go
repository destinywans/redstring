package v3conversation

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
)

type sseOpen struct {
	environment  string
	reply        string
	cursor       int64
	pastTerminal bool
}

type sseRequestError struct {
	code, message string
}

func (e *sseRequestError) Error() string { return e.message }

func (s *Service) sseTxMode() (bool, error) {
	if s.opts.RefreshSessionTx == nil && s.opts.AllowedEnvironmentsTx == nil {
		return false, nil
	}
	if s.opts.RefreshSessionTx == nil || s.opts.AllowedEnvironmentsTx == nil || s.opts.AuthorizeBackgroundTurnTx == nil {
		return false, ErrForbidden
	}
	return true, nil
}

// withSSEReadTx owns the complete connection lifetime. No HTTP writer is passed
// into a read phase. Source authorization may take its existing SHARE locks,
// so this is an ordinary transaction, containing only the existing read work.
func (s *Service) withSSEReadTx(ctx context.Context, read func(pgx.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadWrite})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = read(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) allowedSSERead(ctx context.Context, tx pgx.Tx, user core.User, requested string) ([]string, error) {
	if tx == nil {
		return s.allowed(ctx, user, requested)
	}
	visible, err := s.opts.AllowedEnvironmentsTx(ctx, tx, user)
	if err != nil {
		return nil, err
	}
	return selectAllowedEnvironments(visible, requested)
}

func (s *Service) readSSEOpen(ctx context.Context, r *http.Request, id string, user core.User, useTx bool) (sseOpen, error) {
	var open sseOpen
	read := func(q sseReadQueryer, tx pgx.Tx) error {
		// Persisted ownership determines EventSource scope, never a mutable
		// browser default. Keep validation after these original access checks.
		var err error
		open.environment, _, err = s.ownedQuery(ctx, q, id, user.ID, "")
		if err != nil {
			return err
		}
		if _, err = s.allowedSSERead(ctx, tx, user, open.environment); err != nil {
			return err
		}
		var valid bool
		open.cursor, valid = lastEventCursor(r.Header.Values("Last-Event-ID"))
		if !valid {
			return &sseRequestError{"invalid_event_cursor", "Last-Event-ID must be a nonnegative event ID"}
		}
		requestedReply := r.URL.Query().Get("reply_id")
		if values, present := r.URL.Query()["reply_id"]; present {
			if len(values) != 1 || requestedReply == "" || strings.TrimSpace(requestedReply) != requestedReply || len(requestedReply) > 128 {
				return &sseRequestError{"invalid_reply", "Reply ID is unavailable for this conversation"}
			}
		}
		if open.cursor > 0 {
			// Global event IDs still resolve to their original authorized reply.
			err = q.QueryRow(ctx, `SELECT e.reply_id, EXISTS (
			SELECT 1 FROM v3_conversation_events terminal
			WHERE terminal.conversation_id=e.conversation_id AND terminal.reply_id=e.reply_id
				AND terminal.seq<=e.seq AND terminal.payload->>'type'='message.completed'
		) FROM v3_conversation_events e
			WHERE e.seq=$1 AND e.conversation_id=$2`, open.cursor, id).Scan(&open.reply, &open.pastTerminal)
			if errors.Is(err, pgx.ErrNoRows) {
				return &sseRequestError{"invalid_event_cursor", "Last-Event-ID is unavailable for this conversation"}
			}
			if err == nil && requestedReply != "" && requestedReply != open.reply {
				return &sseRequestError{"invalid_event_cursor", "Last-Event-ID does not belong to this reply"}
			}
		} else if requestedReply != "" {
			err = q.QueryRow(ctx, `SELECT id FROM v3_conversation_replies
			WHERE id=$1 AND conversation_id=$2`, requestedReply, id).Scan(&open.reply)
			if errors.Is(err, pgx.ErrNoRows) {
				return &sseRequestError{"invalid_reply", "Reply ID is unavailable for this conversation"}
			}
		} else {
			open.reply, err = s.latestReplyQuery(ctx, q, id)
		}
		if err != nil {
			return err
		}
		return s.authorizeReplayQuery(ctx, q, tx, id, open.reply, user.ID, open.environment)
	}
	if !useTx {
		err := read(s.db, nil)
		return open, err
	}
	err := s.withSSEReadTx(ctx, func(tx pgx.Tx) error { return read(tx, tx) })
	return open, err
}

func (s *Service) readSSEBatch(ctx context.Context, token, id, owner, env, reply string, cursor int64, useTx bool) ([]replayEvent, bool, error) {
	var batch []replayEvent
	var more bool
	read := func(q sseReadQueryer, tx pgx.Tx) error {
		var current core.User
		var err error
		if tx == nil {
			current, err = s.opts.RefreshSession(ctx, token)
		} else {
			current, err = s.opts.RefreshSessionTx(ctx, tx, token)
		}
		if err != nil {
			return err
		}
		if current.ID != owner || current.Disabled {
			return ErrForbidden
		}
		if _, _, err = s.ownedQuery(ctx, q, id, current.ID, env); err != nil {
			return err
		}
		if _, err = s.allowedSSERead(ctx, tx, current, env); err != nil {
			return err
		}
		if err = s.authorizeReplayQuery(ctx, q, tx, id, reply, current.ID, env); err != nil {
			return err
		}
		batch, more, err = s.replayBatchQuery(ctx, q, reply, cursor)
		return err
	}
	var err error
	if useTx {
		err = s.withSSEReadTx(ctx, func(tx pgx.Tx) error { return read(tx, tx) })
	} else {
		err = read(s.db, nil)
	}
	if err != nil {
		return nil, false, err
	}
	return batch, more, nil
}
