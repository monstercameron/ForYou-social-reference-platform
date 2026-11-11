package provider

import (
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"foryou.local/foryou/internal/apierr"
	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/wire"
)

func TestSettlementClaimsPublicRoutes(t *testing.T) {
	now := time.Date(2026, 11, 11, 11, 59, 0, 0, time.UTC)
	s, claims, _ := settleRouteClaims(t, &now)
	defer s.Close()
	t.Run("claims after sequence return the next two claims", func(t *testing.T) {
		response := settleRouteRequest(s, http.MethodGet, "/v1/settlement-claims?network=bitcoin-mainnet&after_sequence=1", nil)
		if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("claims response = %d, Cache-Control %q", response.Code, response.Header().Get("Cache-Control"))
		}
		var page wire.SettlementClaimsPage
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Claims) != 2 || page.Claims[0].Sequence != 2 || page.Claims[1].Sequence != 3 {
			t.Fatalf("claims page sequences = %+v; want 2 and 3", page.Claims)
		}
	})
	t.Run("unsupported network is invalid query", func(t *testing.T) {
		response := settleRouteRequest(s, http.MethodGet, "/v1/settlement-claims?network=litecoin", nil)
		settleAssertError(t, response, http.StatusBadRequest, "invalid_query")
	})
	t.Run("noncanonical sequence is invalid query", func(t *testing.T) {
		response := settleRouteRequest(s, http.MethodGet, "/v1/settlement-claims?network=bitcoin-mainnet&after_sequence=01", nil)
		settleAssertError(t, response, http.StatusBadRequest, "invalid_query")
	})
	t.Run("unknown parameter is invalid query", func(t *testing.T) {
		response := settleRouteRequest(s, http.MethodGet, "/v1/settlement-claims?network=bitcoin-mainnet&extra=1", nil)
		settleAssertError(t, response, http.StatusBadRequest, "invalid_query")
	})
	t.Run("head is a no-store public read without a body", func(t *testing.T) {
		response := settleRouteRequest(s, http.MethodHead, "/v1/settlement-claims?network=bitcoin-mainnet", nil)
		if response.Code != http.StatusOK || response.Body.Len() != 0 || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("HEAD response = %d body %q Cache-Control %q", response.Code, response.Body.String(), response.Header().Get("Cache-Control"))
		}
	})
	t.Run("leaves are in tree order and reproduce root", func(t *testing.T) {
		response := settleRouteRequest(s, http.MethodGet, "/v1/settlement-claims/"+claims[0].ClaimID+"/leaves?offset=0", nil)
		if response.Code != http.StatusOK {
			t.Fatalf("leaves response = %d %s", response.Code, response.Body.String())
		}
		var page wire.SettlementLeavesPage
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		root, err := settlement.TreeRoot(page.Leaves)
		if err != nil || hex.EncodeToString(root.Hash[:]) != claims[0].ReceiptsRoot {
			t.Fatalf("TreeRoot = %v, %v; want %s", root, err, claims[0].ReceiptsRoot)
		}
	})
	t.Run("unknown claim leaves return not found", func(t *testing.T) {
		response := settleRouteRequest(s, http.MethodGet, "/v1/settlement-claims/fysc2:"+strings.Repeat("0", 64)+"/leaves", nil)
		settleAssertError(t, response, http.StatusNotFound, "not_found")
	})
}

func TestSettlementWitnessRouteVerifiesAndReturnsHeldObject(t *testing.T) {
	now := time.Date(2026, 11, 11, 11, 59, 0, 0, time.UTC)
	s, claims, _ := settleRouteClaims(t, &now)
	defer s.Close()
	claim := claims[0]
	witness := settleRouteWitness(t, claim, 0, now.Add(time.Hour))
	encoded, err := wire.Encode("SettlementWitness", witness)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("new witness is created and retry returns held witness", func(t *testing.T) {
		first := settleRouteRequest(s, http.MethodPost, "/v1/settlement-witnesses", encoded)
		if first.Code != http.StatusCreated {
			t.Fatalf("first witness response = %d %s", first.Code, first.Body.String())
		}
		retry := settleRouteRequest(s, http.MethodPost, "/v1/settlement-witnesses", encoded)
		if retry.Code != http.StatusOK || retry.Body.String() != first.Body.String() {
			t.Fatalf("witness retry = %d %s; first body %s", retry.Code, retry.Body.String(), first.Body.String())
		}
		listed := settleRouteRequest(s, http.MethodGet, "/v1/settlement-claims/"+claim.ClaimID+"/witnesses", nil)
		var page wire.SettlementWitnessesPage
		if listed.Code != http.StatusOK || json.Unmarshal(listed.Body.Bytes(), &page) != nil || len(page.Witnesses) != 1 || page.Witnesses[0] != witness {
			t.Fatalf("witness list = %d %s", listed.Code, listed.Body.String())
		}
	})
	t.Run("malformed body is invalid message", func(t *testing.T) {
		response := settleRouteRequest(s, http.MethodPost, "/v1/settlement-witnesses", []byte("{"))
		settleAssertError(t, response, http.StatusUnprocessableEntity, "invalid_message")
	})
	t.Run("oversized body is invalid message", func(t *testing.T) {
		response := settleRouteRequest(s, http.MethodPost, "/v1/settlement-witnesses", []byte(strings.Repeat(" ", (16<<10)+1)))
		settleAssertError(t, response, http.StatusUnprocessableEntity, "invalid_message")
	})
	t.Run("tampered credit is rejected", func(t *testing.T) {
		tampered := witness
		tampered.CreditBaseUnits = "2"
		body, err := wire.Encode("SettlementWitness", tampered)
		if err != nil {
			t.Fatal(err)
		}
		response := settleRouteRequest(s, http.MethodPost, "/v1/settlement-witnesses", body)
		settleAssertError(t, response, http.StatusUnprocessableEntity, "invalid_message")
	})
	t.Run("unknown claim is not found", func(t *testing.T) {
		unknown := settleRouteWitness(t, claim, 1, now.Add(time.Hour))
		unknown.ClaimID = "fysc2:" + strings.Repeat("0", 64)
		body, err := wire.Encode("SettlementWitness", unknown)
		if err != nil {
			t.Fatal(err)
		}
		response := settleRouteRequest(s, http.MethodPost, "/v1/settlement-witnesses", body)
		settleAssertError(t, response, http.StatusNotFound, "not_found")
	})
	t.Run("valid signature from a noncommittee key is rejected", func(t *testing.T) {
		key := devnet.ProviderKey(9)
		nonmember, err := settlement.SignWitness(key, claim, big.NewInt(1), now.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		body, err := wire.Encode("SettlementWitness", nonmember)
		if err != nil {
			t.Fatal(err)
		}
		response := settleRouteRequest(s, http.MethodPost, "/v1/settlement-witnesses", body)
		settleAssertError(t, response, http.StatusUnprocessableEntity, "invalid_message")
	})
}

func TestSettlementProofRouteFindsClaimQuorum(t *testing.T) {
	now := time.Date(2026, 11, 11, 11, 59, 0, 0, time.UTC)
	s, claims, receipts := settleRouteClaims(t, &now)
	defer s.Close()
	claim := claims[0]
	settleProofWitnesses(t, s, claim, now.Add(time.Hour))
	response := settleRouteRequest(s, http.MethodGet, "/v1/settlements/"+receipts[0].ReceiptID, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("proof response = %d %s", response.Code, response.Body.String())
	}
	var proof wire.SettlementProof
	if err := json.Unmarshal(response.Body.Bytes(), &proof); err != nil {
		t.Fatal(err)
	}
	if _, err := settlement.VerifyProof(receipts[0], proof, settlement.ProofContext{Committee: devnet.Policies()[0].Monitors}); err != nil {
		t.Fatalf("returned proof does not settle: %v", err)
	}
}

func TestSettlementProofRouteLinksToLaterQuorum(t *testing.T) {
	now := time.Date(2026, 11, 11, 11, 59, 0, 0, time.UTC)
	s, claims, receipts := settleRouteClaims(t, &now)
	defer s.Close()
	settleProofWitnesses(t, s, claims[1], now.Add(time.Hour))
	response := settleRouteRequest(s, http.MethodGet, "/v1/settlements/"+receipts[0].ReceiptID, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("proof response = %d %s", response.Code, response.Body.String())
	}
	var proof wire.SettlementProof
	if err := json.Unmarshal(response.Body.Bytes(), &proof); err != nil {
		t.Fatal(err)
	}
	if len(proof.Chain) != 1 || proof.Chain[0].ClaimID != claims[1].ClaimID {
		t.Fatalf("proof chain = %+v; want the next claim", proof.Chain)
	}
	if _, err := settlement.VerifyProof(receipts[0], proof, settlement.ProofContext{Committee: devnet.Policies()[0].Monitors}); err != nil {
		t.Fatalf("chained proof does not settle: %v", err)
	}
}

func TestSettlementProofUnknownReceiptIsNotFound(t *testing.T) {
	now := time.Date(2026, 11, 11, 11, 59, 0, 0, time.UTC)
	s, _, _ := settleRouteClaims(t, &now)
	defer s.Close()
	response := settleRouteRequest(s, http.MethodGet, "/v1/settlements/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", nil)
	settleAssertError(t, response, http.StatusNotFound, "not_found")
}

func settleRouteClaims(t *testing.T, now *time.Time) (*Service, []wire.SettlementClaim, []wire.SubmissionReceipt) {
	t.Helper()
	s, _ := settlementTestService(t, now, "")
	settlementFundAuthor(t, s, *now)
	*now = time.Date(2026, 11, 11, 12, 0, 10, 0, time.UTC)
	first := settlementAdmit(t, s, *now, 91001)
	*now = time.Date(2026, 11, 11, 12, 1, 10, 0, time.UTC)
	second := settlementAdmit(t, s, *now, 91002)
	*now = time.Date(2026, 11, 11, 12, 2, 10, 0, time.UTC)
	third := settlementAdmit(t, s, *now, 91003)
	*now = time.Date(2026, 11, 11, 12, 5, 0, 0, time.UTC)
	claims := settlementRun(t, s)
	if len(claims) != 3 {
		t.Fatalf("settlement claims = %d; want 3", len(claims))
	}
	return s, claims, []wire.SubmissionReceipt{first, second, third}
}

func settleRouteWitness(t *testing.T, claim wire.SettlementClaim, member int, at time.Time) wire.SettlementWitness {
	t.Helper()
	witness, err := settlement.SignWitness(devnet.MonitorKey(member), claim, big.NewInt(1), at)
	if err != nil {
		t.Fatal(err)
	}
	return witness
}

func settleProofWitnesses(t *testing.T, s *Service, claim wire.SettlementClaim, at time.Time) {
	t.Helper()
	witness0 := settleRouteWitness(t, claim, 0, at)
	witness1 := settleRouteWitness(t, claim, 1, at)
	witness2 := settleRouteWitness(t, claim, 2, at)
	witness3 := settleRouteWitness(t, claim, 3, at)
	if _, err := s.Settlement().AddWitness(witness0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Settlement().AddWitness(witness1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Settlement().AddWitness(witness2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Settlement().AddWitness(witness3); err != nil {
		t.Fatal(err)
	}
}

func settleRouteRequest(s *Service, method, target string, body []byte) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, strings.NewReader(string(body)))
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, request)
	return response
}

func settleAssertError(t *testing.T, response *httptest.ResponseRecorder, status int, name string) {
	t.Helper()
	var body apierr.PublicReadError
	if response.Code != status || json.Unmarshal(response.Body.Bytes(), &body) != nil || string(body.Status) != name {
		t.Fatalf("error response = %d %s; want %d {status:%q}", response.Code, response.Body.String(), status, name)
	}
}
