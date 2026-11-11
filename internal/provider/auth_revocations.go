package provider

import (
	"net/http"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/wire"
)

func init() {
	routeGroups = append(routeGroups, arvRegister)
}

func arvRegister(s *Service, mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/revocations", func(w http.ResponseWriter, r *http.Request) {
		body, authorID, ok := aepAuthenticatedBody(s, w, r)
		if !ok {
			return
		}
		var request wire.RevocationRequest
		if wire.Decode(body, "RevocationRequest", &request) != nil {
			writeError(w, http.StatusBadRequest, "invalid_query")
			return
		}
		id, err := s.Authority.Revoke(authorID, request)
		if err != nil {
			aepWriteAuthorityError(w, err)
			return
		}
		response, err := canon.Encode(struct {
			DelegationID string `json:"delegation_id"`
			Status       string `json:"status"`
		}{DelegationID: id, Status: "revoked"})
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "unavailable")
			return
		}
		writeCanonical(w, http.StatusOK, response, "no-store")
	})
}
