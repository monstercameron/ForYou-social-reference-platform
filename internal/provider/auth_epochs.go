package provider

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"

	"foryou.local/foryou/internal/apierr"
	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/delegation"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

type aepStateRequest struct {
	AuthorID  *string `json:"author_id"`
	Challenge *string `json:"challenge"`
}

func init() {
	routeGroups = append(routeGroups, aepRegister)
}

func aepRegister(s *Service, mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/authorization-epochs", func(w http.ResponseWriter, r *http.Request) {
		body, authorID, ok := aepAuthenticatedBody(s, w, r)
		if !ok {
			return
		}
		var request wire.AuthorizationEpochEnroll
		if wire.Decode(body, "AuthorizationEpochEnroll", &request) != nil {
			writeError(w, http.StatusBadRequest, "invalid_query")
			return
		}
		state, created, err := s.Authority.Enroll(authorID, request)
		if err != nil {
			aepWriteAuthorityError(w, err)
			return
		}
		response, err := canon.Encode(state)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "unavailable")
			return
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		writeCanonical(w, status, response, "no-store")
	})
	mux.HandleFunc("POST /v1/authorization-epochs/control", func(w http.ResponseWriter, r *http.Request) {
		body, authorID, ok := aepAuthenticatedBody(s, w, r)
		if !ok {
			return
		}
		var request wire.AuthorizationEpochControl
		if wire.Decode(body, "AuthorizationEpochControl", &request) != nil {
			writeError(w, http.StatusBadRequest, "invalid_query")
			return
		}
		state, err := s.Authority.Control(authorID, request)
		if err != nil {
			aepWriteAuthorityError(w, err)
			return
		}
		response, err := canon.Encode(state)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "unavailable")
			return
		}
		writeCanonical(w, http.StatusOK, response, "no-store")
	})
	mux.HandleFunc("POST /v1/authorization-epochs/state", func(w http.ResponseWriter, r *http.Request) {
		body, authorID, ok := aepAuthenticatedBody(s, w, r)
		if !ok {
			return
		}
		request, err := aepDecodeStateRequest(body)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_query")
			return
		}
		if *request.AuthorID != authorID {
			writeError(w, http.StatusUnauthorized, "unauthenticated")
			return
		}
		response, err := s.Authority.State(authorID, *request.Challenge)
		if err != nil {
			aepWriteAuthorityError(w, err)
			return
		}
		encoded, err := canon.Encode(response)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "unavailable")
			return
		}
		writeCanonical(w, http.StatusOK, encoded, "no-store")
	})
}

func aepAuthenticatedBody(s *Service, w http.ResponseWriter, r *http.Request) ([]byte, string, bool) {
	if !achJSONContentType(r) {
		writeError(w, http.StatusBadRequest, "invalid_query")
		return nil, "", false
	}
	var raw json.RawMessage
	body, err := decodeJSON(r, 1<<20, &raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_query")
		return nil, "", false
	}
	authorID, err := s.ServiceAuth.Verify(delegation.NewAuthRequest(r.Method, r.URL.EscapedPath(), r.URL.RawQuery, r.Header.Get, delegation.BodyJSON, body))
	if err != nil {
		status, name := delegation.AuthErrorStatus(err)
		aepWriteStatus(w, status, name)
		return nil, "", false
	}
	return body, authorID, true
}

func aepDecodeStateRequest(body []byte) (aepStateRequest, error) {
	var request aepStateRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return aepStateRequest{}, err
	}
	if request.AuthorID == nil || request.Challenge == nil {
		return aepStateRequest{}, errors.New("missing required state request member")
	}
	if _, err := keys.DecodeB64URL(*request.Challenge, 32); err != nil {
		return aepStateRequest{}, err
	}
	return request, nil
}

func aepWriteAuthorityError(w http.ResponseWriter, err error) {
	status, name := delegation.ErrorStatus(err)
	aepWriteStatus(w, status, name)
}

func aepWriteStatus(w http.ResponseWriter, status int, name string) {
	if status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable {
		writeRetry(w, status, name, apierr.DefaultRetryAfterSeconds)
		return
	}
	writeError(w, status, name)
}
