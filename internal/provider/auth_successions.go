package provider

import (
	"encoding/json"
	"errors"
	"net/http"

	"foryou.local/foryou/internal/apierr"
	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/succession"
	"foryou.local/foryou/internal/wire"
)

func init() {
	routeGroups = append(routeGroups, ascRegister)
}

func ascRegister(s *Service, mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/successions", func(w http.ResponseWriter, r *http.Request) {
		if !s.LimitRead(w, r, ReadOther) {
			return
		}
		if !achJSONContentType(r) {
			writeError(w, http.StatusBadRequest, "invalid_query")
			return
		}
		var raw json.RawMessage
		body, err := decodeJSON(r, 1<<20, &raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_query")
			return
		}
		var value wire.KeySuccession
		if wire.Decode(body, "KeySuccession", &value) != nil {
			writeError(w, http.StatusBadRequest, "invalid_query")
			return
		}
		if succession.Verify(value) != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid_message")
			return
		}
		added, err := s.Successions.Add(value)
		if errors.Is(err, succession.ErrRegistryFull) {
			writeRetry(w, http.StatusTooManyRequests, "rate_limited", apierr.DefaultRetryAfterSeconds)
			return
		}
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid_message")
			return
		}
		if err := s.PersistSuccessions(); err != nil {
			writeRetry(w, http.StatusServiceUnavailable, "unavailable", apierr.DefaultRetryAfterSeconds)
			return
		}
		response, err := canon.Encode(value)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "unavailable")
			return
		}
		status := http.StatusOK
		if added {
			status = http.StatusCreated
		}
		writeCanonical(w, status, response, "no-store")
	})
	mux.HandleFunc("GET /v1/successions/{did}", func(w http.ResponseWriter, r *http.Request) {
		if !s.LimitRead(w, r, ReadDirect) {
			return
		}
		did := r.PathValue("did")
		publicKey, err := keys.ParseDID(did)
		if err != nil || keys.DID(publicKey) != did {
			writeError(w, http.StatusBadRequest, "invalid_query")
			return
		}
		values := s.Successions.ForOld(did)
		if len(values) == 0 {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		response, err := canon.Encode(struct {
			Successions []wire.KeySuccession `json:"successions"`
		}{Successions: values})
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "unavailable")
			return
		}
		writeCanonical(w, http.StatusOK, response, "public, max-age=60")
	})
}
