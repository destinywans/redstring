// Package apicontract embeds the versioned public contract in the service binary.
package apicontract

import (
	_ "embed"
	"net/http"
)

//go:embed openapi.json
var OpenAPI []byte

func ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(OpenAPI)
}
