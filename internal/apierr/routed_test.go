package apierr

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRouted(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/things/{id}", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("thing " + r.PathValue("id"))) })
	mux.HandleFunc("PUT /v1/things/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusCreated) })
	handler := Routed(mux)
	answer := func(method, path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(method, path, nil))
		return w
	}
	t.Run("a routed request reaches its handler with its path values", func(t *testing.T) {
		if w := answer(http.MethodGet, "/v1/things/7"); w.Code != http.StatusOK || w.Body.String() != "thing 7" {
			t.Errorf("status %d, body %q; want 200 and the handler's body", w.Code, w.Body.String())
		}
	})
	t.Run("an unknown path is not_found in the public error body", func(t *testing.T) {
		w := answer(http.MethodGet, "/v1/nothing")
		if w.Code != http.StatusNotFound || w.Body.String() != `{"status":"not_found"}` || w.Header().Get("Content-Type") != "application/json" || w.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("status %d, body %q, headers %v; want 404 JSON not_found with no-store", w.Code, w.Body.String(), w.Header())
		}
	})
	t.Run("an undefined method names the defined ones and has no body", func(t *testing.T) {
		w := answer(http.MethodPost, "/v1/things/7")
		if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "GET, HEAD, PUT" || w.Header().Get("Cache-Control") != "no-store" || w.Body.Len() != 0 {
			t.Errorf("status %d, Allow %q, Cache-Control %q, body %q; want 405, the three methods, no-store and no body", w.Code, w.Header().Get("Allow"), w.Header().Get("Cache-Control"), w.Body.String())
		}
	})
}
