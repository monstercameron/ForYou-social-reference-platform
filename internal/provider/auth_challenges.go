package provider

import (
	"encoding/json"
	"net/http"
	"strings"

	"foryou.local/foryou/internal/apierr"
	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/delegation"
)

func init() {
	routeGroups = append(routeGroups, achRegister)
}

func achRegister(s *Service, mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/service-challenges", func(w http.ResponseWriter, r *http.Request) {
		if !achJSONContentType(r) {
			writeError(w, http.StatusBadRequest, "invalid_query")
			return
		}
		var raw json.RawMessage
		body, err := decodeJSON(r, delegation.MaxChallengeRequestBytes, &raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_query")
			return
		}
		request, err := delegation.ParseChallengeRequest(body)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_query")
			return
		}
		challenge, err := s.ServiceAuth.Issue(requestSource(r.RemoteAddr), request)
		if err != nil {
			status, name := delegation.AuthErrorStatus(err)
			aepWriteStatus(w, status, name)
			return
		}
		response, err := canon.Encode(challenge)
		if err != nil {
			writeRetry(w, http.StatusServiceUnavailable, "unavailable", apierr.DefaultRetryAfterSeconds)
			return
		}
		writeCanonical(w, http.StatusOK, response, "no-store")
	})
}

func achJSONContentType(r *http.Request) bool {
	return strings.TrimSpace(strings.SplitN(r.Header.Get("Content-Type"), ";", 2)[0]) == "application/json"
}
