package store

import (
	"context"
	"errors"
	"log/slog"
	"strings"
)

const (
	exampleDatabasePassword = "replace-with-random-value"
	exampleAdminPassword    = "replace-with-long-random-password"
)

// IsPublishedPlaceholder matches credentials previously shipped in the
// project's example configuration. Match exact password bytes, like the hash.
func IsPublishedPlaceholder(value string) bool {
	switch value {
	case exampleDatabasePassword, exampleAdminPassword:
		return true
	}
	return false
}

// IsPlaceholderSecret also rejects the placeholder convention in startup configuration.
func IsPlaceholderSecret(value string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(value)), "replace-with-")
}

// RetirePlaceholderAdminPassword repairs previously bootstrapped accounts.
// Limit password-hash checks to the configured and original default addresses.
// The user lock serializes rotation with password changes and session creation.
func (s *Store) RetirePlaceholderAdminPassword(ctx context.Context, email, replacement string) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, `SELECT u.id,u.password_hash FROM users u
 WHERE lower(u.email)=ANY($1) AND u.password_hash<>'' AND NOT u.disabled AND u.deleted_at IS NULL
   AND EXISTS (SELECT 1 FROM rbac_platform_administrators a WHERE a.user_id=u.id)
 ORDER BY u.id FOR UPDATE OF u`, []string{strings.ToLower(strings.TrimSpace(email)), "admin@localhost"})
	if err != nil {
		return err
	}
	var matches []string
	for rows.Next() {
		var id, hash string
		if err = rows.Scan(&id, &hash); err != nil {
			rows.Close()
			return err
		}
		if CheckPassword(exampleAdminPassword, hash) || CheckPassword(exampleDatabasePassword, hash) {
			matches = append(matches, id)
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}

	if len(matches) > 0 && (len(replacement) < 12 || len(replacement) > 1024 || IsPlaceholderSecret(replacement)) {
		return errors.New("set AIOPS_ADMIN_PASSWORD to a new random password of 12 to 1024 characters and restart; startup will replace the example administrator password and revoke its sessions")
	}
	for _, id := range matches {
		if _, err = tx.Exec(ctx, `UPDATE users SET password_hash=$2 WHERE id=$1`, id, PasswordHash(replacement)); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, id); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,actor_kind,action,target,outcome,changes)
 VALUES('','system','bootstrap.placeholder_password.replaced',$1,'success','{"sessions_revoked":true}'::jsonb)`, id); err != nil {
			return err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	if len(matches) > 0 {
		slog.Warn("Replaced example administrator passwords and revoked sessions", "accounts", len(matches))
	}
	return nil
}
