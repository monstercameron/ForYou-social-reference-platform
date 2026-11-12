package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientRoutes(t *testing.T) {
	check := func(t *testing.T, path, want string, status int) {
		t.Helper()
		w := httptest.NewRecorder()
		testClientHandler(t).ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != status || !strings.Contains(w.Body.String(), want) {
			t.Errorf("%s = %d %q; want %d containing %q", path, w.Code, w.Body.String(), status, want)
		}
	}
	t.Run("homepage", func(t *testing.T) { check(t, "/", "ForYou", http.StatusOK) })
	t.Run("settings", func(t *testing.T) { check(t, "/settings", "Settings", http.StatusOK) })
	t.Run("compose", func(t *testing.T) { check(t, "/compose", "New post", http.StatusOK) })
	t.Run("explore", func(t *testing.T) { check(t, "/explore", "Explore", http.StatusOK) })
	t.Run("unknown route", func(t *testing.T) { check(t, "/missing", "Page not found", http.StatusNotFound) })
	t.Run("settings rejects POST with method not allowed", func(t *testing.T) {
		w := httptest.NewRecorder()
		testClientHandler(t).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/settings", nil))
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST /settings = %d; want 405", w.Code)
		}
	})
}

func TestClientUnknownPageShellAndHealth(t *testing.T) {
	h := testClientHandler(t)
	missing := httptest.NewRecorder()
	h.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/missing", nil))
	if missing.Code != http.StatusNotFound || !strings.Contains(missing.Header().Get("Content-Type"), "text/html") || !strings.Contains(missing.Body.String(), `aria-label="Main navigation"`) || !strings.Contains(missing.Body.String(), `href="/">Go to Home</a>`) {
		t.Errorf("unknown page = %d %q; want styled 404 shell with home link", missing.Code, missing.Body.String())
	}
	health := httptest.NewRecorder()
	h.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK || !strings.Contains(health.Header().Get("Content-Type"), "application/json") || !strings.Contains(health.Body.String(), `"status":"ok"`) {
		t.Errorf("health response = %d %q; want JSON health response", health.Code, health.Body.String())
	}
}
