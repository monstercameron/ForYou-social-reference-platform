package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPlannedPageFallbackCanBeReplaced(t *testing.T) {
	previous := routeGroups
	routeGroups = append(routeGroups, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /wallet", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("wallet feature")) })
	})
	t.Cleanup(func() { routeGroups = previous })
	client := newTestClient(t, false)
	w := httptest.NewRecorder()
	client.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/wallet", nil))
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "not available yet") || !strings.Contains(w.Body.String(), "wallet feature") {
		t.Errorf("feature wallet response = %d %q", w.Code, w.Body.String())
	}
}

func TestCatchAllDoesNotHidePlannedLanding(t *testing.T) {
	previous := routeGroups
	routeGroups = append(routeGroups, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("catch all")) })
	})
	t.Cleanup(func() { routeGroups = previous })
	client := newTestClient(t, false)
	w := httptest.NewRecorder()
	client.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/identity", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Identity and keys") || !strings.Contains(w.Body.String(), "This page is not available yet.") {
		t.Errorf("identity landing = %d %q; GET / catch-all should not replace its specific route", w.Code, w.Body.String())
	}
}
