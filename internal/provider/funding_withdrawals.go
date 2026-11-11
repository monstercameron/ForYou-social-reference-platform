package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/big"
	"net/http"

	"foryou.local/foryou/internal/delegation"
	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/funding"
	"foryou.local/foryou/internal/wire"
)

func fdHandleWithdrawal(s *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, authorID, ok := fdAuthenticatedBody(w, r, s, delegation.BodyJSON)
		if !ok {
			return
		}
		var req wire.WithdrawalRequest
		if fdDecodeBody(body, &req) != nil {
			writeError(w, http.StatusBadRequest, "invalid_query")
			return
		}
		if req.ProviderID != s.ProviderID() || req.AuthorID != authorID {
			writeError(w, http.StatusUnauthorized, "unauthenticated")
			return
		}
		if previous, exists := s.Ledger.Withdrawal(authorID, req.RequestID); exists {
			if previous.Request != req {
				writeError(w, http.StatusConflict, "request_conflict")
				return
			}
			if previous.Status == funding.WithdrawalPending {
				_ = fdSendWithdrawal(r.Context(), s, previous)
				previous, _ = s.Ledger.Withdrawal(authorID, req.RequestID)
			}
			_ = fdSendPendingWithdrawals(r.Context(), s, authorID, req.RequestID)
			fdWriteWithdrawal(w, http.StatusOK, s, previous)
			return
		}
		if funding.ValidateWithdrawalRequest(req, s.ProviderID(), authorID) != nil {
			writeError(w, http.StatusBadRequest, "invalid_query")
			return
		}
		withdrawal, created, err := s.Ledger.RequestWithdrawal(req)
		if err != nil {
			fdWriteLedgerError(w, err)
			return
		}
		if withdrawal.Status == funding.WithdrawalPending {
			_ = fdSendWithdrawal(r.Context(), s, withdrawal)
			withdrawal, _ = s.Ledger.Withdrawal(authorID, req.RequestID)
		}
		_ = fdSendPendingWithdrawals(r.Context(), s, authorID, req.RequestID)
		status := http.StatusAccepted
		if !created {
			status = http.StatusOK
		}
		fdWriteWithdrawal(w, status, s, withdrawal)
	}
}

func fdWriteWithdrawal(w http.ResponseWriter, status int, s *Service, withdrawal funding.Withdrawal) {
	request := withdrawal.Request
	result, err := (funding.Signer{Key: s.Key()}).SignWithdrawal(wire.WithdrawalStatus{ProviderID: s.ProviderID(), AuthorID: request.AuthorID, Network: request.Network, AmountBaseUnits: request.AmountBaseUnits, Recipient: request.Recipient, RequestID: request.RequestID, Status: withdrawal.Status, Transfer: withdrawal.Transfer, UpdatedAt: wire.FormatTime(withdrawal.UpdatedAt)})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable")
		return
	}
	fdWriteJSON(w, status, result)
}

func fdSendPendingWithdrawals(ctx context.Context, s *Service, skipAuthorID, skipRequestID string) error {
	var first error
	for _, withdrawal := range s.Ledger.PendingWithdrawals() {
		if withdrawal.Request.AuthorID == skipAuthorID && withdrawal.Request.RequestID == skipRequestID {
			continue
		}
		if err := fdSendWithdrawal(ctx, s, withdrawal); err != nil && first == nil && !errors.Is(err, faux.ErrInvalid) {
			first = err
		}
	}
	return first
}

func fdSendWithdrawal(ctx context.Context, s *Service, withdrawal funding.Withdrawal) error {
	if withdrawal.Status != funding.WithdrawalPending {
		return nil
	}
	request := withdrawal.Request
	amount, ok := new(big.Int).SetString(request.AmountBaseUnits, 10)
	if !ok || amount.Sign() <= 0 {
		return funding.ErrInvalidRequest
	}
	if request.Network == wire.BitcoinMainnet {
		amount.Quo(amount, big.NewInt(1000))
	}
	if amount.Sign() <= 0 {
		return funding.ErrInvalidRequest
	}
	digest := sha256.Sum256([]byte(s.ProviderID() + "\n" + request.AuthorID + "\n" + request.RequestID))
	from := faux.DevAddress(request.Network, s.cfg.WithdrawalLabel)
	transfer, err := s.Chain.Submit(ctx, faux.SubmitRequest{Network: request.Network, From: from, Recipient: request.Recipient, AmountChainUnits: amount.String(), ClientRef: "withdrawal:" + hex.EncodeToString(digest[:])})
	if err != nil {
		if errors.Is(err, faux.ErrInvalid) {
			_, markErr := s.Ledger.MarkWithdrawalFailed(request.AuthorID, request.RequestID)
			return errors.Join(err, markErr)
		}
		return err
	}
	paid := wire.DepositTransfer{Network: transfer.Network, TxID: transfer.TxID, TxHash: transfer.TxHash}
	if transfer.OutputIndex != nil {
		index := int64(*transfer.OutputIndex)
		paid.OutputIndex = &index
	}
	_, err = s.Ledger.MarkWithdrawalSent(request.AuthorID, request.RequestID, paid)
	return err
}
