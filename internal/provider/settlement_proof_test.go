package provider

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestSettlementProofRestrictions(t *testing.T) {
	t.Run("concealed record answers like an unknown receipt", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 11, 59, 0, 0, time.UTC)
		s, _, receipts := settleRouteClaims(t, &now)
		defer s.Close()
		controls := &rdControls{restrictions: map[string]Restriction{}}
		s.controlsState = controls
		controls.rdSet(receipts[0].MessageID, Concealed)
		unknown := settleRouteRequest(s, http.MethodGet, "/v1/settlements/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", nil)
		concealed := settleRouteRequest(s, http.MethodGet, "/v1/settlements/"+receipts[0].ReceiptID, nil)
		settleAssertError(t, concealed, http.StatusNotFound, "not_found")
		if concealed.Body.String() != unknown.Body.String() {
			t.Fatalf("concealed body = %s; want the unknown receipt body %s", concealed.Body.String(), unknown.Body.String())
		}
	})
	t.Run("policy restriction answers 403 policy_restricted", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 11, 59, 0, 0, time.UTC)
		s, _, receipts := settleRouteClaims(t, &now)
		defer s.Close()
		controls := &rdControls{restrictions: map[string]Restriction{}}
		s.controlsState = controls
		controls.rdSet(receipts[0].MessageID, PolicyRestricted)
		response := settleRouteRequest(s, http.MethodGet, "/v1/settlements/"+receipts[0].ReceiptID, nil)
		settleAssertError(t, response, http.StatusForbidden, "policy_restricted")
	})
	t.Run("failed control state answers 503", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 11, 59, 0, 0, time.UTC)
		s, _, receipts := settleRouteClaims(t, &now)
		defer s.Close()
		controls := &rdControls{restrictions: map[string]Restriction{}}
		s.controlsState = controls
		controls.rdFail(errors.New("control state unreadable"))
		response := settleRouteRequest(s, http.MethodGet, "/v1/settlements/"+receipts[0].ReceiptID, nil)
		settleAssertError(t, response, http.StatusServiceUnavailable, "unavailable")
	})
}
