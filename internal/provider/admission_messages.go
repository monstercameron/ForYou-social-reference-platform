package provider

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sync"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/content"
	"foryou.local/foryou/internal/delegation"
	"foryou.local/foryou/internal/fee"
	"foryou.local/foryou/internal/message"
	"foryou.local/foryou/internal/store"
	"foryou.local/foryou/internal/wire"
)

const (
	finalizeMessageBodyLimit = 1048576
	finalizeWindow           = 30 * 24 * time.Hour
	finalizeRetryAfter       = 5
	finalizeRemedyInterval   = time.Hour
)

func init() {
	routeGroups = append(routeGroups, registerFinalizeMessages)
}

func registerFinalizeMessages(s *Service, mux *http.ServeMux) {
	var finalizeMu sync.Mutex
	mux.HandleFunc("POST /v1/messages", func(w http.ResponseWriter, r *http.Request) {
		finalizeMu.Lock()
		defer finalizeMu.Unlock()
		handleFinalizeMessage(s, w, r)
	})
	s.Every("finalize-fee-remedies", finalizeRemedyInterval, func(_ context.Context, now time.Time) error {
		finalizeMu.Lock()
		defer finalizeMu.Unlock()
		return finalizeRestoreFees(s, now)
	})
}

// finalizeRestoreFees restores the fee of each provider-caused failure once its preparation
// window has passed. It runs under the finalization lock, so an acceptance and its remedy
// never interleave. A failure that a later retry overcame was accepted and is never refunded.
func finalizeRestoreFees(s *Service, now time.Time) error {
	var errs []error
	for _, failure := range s.Ledger.ProviderFailures() {
		if now.Before(failure.Issued.Add(finalizeWindow)) {
			continue
		}
		accepted, err := finalizeAccepted(s, failure.MessageID, failure.ReceiptID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if accepted {
			continue
		}
		_, _, claimed, err := s.Settlement().Locate(failure.ReceiptID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if claimed {
			log.Printf("provider: receipt %s was claimed before its finalization failure was recorded; its fee is not restored", failure.ReceiptID)
			continue
		}
		if _, err := s.Ledger.RestoreFee(failure.AuthorID, failure.MessageID, func() (bool, error) {
			return finalizeAccepted(s, failure.MessageID, failure.ReceiptID)
		}); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// finalizeAccepted reports whether the stored acceptance of the message belongs to the
// admission that issued receiptID. A message ID held by another intent or receipt does not
// accept this admission, so its fee stays restorable.
func finalizeAccepted(s *Service, messageID, receiptID string) (bool, error) {
	var meta recordMeta
	var found bool
	err := s.Records().content.View(func(tx store.Tx) error {
		b, ok, err := tx.Get("provider.records", messageID)
		if err != nil || !ok {
			return err
		}
		found = true
		return json.Unmarshal(b, &meta)
	})
	if err != nil {
		return false, err
	}
	return found && meta.ReceiptID == receiptID, nil
}

// finalizeMarkProviderFailed records a provider-caused failure of the admission that the
// submission names before the reply, so its receipt leaves settlement claims at once.
// A submission whose receipt is not the prepared one names no admission to mark, and a
// receipt already in a published claim cannot leave it. The settlement lock keeps the
// publisher from claiming the receipt between the check and the mark.
func finalizeMarkProviderFailed(s *Service, submitted wire.PostMessage) {
	prepared, ok := s.Ledger.Admission(submitted.AuthorID, submitted.MessageID)
	if !ok || prepared.Receipt != submitted.SubmissionReceipt {
		return
	}
	settlementClaimsMu.Lock()
	defer settlementClaimsMu.Unlock()
	_, _, claimed, err := s.Settlement().Locate(submitted.SubmissionReceipt.ReceiptID)
	if err != nil {
		log.Printf("provider: provider-caused finalization failure not recorded, claim state unreadable message_id=%s: %v", submitted.MessageID, err)
		return
	}
	if claimed {
		log.Printf("provider: provider-caused finalization failure not recorded, receipt already claimed message_id=%s", submitted.MessageID)
		return
	}
	if err := s.Ledger.MarkProviderFailed(submitted.AuthorID, submitted.MessageID); err != nil {
		log.Printf("provider: provider-caused finalization failure could not be recorded message_id=%s: %v", submitted.MessageID, err)
	}
}

// handleFinalizeMessage accepts a message only at the provider that prepared its admission;
// this reference service does not relay admissions from other providers.
func handleFinalizeMessage(s *Service, w http.ResponseWriter, r *http.Request) {
	var submitted wire.PostMessage
	_, err := decodeJSON(r, finalizeMessageBodyLimit, &submitted)
	if err != nil {
		if errors.Is(err, errBodyTooLarge) {
			writeError(w, http.StatusUnprocessableEntity, "message_too_large")
			return
		}
		writeError(w, http.StatusUnprocessableEntity, "invalid_message")
		return
	}
	now := s.Now()
	if err = message.Verify(submitted, message.VerifyOptions{
		Now: now,
		Delegation: func(d wire.Delegation, i wire.Intent, providerID string, at time.Time) error {
			return delegation.CheckIntentDelegation(d, i, providerID, at)
		},
	}); err != nil {
		if errors.Is(err, message.ErrIDMismatch) {
			writeError(w, http.StatusUnprocessableEntity, "id_mismatch")
		} else if errors.Is(err, message.ErrMessageTooLarge) {
			writeError(w, http.StatusUnprocessableEntity, "message_too_large")
		} else {
			writeError(w, http.StatusUnprocessableEntity, "invalid_message")
		}
		return
	}
	if err = content.ValidatePostContent(submitted.Payload); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_message")
		return
	}
	if err = fee.VerifyReceipt(submitted.SubmissionReceipt); err != nil || submitted.SubmissionReceipt.ProviderID != s.ProviderID() {
		writeError(w, http.StatusUnprocessableEntity, "invalid_message")
		return
	}
	intent := message.IntentOf(submitted)
	intentBytes, err := canon.Encode(intent)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_message")
		return
	}
	completeBytes, err := canon.Encode(submitted)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_message")
		return
	}
	issuedAt, err := wire.ParseTime(submitted.SubmissionReceipt.IssuedAt)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_message")
		return
	}
	acceptance := Acceptance{
		MessageID:    submitted.MessageID,
		Bytes:        completeBytes,
		IntentDigest: sha256.Sum256(intentBytes),
		ReceiptID:    submitted.SubmissionReceipt.ReceiptID,
		IssuedAt:     issuedAt,
	}
	_, state, err := s.Records().Lookup(submitted.MessageID)
	if err != nil && state == RecordUnknown {
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", finalizeRetryAfter)
		return
	}
	if state != RecordUnknown {
		accepted, acceptErr := s.Records().Accept(acceptance)
		if errors.Is(acceptErr, ErrIDCollision) {
			finalizeRecordCollision(s, submitted.MessageID)
			finalizeMarkProviderFailed(s, submitted)
			writeError(w, http.StatusConflict, "id_collision")
			return
		}
		if errors.Is(acceptErr, ErrRemoved) {
			finalizeWriteRestriction(s, w, submitted.MessageID)
			return
		}
		if acceptErr != nil {
			writeRetry(w, http.StatusServiceUnavailable, "unavailable", finalizeRetryAfter)
			return
		}
		if !accepted.Created {
			prepared, found := s.Ledger.Admission(submitted.AuthorID, submitted.MessageID)
			if found && submitted.SubmissionReceipt != prepared.Receipt {
				if err := finalizeKeepReceiptEvidence(s.Store(), submitted.MessageID, submitted.SubmissionReceipt); err != nil {
					writeRetry(w, http.StatusServiceUnavailable, "unavailable", finalizeRetryAfter)
					return
				}
			}
			finalizeWriteAcceptance(w, http.StatusOK, submitted.MessageID)
			return
		}
	}
	prepared, ok := s.Ledger.Admission(submitted.AuthorID, submitted.MessageID)
	if !ok || !finalizeSameIntent(prepared.Request.Intent, intent) || prepared.Receipt != submitted.SubmissionReceipt {
		writeError(w, http.StatusUnprocessableEntity, "invalid_message")
		return
	}
	if err = s.CheckClock(); err != nil {
		finalizeMarkProviderFailed(s, submitted)
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", finalizeRetryAfter)
		return
	}
	// The specification requires refusal at the deadline but does not name an error; this route uses 409 stale_state.
	if !s.Now().Before(issuedAt.Add(finalizeWindow)) {
		writeError(w, http.StatusConflict, "stale_state")
		return
	}
	accepted, err := s.Records().Accept(acceptance)
	if errors.Is(err, ErrIDCollision) {
		finalizeRecordCollision(s, submitted.MessageID)
		finalizeMarkProviderFailed(s, submitted)
		writeError(w, http.StatusConflict, "id_collision")
		return
	}
	if errors.Is(err, ErrRemoved) {
		finalizeWriteRestriction(s, w, submitted.MessageID)
		return
	}
	if err != nil {
		finalizeMarkProviderFailed(s, submitted)
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", finalizeRetryAfter)
		return
	}
	status := http.StatusCreated
	if !accepted.Created {
		status = http.StatusOK
	}
	finalizeWriteAcceptance(w, status, submitted.MessageID)
}

func finalizeSameIntent(left, right wire.Intent) bool {
	leftBytes, leftErr := canon.Encode(left)
	rightBytes, rightErr := canon.Encode(right)
	return leftErr == nil && rightErr == nil && string(leftBytes) == string(rightBytes)
}

func finalizeKeepReceiptEvidence(s store.Store, messageID string, receipt wire.SubmissionReceipt) error {
	b, err := canon.Encode(receipt)
	if err != nil {
		return err
	}
	return s.Update(func(tx store.Tx) error {
		return tx.Put("provider.messages.acceptance_evidence", messageID+"\n"+receipt.ReceiptID, b)
	})
}

func finalizeRecordCollision(s *Service, messageID string) {
	incident := struct {
		MessageID string `json:"message_id"`
		Status    string `json:"status"`
		Detected  string `json:"detected_at"`
	}{MessageID: messageID, Status: "id_collision", Detected: wire.FormatTime(s.Now())}
	b, err := canon.Encode(incident)
	if err == nil {
		err = s.Store().Update(func(tx store.Tx) error {
			return tx.Put("provider.messages.restricted_incidents", messageID, b)
		})
	}
	if err != nil {
		log.Printf("provider: restricted message ID collision incident could not be stored message_id=%s", messageID)
	}
}

func finalizeWriteAcceptance(w http.ResponseWriter, status int, messageID string) {
	b, err := canon.Encode(wire.MessageAcceptance{MessageID: messageID, Status: "accepted"})
	if err != nil {
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", finalizeRetryAfter)
		return
	}
	writeCanonical(w, status, b, "no-store")
}

func finalizeWriteRestriction(s *Service, w http.ResponseWriter, messageID string) {
	restriction, err := s.Restriction(messageID)
	if err != nil {
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", finalizeRetryAfter)
		return
	}
	switch restriction {
	case PolicyRestricted:
		writeError(w, http.StatusForbidden, "policy_restricted")
	case LegalRestricted:
		writeError(w, http.StatusForbidden, "legal_restricted")
	case Restricted:
		writeError(w, http.StatusForbidden, "restricted")
	default:
		writeError(w, http.StatusNotFound, "not_found")
	}
}
