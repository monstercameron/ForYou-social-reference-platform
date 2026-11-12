package provider

import (
	"encoding/json"
	"net/http"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/retention"
	"foryou.local/foryou/internal/wire"
)

type retentionRequest struct {
	RetentionSeconds int64           `json:"retention_seconds"`
	EffectiveAt      json.RawMessage `json:"effective_at"`
}

func init() {
	routeGroups = append(routeGroups, retentionRoutes)
}

func retentionRoutes(s *Service, mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/retention", func(w http.ResponseWriter, r *http.Request) {
		if !s.LimitRead(w, r, ReadSettlement) {
			return
		}
		declarations := s.Declarations()
		if len(declarations) > 1000 {
			declarations = declarations[:1000]
		}
		if declarations == nil {
			declarations = []wire.RetentionDeclaration{}
		}
		body, err := canon.Encode(wire.RetentionDeclarationsPage{Declarations: declarations})
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "unavailable")
			return
		}
		writeCanonical(w, http.StatusOK, body, "no-store")
	})
	if !s.Dev() {
		return
	}
	mux.HandleFunc("POST /dev/retention", func(w http.ResponseWriter, r *http.Request) {
		if !isLoopback(r) {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		var request retentionRequest
		if _, err := decodeJSON(r, 4096, &request); err != nil || request.RetentionSeconds < retention.MinSeconds {
			writeError(w, http.StatusBadRequest, "invalid_query")
			return
		}
		effectiveAt := s.Now()
		if len(request.EffectiveAt) > 0 {
			var effectiveAtValue string
			if err := json.Unmarshal(request.EffectiveAt, &effectiveAtValue); err != nil {
				writeError(w, http.StatusBadRequest, "invalid_query")
				return
			}
			parsed, err := time.Parse("2006-01-02T15:04:05Z", effectiveAtValue)
			if err != nil || parsed.Format("2006-01-02T15:04:05Z") != effectiveAtValue || parsed.Before(s.Now()) {
				writeError(w, http.StatusBadRequest, "invalid_query")
				return
			}
			effectiveAt = parsed
		}
		declaration, err := retention.Declare(s.Key(), request.RetentionSeconds, s.Config().Retention.Payout, effectiveAt)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_query")
			return
		}
		if err := s.AddDeclaration(declaration); err != nil {
			if effectiveAt.Before(s.Now()) {
				writeError(w, http.StatusBadRequest, "invalid_query")
			} else {
				writeError(w, http.StatusServiceUnavailable, "unavailable")
			}
			return
		}
		body, err := canon.Encode(declaration)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "unavailable")
			return
		}
		writeCanonical(w, http.StatusCreated, body, "no-store")
	})
}
