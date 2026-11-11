package provider

import (
	"net/http"

	"foryou.local/foryou/internal/store"
)

func registerHealth(s *Service, mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeCanonical(w, http.StatusOK, []byte(`{"role":"provider","status":"ok"}`), "no-store")
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ready := true
		if err := s.content.View(func(_ store.Tx) error { return nil }); err != nil {
			ready = false
		}
		if err := s.controls.View(func(_ store.Tx) error { return nil }); err != nil {
			ready = false
		}
		if s.controlsState.Ready() != nil {
			ready = false
		}
		if !ready {
			writeRetry(w, http.StatusServiceUnavailable, "unavailable", 5)
			return
		}
		writeCanonical(w, http.StatusOK, []byte(`{"role":"provider","status":"ready"}`), "no-store")
	})
}
