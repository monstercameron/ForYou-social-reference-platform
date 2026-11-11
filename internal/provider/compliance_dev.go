package provider

import (
	"encoding/json"
	"errors"
	"net/http"
)

type controlDevRequest struct {
	Action    string `json:"action"`
	MessageID string `json:"message_id"`
	Status    string `json:"status"`
}

func controlRegisterDevRoute(s *Service, controls *controlState, mux *http.ServeMux) {
	mux.HandleFunc("POST /dev/controls", func(w http.ResponseWriter, r *http.Request) {
		if !isLoopback(r) {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		var request controlDevRequest
		if _, err := decodeJSON(r, 4096, &request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_query")
			return
		}
		status, err := controlValidateRequest(request.Action, request.MessageID, request.Status)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_query")
			return
		}
		if err := s.controlsState.Ready(); err != nil {
			writeError(w, http.StatusServiceUnavailable, "unavailable")
			return
		}
		sequence, err := controlApply(s, controls, request.Action, request.MessageID, status)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "unavailable")
			return
		}
		controlWriteSequence(w, sequence)
	})
}

func controlValidateRequest(action, messageID, statusText string) (Restriction, error) {
	if !controlMessageIDPattern.MatchString(messageID) {
		return Unrestricted, errors.New("provider: invalid control message ID")
	}
	status := Restriction(statusText)
	switch action {
	case "restrict":
		if !controlStatusValid(status) {
			return Unrestricted, errors.New("provider: invalid control status")
		}
	case "lift":
		if status != Unrestricted {
			return Unrestricted, errors.New("provider: invalid control status")
		}
	case "remove":
		if status == Unrestricted {
			status = Concealed
		}
		if !controlStatusValid(status) {
			return Unrestricted, errors.New("provider: invalid control status")
		}
	default:
		return Unrestricted, errors.New("provider: invalid control action")
	}
	return status, nil
}

func controlWriteSequence(w http.ResponseWriter, sequence uint64) {
	body, err := json.Marshal(struct {
		Sequence uint64 `json:"sequence"`
	}{Sequence: sequence})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
