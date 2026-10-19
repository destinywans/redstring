package v3integrations

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/destinywans/redstring/internal/v3tokens"
	"github.com/jackc/pgx/v5"
)

// BearerConnectorList is the only API-token consumer in this module. The
// composition root must dispatch ONLY an exact GET /api/v1/connectors request
// with an Authorization header to this handler before the Cookie-only V3 gate.
// The ordinary registered Cookie handler continues to serve every other case.
// knownEnvironments must be the instance's configured environment IDs.
func (s *Service) BearerConnectorList(tokens *v3tokens.Service, knownEnvironments []string) http.HandlerFunc {
	known := make(map[string]bool, len(knownEnvironments))
	for _, env := range knownEnvironments {
		known[env] = true
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/connectors" {
			httpapi.Error(w, http.StatusNotFound, "not_found", "Route not found")
			return
		}
		if _, err := r.Cookie("aiops_session"); err == nil {
			httpapi.Error(w, http.StatusUnauthorized, "unauthorized", "Choose one authentication method")
			return
		}
		authorizations := r.Header.Values("Authorization")
		if len(authorizations) != 1 || !strings.HasPrefix(authorizations[0], "Bearer ") ||
			strings.TrimSpace(authorizations[0]) != authorizations[0] ||
			strings.ContainsAny(authorizations[0][len("Bearer "):], " \t,\r\n") {
			httpapi.Error(w, http.StatusUnauthorized, "unauthorized", "Invalid API token")
			return
		}
		plain := strings.TrimPrefix(authorizations[0], "Bearer ")
		environments := r.Header.Values("X-AIOps-Environment")
		if len(environments) != 1 || environments[0] == "" ||
			strings.TrimSpace(environments[0]) != environments[0] || strings.Contains(environments[0], ",") ||
			environments[0] == "all" || !known[environments[0]] {
			httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_environment", "Select one known environment")
			return
		}
		if s == nil || s.DB == nil || tokens == nil || tokens.DB != s.DB || tokens.Access == nil {
			httpapi.Error(w, http.StatusServiceUnavailable, "tokens_unavailable", "API token verification is unavailable")
			return
		}
		category := r.URL.Query().Get("category")
		if category == "" {
			category = "all"
		}
		validCategory := false
		for _, c := range categories {
			validCategory = validCategory || c == category
		}
		if !validCategory {
			httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_category", "Unknown connector category")
			return
		}
		query := strings.TrimSpace(r.URL.Query().Get("q"))
		if len(query) > 200 {
			httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_query", "Search is too long")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		// READ COMMITTED is deliberate: VerifyWithTx first looks up the digest,
		// then locks current governance rows. A repeatable-read snapshot taken
		// before those locks could accept permissions revoked while waiting.
		// The transaction performs no mutations, but governance locking needs
		// a write-capable transaction on PostgreSQL.
		tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
		if err != nil {
			httpapi.Error(w, http.StatusServiceUnavailable, "storage_unavailable", "Connector storage is unavailable")
			return
		}
		defer tx.Rollback(ctx)
		env := environments[0]
		_, err = tokens.VerifyWithTx(ctx, tx, plain, rbac.V3Decision{
			Module: "connectors", Action: "view", EnvironmentID: env,
		})
		if err != nil {
			bearerListError(w, err)
			return
		}
		// Connector registrations have an environment owner, not an application
		// or team owner. VerifyWithTx already checked the exact current owner
		// grant for this environment; every row is bounded to that environment.
		s.listInTx(w, r, tx, []string{env}, category, query, true,
			func(_ context.Context, _ pgx.Tx, c connector) error {
				if c.EnvironmentID != env {
					return store.ErrForbidden
				}
				return nil
			}, func(ctx context.Context, tx pgx.Tx) error {
				// Revocation is serialized by the held row locks. Recheck natural
				// expiry just before committing the response snapshot.
				_, err := tokens.VerifyWithTx(ctx, tx, plain, rbac.V3Decision{
					Module: "connectors", Action: "view", EnvironmentID: env,
				})
				return err
			})
	}
}

func bearerListError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, v3tokens.ErrInvalidToken):
		httpapi.Error(w, http.StatusUnauthorized, "unauthorized", "Invalid API token")
	case errors.Is(err, v3tokens.ErrForbidden), errors.Is(err, store.ErrForbidden):
		httpapi.Error(w, http.StatusForbidden, "forbidden", "Connector access denied")
	default:
		httpapi.Error(w, http.StatusServiceUnavailable, "authorization_unavailable", "Connector authorization unavailable")
	}
}
