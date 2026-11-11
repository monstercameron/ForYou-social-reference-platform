package provider

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math/big"
	"net/http"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/delegation"
	"foryou.local/foryou/internal/fee"
	"foryou.local/foryou/internal/funding"
	"foryou.local/foryou/internal/wire"
)

func init() {
	routeGroups = append(routeGroups, fdRegisterFunding)
}

func fdRegisterFunding(s *Service, mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/deposits", fdHandleDeposit(s))
	mux.HandleFunc("POST /v1/deposits/observe", fdHandleObserve(s))
	mux.HandleFunc("GET /v1/balances/{network}", fdHandleBalance(s))
	mux.HandleFunc("POST /v1/withdrawals", fdHandleWithdrawal(s))
	s.Every("funding-withdrawals", time.Minute, func(ctx context.Context, _ time.Time) error {
		return fdSendPendingWithdrawals(ctx, s, "", "")
	})
}

func fdHandleDeposit(s *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.LimitRead(w, r, ReadOther) {
			return
		}
		body, authorID, ok := fdAuthenticatedBody(w, r, s, delegation.BodyJSON)
		if !ok {
			return
		}
		var req wire.DepositRequest
		if fdDecodeBody(body, &req) != nil {
			writeError(w, http.StatusBadRequest, "invalid_query")
			return
		}
		if req.ProviderID != s.ProviderID() || req.AuthorID != authorID {
			writeError(w, http.StatusUnauthorized, "unauthenticated")
			return
		}
		if original, invoice, exists := s.Ledger.InvoiceForRequest(authorID, req.RequestID); exists {
			if original != req {
				writeError(w, http.StatusConflict, "request_conflict")
				return
			}
			fdWriteJSON(w, http.StatusOK, invoice)
			return
		}
		if funding.ValidateDepositRequest(req, s.ProviderID(), authorID, s.Now()) != nil {
			writeError(w, http.StatusBadRequest, "invalid_query")
			return
		}
		schedule, err := fee.InForce(s.FeeSchedules(), req.Network, s.Now())
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "unavailable")
			return
		}
		cap, err := fee.CustodyCap(schedule)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "unavailable")
			return
		}
		invoice, created, err := s.Ledger.IssueInvoice(req, cap, funding.Signer{Key: s.Key()})
		if err != nil {
			fdWriteLedgerError(w, err)
			return
		}
		status := http.StatusCreated
		if !created {
			status = http.StatusOK
		}
		fdWriteJSON(w, status, invoice)
	}
}

func fdHandleObserve(s *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, authorID, ok := fdAuthenticatedBody(w, r, s, delegation.BodyJSON)
		if !ok {
			return
		}
		var proof wire.DepositProof
		if fdDecodeBody(body, &proof) != nil {
			writeError(w, http.StatusBadRequest, "invalid_query")
			return
		}
		if proof.ProviderID != s.ProviderID() || proof.AuthorID != authorID {
			writeError(w, http.StatusUnauthorized, "unauthenticated")
			return
		}
		if funding.ValidateDepositProof(proof, s.ProviderID(), authorID) != nil {
			writeError(w, http.StatusBadRequest, "invalid_query")
			return
		}
		credit, outcome, err := s.Ledger.Observe(r.Context(), proof, s.Chain, funding.Signer{Key: s.Key()})
		if err != nil {
			fdWriteLedgerError(w, err)
			return
		}
		switch outcome {
		case funding.Pending:
			writeCanonical(w, http.StatusAccepted, []byte(`{"status":"pending"}`), "no-store")
		case funding.Created:
			fdWriteJSON(w, http.StatusCreated, credit)
		case funding.Existing:
			fdWriteJSON(w, http.StatusOK, credit)
		default:
			writeError(w, http.StatusServiceUnavailable, "unavailable")
		}
	}
}

func fdAuthenticatedBody(w http.ResponseWriter, r *http.Request, s *Service, kind delegation.BodyKind) ([]byte, string, bool) {
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 4097))
	if err != nil || len(body) > 4096 {
		writeError(w, http.StatusBadRequest, "invalid_query")
		return nil, "", false
	}
	authorID, err := s.ServiceAuth.Verify(delegation.NewAuthRequest(r.Method, r.URL.EscapedPath(), r.URL.RawQuery, r.Header.Get, kind, body))
	if err != nil {
		fdWriteAuthError(w, err)
		return nil, "", false
	}
	return body, authorID, true
}

func fdAuthenticateEmpty(w http.ResponseWriter, r *http.Request, s *Service) (string, bool) {
	defer r.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(r.Body, 1))
	if readErr != nil || len(body) > 0 {
		writeError(w, http.StatusBadRequest, "invalid_query")
		return "", false
	}
	authorID, err := s.ServiceAuth.Verify(delegation.NewAuthRequest(r.Method, r.URL.EscapedPath(), r.URL.RawQuery, r.Header.Get, delegation.BodyNone, nil))
	if err != nil {
		fdWriteAuthError(w, err)
		return "", false
	}
	return authorID, true
}

func fdDecodeBody(body []byte, value any) error {
	r := &http.Request{Body: io.NopCloser(bytes.NewReader(body))}
	_, err := decodeJSON(r, 4096, value)
	return err
}

func fdWriteAuthError(w http.ResponseWriter, err error) {
	status, name := delegation.AuthErrorStatus(err)
	if status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable {
		writeRetry(w, status, name, 1)
		return
	}
	writeError(w, status, name)
}

func fdWriteLedgerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, funding.ErrUnavailable), errors.Is(err, funding.ErrSaturated):
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", 1)
	case errors.Is(err, funding.ErrInvoiceLimit):
		writeRetry(w, http.StatusTooManyRequests, "rate_limited", 60)
	case errors.Is(err, funding.ErrCustodyCap), errors.Is(err, funding.ErrRequestConflict), errors.Is(err, funding.ErrClaim), errors.Is(err, funding.ErrConflict):
		writeError(w, http.StatusConflict, "request_conflict")
	case errors.Is(err, funding.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found")
	case errors.Is(err, funding.ErrInvalidHint), errors.Is(err, funding.ErrInvalidRequest), errors.Is(err, funding.ErrBinding), errors.Is(err, funding.ErrWindow):
		writeError(w, http.StatusBadRequest, "invalid_query")
	case errors.Is(err, funding.ErrInsufficientBalance):
		writeError(w, http.StatusPaymentRequired, "insufficient_balance")
	default:
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", 1)
	}
}

func fdWriteJSON(w http.ResponseWriter, status int, value any) {
	body, err := canon.Encode(value)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable")
		return
	}
	writeCanonical(w, status, body, "no-store")
}

func fdAmount(value *big.Int) string {
	if value == nil {
		return "0"
	}
	return value.String()
}
