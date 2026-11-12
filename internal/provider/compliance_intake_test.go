package provider

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/store"
	"foryou.local/foryou/internal/wire"
)

var intakeTestSource int

type intakeCountingReader struct {
	reader *bytes.Reader
	read   int
}

func (r *intakeCountingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.read += n
	return n, err
}

func intakeTestService(t *testing.T, now *time.Time) *Service {
	t.Helper()
	cfg, err := TestConfig(func() time.Time { return *now })
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	return service
}

func intakeTestRequest(t *testing.T, service *Service, method, target string, body []byte, recoveryKey string) *httptest.ResponseRecorder {
	t.Helper()
	intakeTestSource++
	request := httptest.NewRequest(method, target, bytes.NewReader(body))
	request.RemoteAddr = fmt.Sprintf("192.0.2.%d:1234", intakeTestSource%250+1)
	if recoveryKey != "" {
		request.Header.Set("ForYou-Intake-Key", recoveryKey)
	}
	response := httptest.NewRecorder()
	service.Handler().ServeHTTP(response, request)
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q; want no-store", got)
	}
	return response
}

func intakeTestSubmission(t *testing.T, service *Service, now time.Time, statement string) (wire.IntakeRequest, []byte, []byte) {
	t.Helper()
	recoveryBytes := []byte("0123456789abcdef0123456789abcdef")
	recoveryKey := keys.EncodeB64URL(recoveryBytes)
	idObject, err := canon.Encode(map[string]string{"operator_id": service.ProviderID(), "recovery_key": recoveryKey})
	if err != nil {
		t.Fatal(err)
	}
	signingInput, err := keys.SigningInput("intake-id", idObject)
	if err != nil {
		t.Fatal(err)
	}
	requestIDDigest := sha256.Sum256(signingInput)
	expectedForm := []byte(fmt.Sprintf(`{"fields":[],"form_id":"notice","form_version":"1","instructions":"Submit a bounded notice for operator review.","kind":"notice","operator_id":"%s"}`, service.ProviderID()))
	formDigest := sha256.Sum256(expectedForm)
	request := wire.IntakeRequest{
		OperatorID: service.ProviderID(), RequestID: keys.EncodeB64URL(requestIDDigest[:]),
		AcceptUntil: wire.FormatTime(now.Add(600 * time.Second)), Kind: "notice", FormID: "notice", FormVersion: "1",
		FormDigest: hex.EncodeToString(formDigest[:]), Locators: []string{}, Statement: statement,
		EvidenceRefs: []wire.IntakeEvidenceRef{}, RegionalFields: map[string]string{},
	}
	body, err := wire.Encode("IntakeRequest", request)
	if err != nil {
		t.Fatal(err)
	}
	return request, body, recoveryBytes
}

func intakeTestStored(t *testing.T, service *Service, requestID string) (intakeStored, bool) {
	t.Helper()
	var result intakeStored
	var found bool
	err := service.Store().View(func(tx store.Tx) error {
		var err error
		result, found, err = intakeRequests.Get(tx, requestID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return result, found
}

func intakeTestPost(t *testing.T, service *Service, body []byte, recoveryKey []byte) *httptest.ResponseRecorder {
	t.Helper()
	return intakeTestRequest(t, service, http.MethodPost, "/v1/notices", body, keys.EncodeB64URL(recoveryKey))
}

func TestIntakeSubmissionAndRetry(t *testing.T) {
	t.Run("new notice has a verifiable receipt and exact retry preserves it after expiry", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		service := intakeTestService(t, &now)
		request, body, recoveryKey := intakeTestSubmission(t, service, now, "Example notice")
		created := intakeTestPost(t, service, body, recoveryKey)
		if created.Code != http.StatusCreated {
			t.Fatalf("new submission status = %d: %s", created.Code, created.Body.Bytes())
		}
		var receipt wire.IntakeReceipt
		if err := json.Unmarshal(created.Body.Bytes(), &receipt); err != nil {
			t.Fatal(err)
		}
		requestDigest := sha256.Sum256(body)
		if got, want := receipt.RequestDigest, hex.EncodeToString(requestDigest[:]); got != want {
			t.Fatalf("request_digest = %q; want %q", got, want)
		}
		unsigned := struct {
			OperatorID    string `json:"operator_id"`
			RequestID     string `json:"request_id"`
			RequestDigest string `json:"request_digest"`
			ReceivedAt    string `json:"received_at"`
			Status        string `json:"status"`
		}{receipt.OperatorID, receipt.RequestID, receipt.RequestDigest, receipt.ReceivedAt, receipt.Status}
		canonical, err := canon.Encode(unsigned)
		if err != nil {
			t.Fatal(err)
		}
		if err := keys.Verify(service.ProviderID(), "intake-receipt", canonical, receipt.Signature); err != nil {
			t.Fatalf("receipt signature: %v", err)
		}
		if receipt.RequestID != request.RequestID {
			t.Fatalf("receipt request_id = %q; want %q", receipt.RequestID, request.RequestID)
		}
		now = now.Add(601 * time.Second)
		retried := intakeTestPost(t, service, body, recoveryKey)
		if retried.Code != http.StatusOK || !bytes.Equal(retried.Body.Bytes(), created.Body.Bytes()) {
			t.Fatalf("exact retry = %d %s; want original receipt %s", retried.Code, retried.Body.Bytes(), created.Body.Bytes())
		}
	})
}

func TestIntakeConflictDoesNotMutate(t *testing.T) {
	t.Run("changed statement under the same ID conflicts and preserves stored bytes", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		service := intakeTestService(t, &now)
		request, body, recoveryKey := intakeTestSubmission(t, service, now, "Original")
		if response := intakeTestPost(t, service, body, recoveryKey); response.Code != http.StatusCreated {
			t.Fatalf("initial submission status = %d", response.Code)
		}
		storedBefore, found := intakeTestStored(t, service, request.RequestID)
		if !found {
			t.Fatal("stored request missing")
		}
		request.Statement = "Changed"
		changed, err := wire.Encode("IntakeRequest", request)
		if err != nil {
			t.Fatal(err)
		}
		conflict := intakeTestPost(t, service, changed, recoveryKey)
		if conflict.Code != http.StatusConflict {
			t.Fatalf("changed request status = %d: %s", conflict.Code, conflict.Body.Bytes())
		}
		intakeAssertBody(t, conflict, `{"status":"request_conflict"}`)
		storedAfter, found := intakeTestStored(t, service, request.RequestID)
		if !found || !bytes.Equal(storedBefore.RequestBytes, storedAfter.RequestBytes) {
			t.Fatal("conflicting request changed the stored submission")
		}
	})
}

func TestIntakeAcceptanceWindow(t *testing.T) {
	t.Run("601 seconds ahead is rejected without storing", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		service := intakeTestService(t, &now)
		request, body, recoveryKey := intakeTestSubmission(t, service, now, "Future")
		request.AcceptUntil = wire.FormatTime(now.Add(601 * time.Second))
		body, err := wire.Encode("IntakeRequest", request)
		if err != nil {
			t.Fatal(err)
		}
		response := intakeTestPost(t, service, body, recoveryKey)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("future acceptance status = %d", response.Code)
		}
		intakeAssertBody(t, response, `{"status":"invalid_query"}`)
		if _, found := intakeTestStored(t, service, request.RequestID); found {
			t.Fatal("future request was stored")
		}
	})

	t.Run("past acceptance is gone and is not stored", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		service := intakeTestService(t, &now)
		request, body, recoveryKey := intakeTestSubmission(t, service, now, "Expired")
		request.AcceptUntil = wire.FormatTime(now)
		body, err := wire.Encode("IntakeRequest", request)
		if err != nil {
			t.Fatal(err)
		}
		response := intakeTestPost(t, service, body, recoveryKey)
		if response.Code != http.StatusGone {
			t.Fatalf("expired acceptance = %d %s", response.Code, response.Body.Bytes())
		}
		intakeAssertBody(t, response, `{"status":"not_found"}`)
		if _, found := intakeTestStored(t, service, request.RequestID); found {
			t.Fatal("expired request was stored")
		}
	})
}

func TestIntakeAuthenticationAndRecovery(t *testing.T) {
	t.Run("wrong POST secret is unauthorized and wrong recovery secret is undisclosed", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		service := intakeTestService(t, &now)
		request, body, recoveryKey := intakeTestSubmission(t, service, now, "Secret")
		wrongKey := append([]byte(nil), recoveryKey...)
		wrongKey[0] ^= 1
		post := intakeTestPost(t, service, body, wrongKey)
		if post.Code != http.StatusUnauthorized {
			t.Fatalf("wrong POST secret status = %d", post.Code)
		}
		intakeAssertBody(t, post, `{"status":"unauthenticated"}`)
		if created := intakeTestPost(t, service, body, recoveryKey); created.Code != http.StatusCreated {
			t.Fatalf("valid POST status = %d", created.Code)
		}
		get := intakeTestRequest(t, service, http.MethodGet, "/v1/intake/"+request.RequestID, nil, keys.EncodeB64URL(wrongKey))
		if get.Code != http.StatusNotFound {
			t.Fatalf("wrong recovery secret status = %d", get.Code)
		}
		intakeAssertBody(t, get, `{"status":"not_found"}`)
		unknownID := keys.EncodeB64URL(make([]byte, 32))
		unknown := intakeTestRequest(t, service, http.MethodGet, "/v1/intake/"+unknownID, nil, keys.EncodeB64URL(recoveryKey))
		if unknown.Code != http.StatusNotFound || !bytes.Equal(unknown.Body.Bytes(), get.Body.Bytes()) {
			t.Fatalf("unknown recovery = %d %s; want same 404 body as wrong secret %s", unknown.Code, unknown.Body.Bytes(), get.Body.Bytes())
		}
	})

	t.Run("recovery returns the exact request and original receipt", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		service := intakeTestService(t, &now)
		request, body, recoveryKey := intakeTestSubmission(t, service, now, "Recover me")
		created := intakeTestPost(t, service, body, recoveryKey)
		recovered := intakeTestRequest(t, service, http.MethodGet, "/v1/intake/"+request.RequestID, nil, keys.EncodeB64URL(recoveryKey))
		if recovered.Code != http.StatusOK {
			t.Fatalf("recovery status = %d: %s", recovered.Code, recovered.Body.Bytes())
		}
		var result wire.IntakeRecovery
		if err := json.Unmarshal(recovered.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		requestBytes, err := wire.Encode("IntakeRequest", result.Request)
		if err != nil || !bytes.Equal(requestBytes, body) {
			t.Fatalf("recovered request differs from submitted bytes: %v", err)
		}
		var original wire.IntakeReceipt
		if err := json.Unmarshal(created.Body.Bytes(), &original); err != nil {
			t.Fatal(err)
		}
		if result.Receipt != original {
			t.Fatalf("recovered receipt = %+v; want original %+v", result.Receipt, original)
		}
	})
}

func TestIntakeBoundsAndForms(t *testing.T) {
	t.Run("oversized body is bounded and route kind mismatch is invalid", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		service := intakeTestService(t, &now)
		reader := &intakeCountingReader{reader: bytes.NewReader(bytes.Repeat([]byte("x"), 300000))}
		httpRequest := httptest.NewRequest(http.MethodPost, "/v1/notices", reader)
		httpRequest.RemoteAddr = "192.0.2.200:1234"
		large := httptest.NewRecorder()
		service.Handler().ServeHTTP(large, httpRequest)
		if got := large.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("Cache-Control = %q; want no-store", got)
		}
		if large.Code != http.StatusBadRequest {
			t.Fatalf("oversized body status = %d", large.Code)
		}
		intakeAssertBody(t, large, `{"status":"invalid_query"}`)
		if reader.read > intakeMaxRequestBytes+1 {
			t.Fatalf("handler read %d bytes; limit is %d", reader.read, intakeMaxRequestBytes+1)
		}
		request, body, recoveryKey := intakeTestSubmission(t, service, now, "Wrong route")
		request.Kind = "rights_request"
		request.RequestedRight = "access"
		body, err := wire.Encode("IntakeRequest", request)
		if err != nil {
			t.Fatal(err)
		}
		response := intakeTestPost(t, service, body, recoveryKey)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("wrong route kind status = %d", response.Code)
		}
		intakeAssertBody(t, response, `{"status":"invalid_query"}`)
	})

	t.Run("form bytes are JCS and match their advertised digest", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		service := intakeTestService(t, &now)
		response := intakeTestRequest(t, service, http.MethodGet, "/v1/intake-forms/notice/1", nil, "")
		wantForm := []byte(fmt.Sprintf(`{"fields":[],"form_id":"notice","form_version":"1","instructions":"Submit a bounded notice for operator review.","kind":"notice","operator_id":"%s"}`, service.ProviderID()))
		if !bytes.Equal(response.Body.Bytes(), wantForm) {
			t.Fatalf("form bytes = %s; want %s", response.Body.Bytes(), wantForm)
		}
		request, body, recoveryKey := intakeTestSubmission(t, service, now, "Digest")
		request.FormDigest = strings.Repeat("0", 64)
		body, err := wire.Encode("IntakeRequest", request)
		if err != nil {
			t.Fatal(err)
		}
		invalid := intakeTestPost(t, service, body, recoveryKey)
		if invalid.Code != http.StatusBadRequest {
			t.Fatalf("mismatched form digest status = %d", invalid.Code)
		}
		intakeAssertBody(t, invalid, `{"status":"invalid_query"}`)
		if _, found := intakeTestStored(t, service, request.RequestID); found {
			t.Fatal("request with mismatched form digest was stored")
		}
		unknown := intakeTestRequest(t, service, http.MethodGet, "/v1/intake-forms/notice/2", nil, "")
		if unknown.Code != http.StatusNotFound {
			t.Fatalf("unknown form status = %d", unknown.Code)
		}
		intakeAssertBody(t, unknown, `{"status":"not_found"}`)
	})
}

func TestIntakeRegionalFieldByteLimit(t *testing.T) {
	form := wire.IntakeForm{Fields: []wire.IntakeFormField{{Name: "region", MaxUTF8Bytes: 3}}}
	t.Run("value at byte limit is allowed", func(t *testing.T) {
		if !intakeRegionalFieldsAllowed(map[string]string{"region": "éx"}, form) {
			t.Fatal("three-byte regional value was rejected")
		}
	})
	t.Run("value over byte limit is rejected", func(t *testing.T) {
		if intakeRegionalFieldsAllowed(map[string]string{"region": "éxy"}, form) {
			t.Fatal("four-byte regional value exceeded the three-byte limit")
		}
	})
}

func intakeAssertBody(t *testing.T, response *httptest.ResponseRecorder, want string) {
	t.Helper()
	if got := response.Body.String(); got != want {
		t.Fatalf("response body = %q; want %q", got, want)
	}
}
