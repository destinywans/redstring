// Package webui serves the built workspace without routing API failures into HTML.
package webui

import (
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
)

type Handler struct {
	root  *os.Root
	files http.Handler
}

func New(directory string) (*Handler, error) {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, fmt.Errorf("open workspace build: %w", err)
	}
	info, err := fs.Stat(root.FS(), "index.html")
	if err != nil || !info.Mode().IsRegular() {
		root.Close()
		return nil, fmt.Errorf("workspace build requires a regular index.html")
	}
	return &Handler{root: root, files: http.FileServerFS(root.FS())}, nil
}

func (h *Handler) Close() error { return h.root.Close() }

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	for _, part := range strings.Split(r.URL.Path, "/") {
		if strings.HasPrefix(part, ".") {
			http.NotFound(w, r)
			return
		}
	}
	if name == "api" || strings.HasPrefix(name, "api/") || name == "healthz" || name == "openapi.json" {
		http.NotFound(w, r)
		return
	}
	info, err := fs.Stat(h.root.FS(), name)
	if err == nil && info.Mode().IsRegular() {
		if strings.HasPrefix(name, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		h.files.ServeHTTP(w, r)
		return
	}
	// Asset misses and directories must never become a successful HTML response.
	if strings.HasPrefix(name, "assets/") || path.Ext(name) != "" || (err == nil && info.IsDir() && name != "." && name != "") {
		http.NotFound(w, r)
		return
	}
	if accept := r.Header.Get("Accept"); accept != "" && !strings.Contains(accept, "text/html") && !strings.Contains(accept, "*/*") {
		http.NotFound(w, r)
		return
	}
	file, err := h.root.Open("index.html")
	if err != nil {
		http.Error(w, "Workspace unavailable", http.StatusServiceUnavailable)
		return
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		http.Error(w, "Workspace unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	http.ServeContent(w, r, "index.html", stat.ModTime(), file)
}
