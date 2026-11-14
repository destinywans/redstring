package main

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"

	"github.com/destinywans/redstring/internal/apicontract"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5/pgconn"
)

func validateStartupDatabaseSecret(dsn string) error {
	if dsn == "" {
		return errors.New("DATABASE_URL is required")
	}
	cfg, err := pgconn.ParseConfig(dsn)
	if err != nil {
		// Parser errors can contain the original connection string.
		return errors.New("invalid DATABASE_URL or PostgreSQL connection configuration")
	}
	if store.IsPlaceholderSecret(cfg.Password) {
		return errors.New("DATABASE_URL must use a real PostgreSQL password; example values are not accepted")
	}
	return nil
}

func warnMissingPublicOrigin(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || strings.EqualFold(host, "localhost") {
		return false
	}
	if ip, err := netip.ParseAddr(host); err == nil && ip.Unmap().IsLoopback() {
		return false
	}
	// The stock Compose service binds all IPv4 addresses inside the
	// container but publishes only on host loopback.
	if addr == "0.0.0.0:8080" {
		if _, err := os.Stat("/.dockerenv"); err == nil {
			return false
		}
	}
	return true
}

func rootHandler(api, web http.Handler, policy httpapi.HostPolicy) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /openapi.json", apicontract.ServeHTTP)
	mux.Handle("/api/", api)
	mux.Handle("/api", api)
	mux.Handle("/healthz", api)
	if web == nil {
		web = api
	}
	mux.Handle("/", web)
	return policy.Wrap(mux)
}
