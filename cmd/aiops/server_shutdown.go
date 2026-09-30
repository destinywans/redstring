package main

import (
	"context"
	"errors"
	"net/http"
)

// Closing active transports after the grace period also cancels their request
// contexts. Otherwise an SSE socket or DB query can outlive shutdown and block
// the deferred pgxpool.Close indefinitely.
func shutdownHTTPServer(ctx context.Context, server *http.Server) error {
	if err := server.Shutdown(ctx); err != nil {
		return errors.Join(err, server.Close())
	}
	return nil
}
