package httpapi

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/store"
)

// SessionMissing reports a session lookup that found no live session: the
// token is unknown, expired or revoked, or its account is disabled. Only these
// mean the caller is signed out.
func SessionMissing(err error) bool {
	return errors.Is(err, pgx.ErrNoRows) || errors.Is(err, store.ErrSessionRevoked) || errors.Is(err, store.ErrNotFound)
}

// SessionError answers a failed session lookup (PA-59). A missing session is
// 401; any other failure means the session store could not be read, so the
// answer is 503 and clients keep the user signed in and retry.
func SessionError(w http.ResponseWriter, err error) {
	if SessionMissing(err) {
		Error(w, http.StatusUnauthorized, "unauthorized", "Session expired")
		return
	}
	Error(w, http.StatusServiceUnavailable, "session_store_unavailable", "Sessions cannot be checked right now; retry shortly")
}
