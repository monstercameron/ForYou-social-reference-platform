package provider

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"foryou.local/foryou/internal/apierr"
	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/content"
	"foryou.local/foryou/internal/delegation"
	"foryou.local/foryou/internal/fee"
	"foryou.local/foryou/internal/funding"
	"foryou.local/foryou/internal/message"
	"foryou.local/foryou/internal/wire"
)

type admissionCapacityFailure struct {
	seconds int
}

func (e admissionCapacityFailure) Error() string { return "provider: admission capacity exhausted" }

// admissionCapacity reports whether the provider can retain a new admission.
// The core currently has no retention-capacity signal; this seam defaults open.
var admissionCapacity = func(_ *Service, _ int) (retryAfterSeconds int, ok bool) { return 0, true }

func init() {
	routeGroups = append(routeGroups, admissionRegisterRoutes)
}

func admissionRegisterRoutes(s *Service, mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/fee-quotes", s.admissionFeeQuote)
	mux.HandleFunc("POST /v1/admissions", s.admissionAdmit)
	mux.HandleFunc("GET /v1/admissions/{message_id}", s.admissionLookup)
	s.Every("admission quote eviction", time.Minute, s.admissionEvictQuotes)
}

func (s *Service) admissionAdmit(w http.ResponseWriter, r *http.Request) {
	var req wire.AdmissionRequest
	body, err := decodeJSON(r, admissionJSONLimit, &req)
	if err != nil {
		admissionWriteDecodeError(w, err)
		return
	}
	if value, err := canon.Parse(body, canon.MessageLimits); err != nil || wire.Check("AdmissionRequest", value) != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_message")
		return
	}
	if req.ProviderID != s.ProviderID() {
		writeError(w, http.StatusUnprocessableEntity, "bad_authorization")
		return
	}
	if err := admissionValidateIntent(req.Intent); err != nil {
		admissionWriteIntentError(w, err)
		return
	}
	if err := content.ValidatePostContent(req.Intent.Payload); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_message")
		return
	}
	id, err := message.ID(req.Intent)
	if err != nil {
		admissionWriteIntentError(w, err)
		return
	}
	verify := func(signer string) error {
		return fee.VerifyAdmission(fee.AdmissionAuthorization(s.ProviderID(), req.Intent.AuthorID, id, req.QuoteID), signer, req.Authorization)
	}
	if err := s.admissionVerifyIntentSigner(req.Intent, verify, time.Time{}, false); err != nil {
		admissionWriteDelegationError(w, err)
		return
	}
	if prior, ok := s.Ledger.Admission(req.Intent.AuthorID, id); ok {
		body, err := canon.Encode(prior.Receipt)
		if err != nil {
			writeRetry(w, http.StatusServiceUnavailable, "unavailable", apierr.DefaultRetryAfterSeconds)
			return
		}
		writeCanonical(w, http.StatusOK, body, "no-store")
		return
	}
	issued, found, err := s.admissionLoadQuote(req.QuoteID)
	if err != nil {
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", apierr.DefaultRetryAfterSeconds)
		return
	}
	if !found {
		writeError(w, http.StatusConflict, "quote_conflict")
		return
	}
	if issued.AuthorID != req.Intent.AuthorID || issued.MessageID != id {
		writeError(w, http.StatusConflict, "quote_conflict")
		return
	}
	if err := s.CheckClock(); err != nil {
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", apierr.DefaultRetryAfterSeconds)
		return
	}
	quote := issued.Quote
	expires, err := wire.ParseTime(quote.ExpiresAt)
	if err != nil {
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", apierr.DefaultRetryAfterSeconds)
		return
	}
	now := s.Now()
	if !now.Before(expires) {
		writeError(w, http.StatusUnprocessableEntity, "quote_expired")
		return
	}
	if err := message.CheckTimestamp(req.Intent, now); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_message")
		return
	}
	if err := s.admissionCheckTarget(req.Intent, id); err != nil {
		admissionWriteTargetError(w, err)
		return
	}
	intentBytes, err := canon.Encode(req.Intent)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_message")
		return
	}
	draft := funding.AdmissionDraft{Request: req, AuthorID: req.Intent.AuthorID, MessageID: id, Quote: quote}
	check := func(at time.Time) error {
		if !at.Before(expires) {
			return admissionQuoteExpired
		}
		if retry, ok := admissionCapacity(s, len(intentBytes)); !ok {
			return admissionCapacityFailure{seconds: retry}
		}
		return nil
	}
	seal := func(receipt wire.SubmissionReceipt) (wire.SubmissionReceipt, error) {
		signed, err := fee.SignReceipt(receipt, s.Key())
		if err != nil {
			return wire.SubmissionReceipt{}, err
		}
		if err := message.CheckBudget(req.Intent, signed); err != nil {
			return wire.SubmissionReceipt{}, err
		}
		return signed, nil
	}
	var record wire.PreparedAdmissionRecord
	var created bool
	var callbackErr error
	commit := func(at time.Time) error {
		draft.At = at
		record, created, callbackErr = s.Ledger.Admit(r.Context(), draft, check, seal)
		return callbackErr
	}
	if req.Intent.Delegation != nil {
		use := delegation.Use{AuthorID: req.Intent.AuthorID, SignerID: req.Intent.SignerID, MessageType: "post", ProviderID: s.ProviderID()}
		err = s.Authority.Admit(*req.Intent.Delegation, use, commit)
	} else {
		err = commit(time.Time{})
	}
	if err != nil {
		if callbackErr != nil {
			admissionWriteCommitError(w, callbackErr)
		} else {
			admissionWriteDelegationError(w, err)
		}
		return
	}
	body, err = canon.Encode(record.Receipt)
	if err != nil {
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", apierr.DefaultRetryAfterSeconds)
		return
	}
	status := http.StatusCreated
	if !created {
		status = http.StatusOK
	}
	writeCanonical(w, status, body, "no-store")
}

var admissionQuoteExpired = errors.New("provider: quote expired at authorization time")

func admissionWriteCommitError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, admissionQuoteExpired):
		writeError(w, http.StatusUnprocessableEntity, "quote_expired")
	case errors.Is(err, funding.ErrInsufficientBalance):
		writeError(w, http.StatusPaymentRequired, "insufficient_balance")
	case errors.Is(err, funding.ErrQuoteConflict):
		writeError(w, http.StatusConflict, "quote_conflict")
	case errors.Is(err, funding.ErrUnavailable), errors.Is(err, ErrClockUncertain):
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", apierr.DefaultRetryAfterSeconds)
	default:
		var capacity admissionCapacityFailure
		if errors.As(err, &capacity) {
			writeRetry(w, http.StatusServiceUnavailable, "unavailable", capacity.seconds)
			return
		}
		if errors.Is(err, message.ErrMessageTooLarge) {
			writeError(w, http.StatusUnprocessableEntity, "message_too_large")
			return
		}
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", apierr.DefaultRetryAfterSeconds)
	}
}

func (s *Service) admissionCheckTarget(intent wire.Intent, id string) error {
	if intent.Payload.Supersedes == "" {
		return message.CheckAdmissionEdge(intent, id, nil)
	}
	bytes, state, err := s.Records().Lookup(intent.Payload.Supersedes)
	if err != nil {
		return admissionTargetStoreError{err: err}
	}
	if state != RecordRetained || len(bytes) == 0 {
		return message.CheckAdmissionEdge(intent, id, nil)
	}
	var target wire.PostMessage
	if err := json.Unmarshal(bytes, &target); err != nil {
		return admissionTargetStoreError{err: err}
	}
	return message.CheckAdmissionEdge(intent, id, &target)
}

type admissionTargetStoreError struct{ err error }

func (e admissionTargetStoreError) Error() string {
	return "provider: target lookup failed: " + e.err.Error()
}
func (e admissionTargetStoreError) Unwrap() error { return e.err }

func admissionWriteTargetError(w http.ResponseWriter, err error) {
	var storeErr admissionTargetStoreError
	if errors.As(err, &storeErr) {
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", apierr.DefaultRetryAfterSeconds)
		return
	}
	writeError(w, http.StatusUnprocessableEntity, "invalid_message")
}

// admissionVerifyIntentSigner runs verify with the intent's effective signer after checking the
// delegation structure; with currentDelegation it also requires the delegation to be usable at
// at, while admission leaves delegation time and revocation to its gate.
func (s *Service) admissionVerifyIntentSigner(intent wire.Intent, verify func(signer string) error, at time.Time, currentDelegation bool) error {
	signer := intent.AuthorID
	if intent.Delegation != nil {
		signer = intent.SignerID
		if signer == "" || intent.Delegation.AuthorID != intent.AuthorID || intent.Delegation.SignerID != signer || intent.Delegation.ProviderID != s.ProviderID() {
			return delegation.ErrInvalidDelegation
		}
		delegationBytes, err := canon.Encode(intent.Delegation)
		if err != nil {
			return delegation.ErrInvalidDelegation
		}
		delegationValue, err := canon.Parse(delegationBytes, canon.MessageLimits)
		if err != nil || wire.Check("Delegation", delegationValue) != nil {
			return delegation.ErrInvalidDelegation
		}
	} else if intent.SignerID != "" {
		return delegation.ErrInvalidDelegation
	}
	if verify(signer) != nil {
		return errors.New("provider: bad author signature")
	}
	if intent.Delegation == nil || !currentDelegation {
		return nil
	}
	use := delegation.Use{AuthorID: intent.AuthorID, SignerID: signer, MessageType: "post", ProviderID: s.ProviderID(), At: at}
	// Authority.Admit is used as a read-only current-authorization check with a no-op commit.
	return s.Authority.Admit(*intent.Delegation, use, func(time.Time) error { return nil })
}

func (s *Service) admissionLookup(w http.ResponseWriter, r *http.Request) {
	authorID, err := s.ServiceAuth.Verify(delegation.NewAuthRequest(r.Method, r.URL.EscapedPath(), r.URL.RawQuery, r.Header.Get, delegation.BodyNone, nil))
	if err != nil {
		status, name := delegation.AuthErrorStatus(err)
		if status == http.StatusServiceUnavailable {
			writeRetry(w, status, name, apierr.DefaultRetryAfterSeconds)
		} else {
			writeError(w, status, name)
		}
		return
	}
	messageID := r.PathValue("message_id")
	if !admissionCanonicalMessageID(messageID) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	record, ok := s.Ledger.Admission(authorID, messageID)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	body, err := canon.Encode(record.Receipt)
	if err != nil {
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", apierr.DefaultRetryAfterSeconds)
		return
	}
	writeCanonical(w, http.StatusOK, body, "no-store")
}

func admissionCanonicalMessageID(id string) bool {
	if len(id) != len("fy1:")+64 || !strings.HasPrefix(id, "fy1:") {
		return false
	}
	decoded, err := hex.DecodeString(id[len("fy1:"):])
	return err == nil && hex.EncodeToString(decoded) == id[len("fy1:"):]
}
