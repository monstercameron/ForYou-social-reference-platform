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
		mux.HandleFunc("GET /network", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("network feature")) })
	})
	t.Cleanup(func() { routeGroups = previous })
	client := newTestClient(t, false)
	w := httptest.NewRecorder()
	client.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/network", nil))
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "not available yet") || !strings.Contains(w.Body.String(), "network feature") {
		t.Errorf("feature network response = %d %q", w.Code, w.Body.String())
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
	client.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/network", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Network") || !strings.Contains(w.Body.String(), "This page is not available yet.") {
		t.Errorf("network landing = %d %q; GET / catch-all should not replace its specific route", w.Code, w.Body.String())
	}
}
