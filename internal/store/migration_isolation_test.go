package store

import (
	"context"
	"testing"
)

func TestMigrationsKeepConstraintsInEachSchema(t *testing.T) {
	first := isolated(t)
	second := isolated(t)
	for _, db := range []*Store{first, second} {
		var count int
		err := db.DB.QueryRow(context.Background(), `SELECT count(*) FROM pg_constraint
			WHERE conrelid='v3_runbooks'::regclass
			AND conname='v3_runbooks_conversation_source_check'`).Scan(&count)
		if err != nil || count != 1 {
			t.Fatalf("runbook constraint in isolated schema: count=%d err=%v", count, err)
		}
	}
}
