package provider

import (
	"crypto/sha256"
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
		writeError(w, http.StatusConflict, "id_collision")
		return
	}
	if errors.Is(err, ErrRemoved) {
		finalizeWriteRestriction(s, w, submitted.MessageID)
		return
	}
	if err != nil {
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
