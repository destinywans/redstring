package webui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceRoutingAndIsolation(t *testing.T) {
	directory := t.TempDir()
	for name, body := range map[string]string{"index.html": "<html>workspace</html>", "assets/app-a1.js": "console.log('app')", ".env": "private"} {
		filename := filepath.Join(directory, name)
		if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filename, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(directory, "assets", "outside.txt")); err != nil {
		t.Fatal(err)
	}
	h, err := New(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	for _, tc := range []struct {
		method, path, accept string
		status               int
		body                 string
	}{
		{"GET", "/", "text/html", 200, "workspace"},
		{"GET", "/investigations/123", "text/html", 200, "workspace"},
		{"HEAD", "/investigations/123", "text/html", 200, ""},
		{"GET", "/assets/app-a1.js", "*/*", 200, "console.log"},
		{"GET", "/assets/missing.js", "text/html", 404, ""},
		{"GET", "/assets", "text/html", 404, ""},
		{"GET", "/missing.json", "*/*", 404, ""},
		{"GET", "/api/v1/missing", "text/html", 404, ""},
		{"GET", "/.env", "text/html", 404, ""},
		{"GET", "/assets/outside.txt", "*/*", 404, ""},
		{"GET", "/unknown", "application/json", 404, ""},
		{"POST", "/investigations/123", "text/html", 405, ""},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, nil)
			r.Header.Set("Accept", tc.accept)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.body) {
				t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "outside") {
				t.Fatal("private content leaked")
			}
			if tc.method == "HEAD" && w.Body.Len() != 0 {
				t.Fatal("HEAD returned content")
			}
			if w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Content-Security-Policy") == "" {
				t.Fatal("missing security headers")
			}
			if tc.path == "/assets/app-a1.js" && !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
				t.Fatal("asset not cached")
			}
			if tc.path == "/" && w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("HTML can be stale")
			}
		})
	}
}

func TestWorkspaceRequiresBuild(t *testing.T) {
	if h, err := New(t.TempDir()); err == nil {
		h.Close()
		t.Fatal("empty directory accepted")
	}
}

var _ http.Handler = (*Handler)(nil)
