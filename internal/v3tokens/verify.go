package v3tokens

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/jackc/pgx/v5"
)

// TokenPrincipal identifies a machine credential separately from a human
// session. In particular, OwnerID is an authorization ceiling, not a human
// login or permission to use cookie-only management routes.
type TokenPrincipal struct {
	TokenID        string
	OwnerID        string
	EnvironmentID  string
	CredentialKind string
}

// validSecret rejects alternate encodings of the issued 256-bit random value.
// The digest includes the prefix, exactly as it did at issuance.
func validSecret(plain string) bool {
	if !strings.HasPrefix(plain, secretPrefix) || len(plain) != len(secretPrefix)+43 {
		return false
	}
	encoded := plain[len(secretPrefix):]
	random, err := base64.RawURLEncoding.DecodeString(encoded)
	return err == nil && len(random) == 32 &&
		base64.RawURLEncoding.EncodeToString(random) == encoded
}

// VerifyWithTx attenuates a token to one exact V3 object-level read decision.
// The caller must resolve the real application/team scope, use the selected
// environment in decision, and keep tx open through the protected read. This
// method deliberately does not authenticate HTTP requests or return core.User.
// In particular, a token cannot create tokens, approve or execute actions.
func (s *Service) VerifyWithTx(ctx context.Context, tx pgx.Tx, plain string, decision rbac.V3Decision) (TokenPrincipal, error) {
	var principal TokenPrincipal
	if s == nil || s.Access == nil || tx == nil {
		return principal, ErrUnavailable
	}
	if !validSecret(plain) {
		return principal, ErrInvalidToken
	}
	allowedModule, knownScope := allowedScopes[decision.Module+":view"]
	if decision.Action != "view" || !knownScope || allowedModule != decision.Module ||
		decision.EnvironmentID == "" || decision.EnvironmentID == "all" ||
		strings.TrimSpace(decision.EnvironmentID) != decision.EnvironmentID {
		return principal, ErrForbidden
	}

	digest := sha256.Sum256([]byte(plain))
	var tokenID, ownerID string
	// A hash-index lookup avoids taking shared governance locks for random
	// invalid credentials. The row and owner are rechecked under locks below.
	err := tx.QueryRow(ctx, `SELECT id,owner_id FROM v3_api_tokens
	 WHERE secret_hash=$1`, digest[:]).Scan(&tokenID, &ownerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return principal, ErrInvalidToken
	}
	if err != nil {
		return principal, err
	}
	if err = lockGovernance(ctx, tx); err != nil {
		return principal, err
	}
	var kind, source string
	err = tx.QueryRow(ctx, `SELECT v3_identity_kind,source FROM users
	 WHERE id=$1 AND NOT disabled AND deleted_at IS NULL FOR SHARE`, ownerID).Scan(&kind, &source)
	if errors.Is(err, pgx.ErrNoRows) {
		return principal, ErrInvalidToken
	}
	if err != nil {
		return principal, err
	}
	if kind != "human" || source == "settings_demo" {
		return principal, ErrInvalidToken
	}

	var scopes []string
	err = tx.QueryRow(ctx, `SELECT scopes FROM v3_api_tokens
	 WHERE id=$1 AND owner_id=$2 AND secret_hash=$3
	 AND environment_id=$4 AND revoked_at IS NULL
	 AND expires_at>clock_timestamp() FOR SHARE`,
		tokenID, ownerID, digest[:], decision.EnvironmentID).Scan(&scopes)
	if errors.Is(err, pgx.ErrNoRows) {
		return principal, ErrInvalidToken
	}
	if err != nil {
		return principal, err
	}
	wantScope := decision.Module + ":view"
	allowed := false
	for _, scope := range scopes {
		if scope == wantScope {
			allowed = true
			break
		}
	}
	if !allowed {
		return principal, ErrForbidden
	}
	// Current owner permissions remain an upper bound, including the
	// persisted object's application/team grant and environment membership.
	if err = s.Access.AuthorizeWithTx(ctx, tx, core.User{ID: ownerID}, decision); err != nil {
		return principal, err
	}
	principal = TokenPrincipal{
		TokenID: tokenID, OwnerID: ownerID,
		EnvironmentID: decision.EnvironmentID, CredentialKind: "api_token",
	}
	return principal, nil
}
