package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"foryou.local/foryou/internal/apierr"
	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/content"
	"foryou.local/foryou/internal/delegation"
	"foryou.local/foryou/internal/fee"
	"foryou.local/foryou/internal/message"
	"foryou.local/foryou/internal/store"
	"foryou.local/foryou/internal/wire"
)

const admissionJSONLimit = 1048576

type admissionIssuedQuote struct {
	Quote     wire.FeeQuote `json:"quote"`
	AuthorID  string        `json:"author_id"`
	MessageID string        `json:"message_id"`
}

// admissionFeeQuote maps a stale requested schedule to 409 stale_state because the catalog defines no schedule-specific code.
func (s *Service) admissionFeeQuote(w http.ResponseWriter, r *http.Request) {
	var req wire.FeePreflightRequest
	body, err := decodeJSON(r, admissionJSONLimit, &req)
	if err != nil {
		admissionWriteDecodeError(w, err)
		return
	}
	// Wrapper schema errors, including authorization encoding, use invalid_message.
	if value, err := canon.Parse(body, canon.MessageLimits); err != nil || wire.Check("FeePreflightRequest", value) != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_message")
		return
	}
	if req.ProviderID != s.ProviderID() {
		writeError(w, http.StatusUnprocessableEntity, "bad_authorization")
		return
	}
	intent, err := admissionDecodeIntent(req.Intent)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_message")
		return
	}
	if err := admissionValidateIntent(intent); err != nil {
		admissionWriteIntentError(w, err)
		return
	}
	if req.AuthorID != intent.AuthorID {
		writeError(w, http.StatusUnprocessableEntity, "bad_authorization")
		return
	}
	id, err := message.ID(intent)
	if err != nil {
		admissionWriteIntentError(w, err)
		return
	}
	intentBytes, err := canon.Encode(intent)
	if err != nil {
		admissionWriteIntentError(w, err)
		return
	}
	billableBytes := len(intentBytes)
	if billableBytes > message.MaxMessageBytes {
		writeError(w, http.StatusUnprocessableEntity, "message_too_large")
		return
	}
	if err := s.CheckClock(); err != nil {
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", apierr.DefaultRetryAfterSeconds)
		return
	}
	now := s.Now()
	verify := func(signer string) error {
		return fee.VerifyPreflight(fee.PreflightAuthorization(s.ProviderID(), req.AuthorID, id, req.ScheduleID), signer, req.Authorization)
	}
	if err := s.admissionVerifyIntentSigner(intent, verify, now, true); err != nil {
		admissionWriteDelegationError(w, err)
		return
	}
	if err := content.ValidatePostContent(intent.Payload); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_message")
		return
	}
	if err := message.CheckTimestamp(intent, now); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_message")
		return
	}
	if err := s.admissionCheckTarget(intent, id); err != nil {
		admissionWriteTargetError(w, err)
		return
	}
	schedules := s.FeeSchedules()
	var network string
	for _, candidate := range schedules {
		if candidate.ScheduleID == req.ScheduleID {
			network = candidate.Network
			break
		}
	}
	if network == "" {
		writeError(w, http.StatusConflict, "stale_state")
		return
	}
	schedule, err := fee.InForce(schedules, network, now)
	if err != nil {
		writeError(w, http.StatusConflict, "stale_state")
		return
	}
	if req.ScheduleID != schedule.ScheduleID {
		writeError(w, http.StatusConflict, "stale_state")
		return
	}
	lifetime := s.Config().QuoteLifetime
	quote, err := fee.IssueQuote(fee.QuoteRequest{ProviderID: s.ProviderID(), AuthorID: req.AuthorID, MessageID: id, BillableBytes: billableBytes, PreflightAt: now, ExpiresAt: now.Add(lifetime)}, schedule, schedules, s.Key())
	if err != nil {
		writeError(w, http.StatusConflict, "stale_state")
		return
	}
	if err := s.admissionStoreQuote(admissionIssuedQuote{Quote: quote, AuthorID: req.AuthorID, MessageID: id}); err != nil {
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", apierr.DefaultRetryAfterSeconds)
		return
	}
	body, err = canon.Encode(quote)
	if err != nil {
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", apierr.DefaultRetryAfterSeconds)
		return
	}
	writeCanonical(w, http.StatusOK, body, "no-store")
}

func admissionValidateIntent(intent wire.Intent) error {
	return message.ValidateIntent(intent)
}

// admissionDecodeIntent decodes the raw preflight intent as a closed object and requires
// its canonical form to survive the decoding unchanged, so the quote binds the exact intent I.
func admissionDecodeIntent(raw json.RawMessage) (wire.Intent, error) {
	var intent wire.Intent
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&intent); err != nil {
		return wire.Intent{}, err
	}
	want, err := canon.Canonicalize(raw, canon.MessageLimits)
	if err != nil {
		return wire.Intent{}, err
	}
	got, err := canon.Encode(intent)
	if err != nil {
		return wire.Intent{}, err
	}
	if !bytes.Equal(got, want) {
		return wire.Intent{}, errors.New("provider: intent does not round-trip")
	}
	return intent, nil
}

func admissionWriteDecodeError(w http.ResponseWriter, err error) {
	if errors.Is(err, errBodyTooLarge) {
		writeError(w, http.StatusUnprocessableEntity, "message_too_large")
		return
	}
	writeError(w, http.StatusBadRequest, "invalid_query")
}

func admissionWriteIntentError(w http.ResponseWriter, err error) {
	if errors.Is(err, message.ErrMessageTooLarge) {
		writeError(w, http.StatusUnprocessableEntity, "message_too_large")
		return
	}
	writeError(w, http.StatusUnprocessableEntity, "invalid_message")
}

func admissionWriteDelegationError(w http.ResponseWriter, err error) {
	status, name := delegation.ErrorStatus(err)
	if status == 0 {
		return
	}
	if status == http.StatusServiceUnavailable {
		writeRetry(w, status, name, apierr.DefaultRetryAfterSeconds)
		return
	}
	writeError(w, status, name)
}

func (s *Service) admissionStoreQuote(issued admissionIssuedQuote) error {
	encoded, err := json.Marshal(issued)
	if err != nil {
		return err
	}
	return s.Store().Update(func(tx store.Tx) error {
		return tx.Put("provider.admission.quotes", issued.Quote.QuoteID, encoded)
	})
}

func (s *Service) admissionLoadQuote(id string) (admissionIssuedQuote, bool, error) {
	var issued admissionIssuedQuote
	var found bool
	err := s.Store().View(func(tx store.Tx) error {
		raw, ok, err := tx.Get("provider.admission.quotes", id)
		if err != nil || !ok {
			return err
		}
		if err := json.Unmarshal(raw, &issued); err != nil {
			return err
		}
		found = true
		return nil
	})
	return issued, found, err
}

func (s *Service) admissionEvictQuotes(_ context.Context, now time.Time) error {
	return s.Store().Update(func(tx store.Tx) error {
		return tx.Scan("provider.admission.quotes", store.Range{}, func(id string, raw []byte) (bool, error) {
			var issued admissionIssuedQuote
			if err := json.Unmarshal(raw, &issued); err != nil {
				return false, err
			}
			expires, err := wire.ParseTime(issued.Quote.ExpiresAt)
			if err != nil {
				return false, err
			}
			if !now.Before(expires) {
				return true, tx.Delete("provider.admission.quotes", id)
			}
			return true, nil
		})
	})
}
