// Package v3templatecapacity coordinates private template saves and system
// imports against the bounded V3 template list.
package v3templatecapacity

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

const MaxVisibleItems = 500

var ErrFull = errors.New("canvas template list capacity reached")

// Lock must be held by every template insert transaction before checking
// capacity. Under READ COMMITTED, the subsequent count sees the preceding
// writer's commit after this lock is acquired.
func Lock(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text,0))`,
		"v3.canvas.template.capacity")
	return err
}

// CheckPrivate counts every private template owned by this user, including
// environments hidden by a narrower list request. Lock must already be held.
func CheckPrivate(ctx context.Context, tx pgx.Tx, ownerID string) error {
	var total int64
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM v3_canvas_templates
 WHERE owner_id IS NULL OR owner_id=$1`, ownerID).Scan(&total); err != nil {
		return err
	}
	if total >= MaxVisibleItems {
		return ErrFull
	}
	return nil
}

// CheckSystem protects every user's possible list: each system template is
// visible to all users, so the largest private collection sets the bound.
// Lock must already be held.
func CheckSystem(ctx context.Context, tx pgx.Tx) error {
	var largestVisible int64
	if err := tx.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM v3_canvas_templates WHERE owner_id IS NULL)
 + COALESCE((SELECT max(private_count) FROM (
   SELECT count(*) AS private_count FROM v3_canvas_templates
   WHERE owner_id IS NOT NULL GROUP BY owner_id
 ) owner_counts),0)`).Scan(&largestVisible); err != nil {
		return err
	}
	if largestVisible >= MaxVisibleItems {
		return ErrFull
	}
	return nil
}
