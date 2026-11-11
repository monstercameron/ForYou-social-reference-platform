package provider

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/delegation"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/succession"
	"foryou.local/foryou/internal/wire"
)

func TestAuthorizationRoutes(t *testing.T) {
	t.Run("enrollment replay control and state use one-use challenges", func(t *testing.T) {
		s, now := autService(t)
		defer s.Close()
		author := autAuthorKey(t)
		authorID := keys.DIDOf(author)
		enrollment, err := delegation.SignEnroll(author, wire.AuthorizationEpochEnroll{ProviderID: s.ProviderID(), RequestNonce: keys.EncodeB64URL(bytes.Repeat([]byte{1}, 32)), NotAfter: wire.FormatTime(now.Add(time.Minute))})
		if err != nil {
			t.Fatal(err)
		}
		body, err := canon.Encode(enrollment)
		if err != nil {
			t.Fatal(err)
		}
		headers := autRootHeaders(t, s, author, "POST", "/v1/authorization-epochs", body)
		first := autRequest(t, s.Handler(), http.MethodPost, "/v1/authorization-epochs", body, headers)
		if first.Code != http.StatusCreated {
			t.Fatalf("enroll status = %d, body %s; want 201", first.Code, first.Body)
		}
		var state wire.AuthorizationEpochState
		if err := wire.Decode(first.Body.Bytes(), "AuthorizationEpochState", &state); err != nil || state.Status != "active" || state.StateRevision != 1 || state.Epoch.EpochSequence != 1 {
			t.Fatalf("enroll state = %+v, %v; want active revision 1 epoch 1", state, err)
		}
		replayed := autRequest(t, s.Handler(), http.MethodPost, "/v1/authorization-epochs", body, headers)
		autWantError(t, replayed, http.StatusUnauthorized, "unauthenticated")

		retryHeaders := autRootHeaders(t, s, author, "POST", "/v1/authorization-epochs", body)
		retry := autRequest(t, s.Handler(), http.MethodPost, "/v1/authorization-epochs", body, retryHeaders)
		if retry.Code != http.StatusOK {
			t.Fatalf("exact enrollment retry status = %d; want 200", retry.Code)
		}
		if retry.Body.String() != first.Body.String() {
			t.Fatalf("exact enrollment retry body = %s; want original state %s", retry.Body, first.Body)
		}

		control, err := delegation.SignControl(author, wire.AuthorizationEpochControl{ProviderID: s.ProviderID(), AuthorID: authorID, ExpectedEpochID: state.Epoch.EpochID, ExpectedStateRevision: 1, Action: "freeze", Nonce: keys.EncodeB64URL(bytes.Repeat([]byte{2}, 32))})
		if err != nil {
			t.Fatal(err)
		}
		controlBody, err := canon.Encode(control)
		if err != nil {
			t.Fatal(err)
		}
		controlHeaders := autRootHeaders(t, s, author, "POST", "/v1/authorization-epochs/control", controlBody)
		frozen := autRequest(t, s.Handler(), http.MethodPost, "/v1/authorization-epochs/control", controlBody, controlHeaders)
		if frozen.Code != http.StatusOK {
			t.Fatalf("freeze status = %d; want 200", frozen.Code)
		}
		if err := wire.Decode(frozen.Body.Bytes(), "AuthorizationEpochState", &state); err != nil || state.StateRevision != 2 || state.Status != "frozen" {
			t.Fatalf("freeze state = %+v, %v; want frozen revision 2", state, err)
		}

		stale, err := delegation.SignControl(author, wire.AuthorizationEpochControl{ProviderID: s.ProviderID(), AuthorID: authorID, ExpectedEpochID: state.Epoch.EpochID, ExpectedStateRevision: 1, Action: "freeze", Nonce: keys.EncodeB64URL(bytes.Repeat([]byte{3}, 32))})
		if err != nil {
			t.Fatal(err)
		}
		staleBody, err := canon.Encode(stale)
		if err != nil {
			t.Fatal(err)
		}
		staleHeaders := autRootHeaders(t, s, author, "POST", "/v1/authorization-epochs/control", staleBody)
		autWantError(t, autRequest(t, s.Handler(), http.MethodPost, "/v1/authorization-epochs/control", staleBody, staleHeaders), http.StatusConflict, "stale_state")

		stateChallenge := keys.EncodeB64URL(bytes.Repeat([]byte{4}, 32))
		stateBody, err := canon.Encode(struct {
			AuthorID  string `json:"author_id"`
			Challenge string `json:"challenge"`
		}{authorID, stateChallenge})
		if err != nil {
			t.Fatal(err)
		}
		stateHeaders := autRootHeaders(t, s, author, "POST", "/v1/authorization-epochs/state", stateBody)
		stateResponse := autRequest(t, s.Handler(), http.MethodPost, "/v1/authorization-epochs/state", stateBody, stateHeaders)
		if stateResponse.Code != http.StatusOK {
			t.Fatalf("state status = %d, body %s; want 200", stateResponse.Code, stateResponse.Body)
		}
		var signedState wire.AuthorizationEpochStateResponse
		if err := wire.Decode(stateResponse.Body.Bytes(), "AuthorizationEpochStateResponse", &signedState); err != nil || delegation.VerifyStateResponse(signedState, s.ProviderID(), stateChallenge) != nil {
			t.Fatalf("state response failed signature or challenge verification: %v", err)
		}

		wrongAuthorBody, err := canon.Encode(struct {
			AuthorID  string `json:"author_id"`
			Challenge string `json:"challenge"`
		}{keys.DIDOf(autDelegateKey(t)), keys.EncodeB64URL(bytes.Repeat([]byte{5}, 32))})
		if err != nil {
			t.Fatal(err)
		}
		wrongAuthorHeaders := autRootHeaders(t, s, author, "POST", "/v1/authorization-epochs/state", wrongAuthorBody)
		autWantError(t, autRequest(t, s.Handler(), http.MethodPost, "/v1/authorization-epochs/state", wrongAuthorBody, wrongAuthorHeaders), http.StatusUnauthorized, "unauthenticated")
	})

	t.Run("challenge binding rejects replay and another route", func(t *testing.T) {
		s, _ := autService(t)
		defer s.Close()
		author := autAuthorKey(t)
		body, err := canon.Encode(struct {
			Value string `json:"value"`
		}{"bound"})
		if err != nil {
			t.Fatal(err)
		}
		headers := autRootHeaders(t, s, author, "POST", "/v1/authorization-epochs/control", body)
		wrongRoute := autRequest(t, s.Handler(), http.MethodPost, "/v1/revocations", body, headers)
		autWantError(t, wrongRoute, http.StatusUnauthorized, "unauthenticated")
	})

	t.Run("revocation accepts an unseen valid delegation", func(t *testing.T) {
		s, now := autService(t)
		defer s.Close()
		author := autAuthorKey(t)
		state := autEnroll(t, s, author, now)
		signer := autDelegateKey(t)
		d, err := delegation.SignDelegation(author, wire.Delegation{AuthorID: keys.DIDOf(author), SignerID: keys.DIDOf(signer), ProviderID: s.ProviderID(), Scope: []string{"post"}, Epoch: *state.Epoch, ValidFrom: wire.FormatTime(now), ValidUntil: wire.FormatTime(now.Add(time.Hour))})
		if err != nil {
			t.Fatal(err)
		}
		request, err := delegation.SignRevocation(author, wire.RevocationRequest{ProviderID: s.ProviderID(), DelegationID: d.DelegationID, Delegation: d})
		if err != nil {
			t.Fatal(err)
		}
		body, err := canon.Encode(request)
		if err != nil {
			t.Fatal(err)
		}
		headers := autRootHeaders(t, s, author, "POST", "/v1/revocations", body)
		response := autRequest(t, s.Handler(), http.MethodPost, "/v1/revocations", body, headers)
		if response.Code != http.StatusOK {
			t.Fatalf("revoke status = %d, body %s; want 200", response.Code, response.Body)
		}
		var ack map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &ack); err != nil || len(ack) != 2 || ack["delegation_id"] != d.DelegationID || ack["status"] != "revoked" {
			t.Fatalf("revocation acknowledgement = %v, %v; want exactly the two specified fields", ack, err)
		}
		retryHeaders := autRootHeaders(t, s, author, "POST", "/v1/revocations", body)
		retry := autRequest(t, s.Handler(), http.MethodPost, "/v1/revocations", body, retryHeaders)
		if retry.Code != http.StatusOK || retry.Body.String() != response.Body.String() {
			t.Fatalf("idempotent revoke = %d %s; want the same 200 acknowledgement", retry.Code, retry.Body)
		}
	})
}

func TestServiceChallengeBounds(t *testing.T) {
	t.Run("4097-byte issuance body is invalid_query", func(t *testing.T) {
		s, _ := autService(t)
		defer s.Close()
		response := autRequest(t, s.Handler(), http.MethodPost, "/v1/service-challenges", bytes.Repeat([]byte{' '}, 4097), autJSONHeaders())
		autWantError(t, response, http.StatusBadRequest, "invalid_query")
	})
	t.Run("query resource is invalid_query", func(t *testing.T) {
		s, _ := autService(t)
		defer s.Close()
		body, err := canon.Encode(delegation.ChallengeRequest{AuthorID: keys.DIDOf(autAuthorKey(t)), Method: "POST", Resource: "/v1/authorization-epochs?x=1", BodyHash: strings.Repeat("0", 64)})
		if err != nil {
			t.Fatal(err)
		}
		response := autRequest(t, s.Handler(), http.MethodPost, "/v1/service-challenges", body, autJSONHeaders())
		autWantError(t, response, http.StatusBadRequest, "invalid_query")
	})
}

func TestAuthorizationStateChallengeValidation(t *testing.T) {
	t.Run("noncanonical challenge is invalid_query", func(t *testing.T) {
		s, _ := autService(t)
		defer s.Close()
		author := autAuthorKey(t)
		body, err := canon.Encode(struct {
			AuthorID  string `json:"author_id"`
			Challenge string `json:"challenge"`
		}{keys.DIDOf(author), strings.Repeat("!", 43)})
		if err != nil {
			t.Fatal(err)
		}
		headers := autRootHeaders(t, s, author, http.MethodPost, "/v1/authorization-epochs/state", body)
		autWantError(t, autRequest(t, s.Handler(), http.MethodPost, "/v1/authorization-epochs/state", body, headers), http.StatusBadRequest, "invalid_query")
	})
}

func TestSuccessionRoutes(t *testing.T) {
	t.Run("publishes vector once and looks it up by old DID", func(t *testing.T) {
		s, _ := autService(t)
		defer s.Close()
		value := autSuccessionVector()
		if err := succession.Verify(value); err != nil {
			t.Fatal(err)
		}
		body, err := canon.Encode(value)
		if err != nil {
			t.Fatal(err)
		}
		first := autRequest(t, s.Handler(), http.MethodPost, "/v1/successions", body, autJSONHeaders())
		second := autRequest(t, s.Handler(), http.MethodPost, "/v1/successions", body, autJSONHeaders())
		if first.Code != http.StatusCreated || second.Code != http.StatusOK {
			t.Fatalf("succession publish statuses = %d/%d; want 201/200", first.Code, second.Code)
		}
		lookup := autRequest(t, s.Handler(), http.MethodGet, "/v1/successions/"+value.OldID, nil, nil)
		if lookup.Code != http.StatusOK {
			t.Fatalf("succession lookup status = %d; want 200", lookup.Code)
		}
		var result struct {
			Successions []wire.KeySuccession `json:"successions"`
		}
		if err := json.Unmarshal(lookup.Body.Bytes(), &result); err != nil || len(result.Successions) != 1 || result.Successions[0] != value {
			t.Fatalf("succession lookup = %+v, %v; want the published vector", result, err)
		}
		missing := autRequest(t, s.Handler(), http.MethodGet, "/v1/successions/"+keys.DIDOf(autDelegateKey(t)), nil, nil)
		if missing.Code != http.StatusNotFound || missing.Body.String() != `{"status":"not_found"}` {
			t.Fatalf("missing succession response = %d %s; want exact 404 body", missing.Code, missing.Body)
		}
	})
	t.Run("tampered signature and noncanonical DID are rejected", func(t *testing.T) {
		s, _ := autService(t)
		defer s.Close()
		value := autSuccessionVector()
		tampered := []byte(value.NewSignature)
		if tampered[43] == 'A' {
			tampered[43] = 'B'
		} else {
			tampered[43] = 'A'
		}
		value.NewSignature = string(tampered)
		body, err := canon.Encode(value)
		if err != nil {
			t.Fatal(err)
		}
		invalid := autRequest(t, s.Handler(), http.MethodPost, "/v1/successions", body, autJSONHeaders())
		autWantError(t, invalid, http.StatusUnprocessableEntity, "invalid_message")
		badDID := autRequest(t, s.Handler(), http.MethodGet, "/v1/successions/not-a-did", nil, nil)
		autWantError(t, badDID, http.StatusBadRequest, "invalid_query")
	})
}

func autService(t *testing.T) (*Service, time.Time) {
	t.Helper()
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	cfg, err := TestConfig(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s, now
}

func autAuthorKey(t *testing.T) keys.PrivateKey {
	t.Helper()
	return autKeyFromSeedHex(t, "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f")
}

func autDelegateKey(t *testing.T) keys.PrivateKey {
	t.Helper()
	return autKeyFromSeedHex(t, "404142434445464748494a4b4c4d4e4f505152535455565758595a5b5c5d5e5f")
}

func autKeyFromSeedHex(t *testing.T, seedHex string) keys.PrivateKey {
	t.Helper()
	seed, err := hex.DecodeString(seedHex)
	if err != nil {
		t.Fatal(err)
	}
	key, err := keys.NewKeyFromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func autSuccessionVector() wire.KeySuccession {
	return wire.KeySuccession{SuccessionID: "fyks2:a3156461bf70d3de6fa794f8f1d1cb6a0c7ac71674aa717c26004612b0c808b6", OldID: "did:key:z6MkehRgf7yJbgaGfYsdoAsKdBPE3dj2CYhowQdcjqSJgvVd", NewID: "did:key:z6Mkgxj2R3HLtQRpPnvfvpuKEceSqf3tZHBjdmZ3fFz3JHGG", EffectiveAt: "2026-12-01T00:00:00Z", OldSignature: "r_Zxt-fw5D9i8N9UgSaYi1FBeRALA9FbfZQF6MnO2nnX8kf8cBBT0jcu-1LhC4LUCbc46yQIyaEGL5Yqk1ZuDw", NewSignature: "0JyWdFLhAHFOQKimHD13oKxTVvbW45tm3YR43wo94KM1FihVlMDGUarOv-3mk0ZLFWVz1qyD3ViRB5yb06S4BQ"}
}

func autEnroll(t *testing.T, s *Service, author keys.PrivateKey, now time.Time) wire.AuthorizationEpochState {
	t.Helper()
	request, err := delegation.SignEnroll(author, wire.AuthorizationEpochEnroll{ProviderID: s.ProviderID(), RequestNonce: keys.EncodeB64URL(bytes.Repeat([]byte{9}, 32)), NotAfter: wire.FormatTime(now.Add(time.Minute))})
	if err != nil {
		t.Fatal(err)
	}
	body, err := canon.Encode(request)
	if err != nil {
		t.Fatal(err)
	}
	headers := autRootHeaders(t, s, author, "POST", "/v1/authorization-epochs", body)
	response := autRequest(t, s.Handler(), http.MethodPost, "/v1/authorization-epochs", body, headers)
	if response.Code != http.StatusCreated {
		t.Fatalf("enroll status = %d, body %s; want 201", response.Code, response.Body)
	}
	var state wire.AuthorizationEpochState
	if err := wire.Decode(response.Body.Bytes(), "AuthorizationEpochState", &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func autRootHeaders(t *testing.T, s *Service, author keys.PrivateKey, method, resource string, body []byte) http.Header {
	t.Helper()
	request := delegation.ChallengeRequest{AuthorID: keys.DIDOf(author), Method: method, Resource: resource, BodyHash: autBodyHash(t, body)}
	challengeBody, err := canon.Encode(request)
	if err != nil {
		t.Fatal(err)
	}
	challengeResponse := autRequest(t, s.Handler(), http.MethodPost, "/v1/service-challenges", challengeBody, autJSONHeaders())
	if challengeResponse.Code != http.StatusOK {
		t.Fatalf("challenge status = %d, body %s; want 200", challengeResponse.Code, challengeResponse.Body)
	}
	var challenge wire.ServiceChallenge
	if err := wire.Decode(challengeResponse.Body.Bytes(), "ServiceChallenge", &challenge); err != nil {
		t.Fatal(err)
	}
	challengeHeader, signature, err := delegation.SignChallenge(author, challenge)
	if err != nil {
		t.Fatal(err)
	}
	headers := autJSONHeaders()
	headers.Set(delegation.HeaderChallenge, challengeHeader)
	headers.Set(delegation.HeaderSignature, signature)
	return headers
}

func autBodyHash(t *testing.T, body []byte) string {
	t.Helper()
	hash, err := delegation.BodyHash(delegation.BodyJSON, body)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func autJSONHeaders() http.Header {
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	return headers
}

func autRequest(t *testing.T, handler http.Handler, method, target string, body []byte, headers http.Header) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, bytes.NewReader(body))
	if headers != nil {
		request.Header = headers.Clone()
	}
	request.RemoteAddr = "192.0.2.10:12345"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func autWantError(t *testing.T, response *httptest.ResponseRecorder, status int, name string) {
	t.Helper()
	var body struct {
		Status string `json:"status"`
	}
	if response.Code != status || json.Unmarshal(response.Body.Bytes(), &body) != nil || body.Status != name {
		t.Fatalf("error response = %d %s; want %d status %q", response.Code, response.Body, status, name)
	}
}
