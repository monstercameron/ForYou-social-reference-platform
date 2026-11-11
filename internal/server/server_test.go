package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadiness(t *testing.T) {
	check := func(t *testing.T, ping func(context.Context) error, status int, body string) {
		t.Helper()
		w := httptest.NewRecorder()
		newHandler("provider", ping).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if w.Code != status || !strings.Contains(w.Body.String(), body) {
			t.Errorf("readiness = %d %q; want %d containing %q", w.Code, w.Body.String(), status, body)
		}
	}
	t.Run("missing database", func(t *testing.T) { check(t, nil, http.StatusServiceUnavailable, "not_configured") })
	t.Run("database unavailable", func(t *testing.T) {
		check(t, func(context.Context) error { return errors.New("secret connection detail") }, http.StatusServiceUnavailable, "unavailable")
	})
	t.Run("database ready", func(t *testing.T) {
		check(t, func(context.Context) error { return nil }, http.StatusOK, "ready")
	})
}

func TestRoleRoutes(t *testing.T) {
	check := func(t *testing.T, role, path, want string, status int) {
		t.Helper()
		w := httptest.NewRecorder()
		newHandler(role, nil).ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != status || !strings.Contains(w.Body.String(), want) {
			t.Errorf("%s %s = %d %q; want %d containing %q", role, path, w.Code, w.Body.String(), status, want)
		}
	}
	t.Run("client homepage", func(t *testing.T) { check(t, "client", "/", "ForYou", http.StatusOK) })
	t.Run("provider health", func(t *testing.T) { check(t, "provider", "/healthz", "provider", http.StatusOK) })
	t.Run("foundation identity", func(t *testing.T) { check(t, "foundation", "/", "foundation", http.StatusOK) })
	t.Run("replica identity", func(t *testing.T) { check(t, "replica", "/", "replica", http.StatusOK) })
	t.Run("client settings", func(t *testing.T) { check(t, "client", "/settings", "Settings", http.StatusOK) })
	t.Run("client explore", func(t *testing.T) { check(t, "client", "/explore", "Explore", http.StatusOK) })
	t.Run("provider excludes settings", func(t *testing.T) { check(t, "provider", "/settings", "404", http.StatusNotFound) })
	t.Run("foundation excludes explore", func(t *testing.T) { check(t, "foundation", "/explore", "404", http.StatusNotFound) })
	t.Run("unknown route", func(t *testing.T) { check(t, "client", "/missing", "404", http.StatusNotFound) })
}

func TestReplicaRejectsClientFeedback(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/feedback", strings.NewReader("id=maya&vote=up"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	newHandler("replica", nil).ServeHTTP(w, r)
	if w.Code != http.StatusNotFound || len(w.Result().Cookies()) != 0 {
		t.Errorf("replica feedback response: status %d, cookies %d", w.Code, len(w.Result().Cookies()))
	}
}
