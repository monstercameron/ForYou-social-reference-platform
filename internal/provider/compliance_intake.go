package provider

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"time"

	"foryou.local/foryou/internal/apierr"
	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/store"
	"foryou.local/foryou/internal/wire"
)

const intakeMaxRequestBytes = 262144

var intakeRequests = store.Collection[intakeStored]{Name: "provider.intake.requests"}

// intakeExpiredErr uses not_found for the guide's required 410 because PublicReadError has no intake-specific expiry name.
var intakeExpiredErr = errors.New("intake acceptance window expired")
var intakeInvalidWindowErr = errors.New("intake acceptance window exceeds bound")
var intakeConflictErr = errors.New("intake request conflict")
var intakeInvalidFormErr = errors.New("intake form does not accept request")

type intakeStored struct {
	RequestBytes []byte             `json:"request_bytes"`
	Route        string             `json:"route"`
	ReceivedAt   string             `json:"received_at"`
	Receipt      wire.IntakeReceipt `json:"receipt"`
}

type intakeFormRecord struct {
	intakeForm   wire.IntakeForm
	intakeBody   []byte
	intakeDigest string
}

func init() {
	routeGroups = append(routeGroups, intakeRegister)
}

func intakeRegister(service *Service, mux *http.ServeMux) {
	forms := make(map[string]intakeFormRecord, 3)
	for _, definition := range []struct {
		id           string
		kind         string
		instructions string
	}{
		{id: "notice", kind: "notice", instructions: "Submit a bounded notice for operator review."},
		{id: "rights", kind: "rights_request", instructions: "Submit a bounded data-rights request."},
		{id: "appeal", kind: "appeal", instructions: "Submit a bounded appeal for operator review."},
	} {
		form := wire.IntakeForm{
			OperatorID:   service.ProviderID(),
			FormID:       definition.id,
			FormVersion:  "1",
			Kind:         definition.kind,
			Fields:       []wire.IntakeFormField{},
			Instructions: definition.instructions,
		}
		body, err := wire.Encode("IntakeForm", form)
		if err != nil {
			panic(err)
		}
		digest := sha256.Sum256(body)
		forms[definition.id+"/1"] = intakeFormRecord{intakeForm: form, intakeBody: body, intakeDigest: hex.EncodeToString(digest[:])}
	}
	mux.HandleFunc("POST /v1/notices", func(w http.ResponseWriter, r *http.Request) {
		service.intakeSubmitRoute(forms, "notice", w, r)
	})
	mux.HandleFunc("POST /v1/rights-requests", func(w http.ResponseWriter, r *http.Request) {
		service.intakeSubmitRoute(forms, "rights_request", w, r)
	})
	mux.HandleFunc("POST /v1/appeals", func(w http.ResponseWriter, r *http.Request) {
		service.intakeSubmitRoute(forms, "appeal", w, r)
	})
	mux.HandleFunc("GET /v1/intake/{request_id}", service.intakeRecoveryRoute)
	mux.HandleFunc("GET /v1/intake-forms/{form_id}/{form_version}", func(w http.ResponseWriter, r *http.Request) {
		service.intakeFormRoute(forms, w, r)
	})
}

func (s *Service) intakeFormRoute(forms map[string]intakeFormRecord, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	key := r.PathValue("form_id") + "/" + r.PathValue("form_version")
	form, ok := forms[key]
	if !ok {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	writeCanonical(w, http.StatusOK, form.intakeBody, "no-store")
}

func (s *Service) intakeSubmitRoute(forms map[string]intakeFormRecord, kind string, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.LimitRead(w, r, ReadListing) {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, intakeMaxRequestBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_query")
		return
	}
	if len(body) > intakeMaxRequestBytes {
		writeError(w, http.StatusBadRequest, "invalid_query")
		return
	}
	limits := canon.Limits{MaxBytes: intakeMaxRequestBytes, MaxDepth: 8}
	if _, err := canon.Parse(body, limits); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_query")
		return
	}
	canonical, err := canon.Canonicalize(body, limits)
	if err != nil || !bytes.Equal(canonical, body) {
		writeError(w, http.StatusBadRequest, "invalid_query")
		return
	}
	var request wire.IntakeRequest
	if err := wire.DecodeLimits(body, limits, "IntakeRequest", &request); err != nil || !intakeByteBounds(request) {
		writeError(w, http.StatusBadRequest, "invalid_query")
		return
	}
	if request.OperatorID != s.ProviderID() {
		writeError(w, http.StatusBadRequest, "invalid_query")
		return
	}
	recoveryKey, err := keys.DecodeB64URL(r.Header.Get("ForYou-Intake-Key"), 32)
	if err != nil || !intakeRequestIDMatches(request.RequestID, request.OperatorID, recoveryKey) {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	requestDigest := sha256.Sum256(body)
	var receipt wire.IntakeReceipt
	var outcome int
	err = s.Store().Update(func(tx store.Tx) error {
		existing, found, err := intakeRequests.Get(tx, request.RequestID)
		if err != nil {
			return err
		}
		if found {
			if existing.Route != r.URL.Path || !bytes.Equal(existing.RequestBytes, body) {
				return intakeConflictErr
			}
			receipt = existing.Receipt
			outcome = http.StatusOK
			return nil
		}
		if request.Kind != kind {
			return intakeInvalidFormErr
		}
		form, ok := forms[request.FormID+"/"+request.FormVersion]
		if !ok || form.intakeForm.Kind != kind || request.FormDigest != form.intakeDigest || request.FormID != form.intakeForm.FormID || request.FormVersion != form.intakeForm.FormVersion || !intakeRegionalFieldsAllowed(request.RegionalFields, form.intakeForm) {
			return intakeInvalidFormErr
		}
		acceptUntil, err := time.Parse("2006-01-02T15:04:05Z", request.AcceptUntil)
		if err != nil {
			return intakeInvalidFormErr
		}
		if err := s.CheckClock(); err != nil {
			return err
		}
		now := s.Now()
		if !now.Before(acceptUntil) {
			return intakeExpiredErr
		}
		if acceptUntil.After(now.Add(600 * time.Second)) {
			return intakeInvalidWindowErr
		}
		receivedAt := wire.FormatTime(now)
		unsigned := struct {
			OperatorID    string `json:"operator_id"`
			RequestID     string `json:"request_id"`
			RequestDigest string `json:"request_digest"`
			ReceivedAt    string `json:"received_at"`
			Status        string `json:"status"`
		}{s.ProviderID(), request.RequestID, hex.EncodeToString(requestDigest[:]), receivedAt, "received"}
		signature, err := s.Sign("intake-receipt", unsigned)
		if err != nil {
			return err
		}
		receipt = wire.IntakeReceipt{
			OperatorID: unsigned.OperatorID, RequestID: unsigned.RequestID, RequestDigest: unsigned.RequestDigest,
			ReceivedAt: unsigned.ReceivedAt, Status: unsigned.Status, Signature: signature,
		}
		if err := intakeRequests.Put(tx, request.RequestID, intakeStored{RequestBytes: append([]byte(nil), body...), Route: r.URL.Path, ReceivedAt: receivedAt, Receipt: receipt}); err != nil {
			return err
		}
		outcome = http.StatusCreated
		return nil
	})
	if err != nil {
		switch {
		case errors.Is(err, intakeExpiredErr):
			intakeWritePublicError(w, http.StatusGone, apierr.NotFound)
		case errors.Is(err, intakeInvalidWindowErr), errors.Is(err, intakeInvalidFormErr):
			writeError(w, http.StatusBadRequest, "invalid_query")
		case errors.Is(err, intakeConflictErr):
			writeError(w, http.StatusConflict, "request_conflict")
		default:
			writeRetry(w, http.StatusServiceUnavailable, "unavailable", apierr.DefaultRetryAfterSeconds)
		}
		return
	}
	encoded, err := wire.Encode("IntakeReceipt", receipt)
	if err != nil {
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", apierr.DefaultRetryAfterSeconds)
		return
	}
	writeCanonical(w, outcome, encoded, "no-store")
}

func (s *Service) intakeRecoveryRoute(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.LimitRead(w, r, ReadDirect) {
		return
	}
	requestID := r.PathValue("request_id")
	if _, err := keys.DecodeB64URL(requestID, 32); err != nil {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	key, err := keys.DecodeB64URL(r.Header.Get("ForYou-Intake-Key"), 32)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	var record intakeStored
	var found bool
	err = s.Store().View(func(tx store.Tx) error {
		var err error
		record, found, err = intakeRequests.Get(tx, requestID)
		return err
	})
	if err != nil {
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", apierr.DefaultRetryAfterSeconds)
		return
	}
	if !found || record.Receipt.RequestID != requestID || !intakeRequestIDMatches(requestID, record.Receipt.OperatorID, key) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	var request wire.IntakeRequest
	if err := wire.DecodeLimits(record.RequestBytes, canon.Limits{MaxBytes: intakeMaxRequestBytes, MaxDepth: 8}, "IntakeRequest", &request); err != nil {
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", apierr.DefaultRetryAfterSeconds)
		return
	}
	recovery := wire.IntakeRecovery{Request: request, Receipt: record.Receipt}
	encoded, err := wire.Encode("IntakeRecovery", recovery)
	if err != nil {
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", apierr.DefaultRetryAfterSeconds)
		return
	}
	writeCanonical(w, http.StatusOK, encoded, "no-store")
}

func intakeRequestIDMatches(requestID, operatorID string, recoveryKey []byte) bool {
	if len(recoveryKey) != 32 {
		return false
	}
	encodedKey := keys.EncodeB64URL(recoveryKey)
	canonical, err := canon.Encode(map[string]string{"operator_id": operatorID, "recovery_key": encodedKey})
	if err != nil {
		return false
	}
	input, err := keys.SigningInput("intake-id", canonical)
	if err != nil {
		return false
	}
	digest := sha256.Sum256(input)
	derived := keys.EncodeB64URL(digest[:])
	return len(requestID) == len(derived) && subtle.ConstantTimeCompare([]byte(requestID), []byte(derived)) == 1
}

func intakeByteBounds(request wire.IntakeRequest) bool {
	for _, locator := range request.Locators {
		if len(locator) > 4096 {
			return false
		}
	}
	if len(request.Statement) > 65536 || len(request.Locators) > 16 || len(request.EvidenceRefs) > 8 || len(request.RegionalFields) > 32 {
		return false
	}
	for _, evidence := range request.EvidenceRefs {
		if len(evidence.Locator) > 4096 || len(evidence.Description) > 2048 {
			return false
		}
	}
	for name, value := range request.RegionalFields {
		if len(name) > 128 || len(value) > 4096 {
			return false
		}
	}
	if request.Contact != nil {
		if len(request.Contact.Address) == 0 || len(request.Contact.Address) > 320 {
			return false
		}
		for _, character := range request.Contact.Address {
			if character < 0x21 || character > 0x7e {
				return false
			}
		}
	}
	return true
}

func intakeRegionalFieldsAllowed(values map[string]string, form wire.IntakeForm) bool {
	allowed := make(map[string]wire.IntakeFormField, len(form.Fields))
	for _, field := range form.Fields {
		if _, exists := allowed[field.Name]; exists {
			return false
		}
		allowed[field.Name] = field
	}
	for name, value := range values {
		field, ok := allowed[name]
		if !ok || int64(len(value)) > field.MaxUTF8Bytes {
			return false
		}
	}
	for name, field := range allowed {
		if field.Required {
			if _, ok := values[name]; !ok {
				return false
			}
		}
	}
	return true
}

func intakeWritePublicError(w http.ResponseWriter, status int, name apierr.Status) {
	body, err := canon.Encode(apierr.PublicReadError{Status: name})
	if err != nil {
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", apierr.DefaultRetryAfterSeconds)
		return
	}
	writeCanonical(w, status, body, "no-store")
}
