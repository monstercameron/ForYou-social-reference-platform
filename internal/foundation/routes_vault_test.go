package foundation

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/fee"
	"foryou.local/foryou/internal/governance"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/message"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/succession"
	"foryou.local/foryou/internal/wire"
)

func TestVaultDeliveryRoutes(t *testing.T) {
	t.Run("new delivery signs and logs exactly once", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
		service, err := New(testConfig(t, &now))
		if err != nil {
			t.Fatal(err)
		}
		defer service.Close()
		delivery, messageID := vaultDeliveryTestDelivery(t, now, 4)
		body, err := canon.Encode(delivery)
		if err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/v1/vault/messages/"+messageID, bytes.NewReader(body)))
		if response.Code != http.StatusCreated || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("delivery response = %d, Cache-Control %q, body %s", response.Code, response.Header().Get("Cache-Control"), response.Body)
		}
		var receipt wire.VaultReceipt
		if err := json.Unmarshal(response.Body.Bytes(), &receipt); err != nil {
			t.Fatal(err)
		}
		storedAt, err := wire.ParseTime(receipt.StoredAt)
		if err != nil || service.Trust().VerifyFoundation(receipt, storedAt, "vault-receipt", nil) != nil {
			t.Fatalf("vault receipt did not verify at stored_at: %v", err)
		}
		entries := service.LogEntries(-1, 10)
		record, held := service.VaultRecord(messageID)
		if len(entries) != 1 || entries[0].Kind != string(governance.LogKindVaultReceipt) || !held || entries[0].ObjectHash != record.VaultReceiptHash {
			t.Fatalf("vault log entries = %+v", entries)
		}
		second := httptest.NewRecorder()
		service.Handler().ServeHTTP(second, httptest.NewRequest(http.MethodPut, "/v1/vault/messages/"+messageID, bytes.NewReader(body)))
		if second.Code != http.StatusOK || second.Body.String() != response.Body.String() || len(service.LogEntries(-1, 10)) != 1 {
			t.Fatalf("repeat response = %d %s; log = %+v", second.Code, second.Body, service.LogEntries(-1, 10))
		}
	})

	t.Run("proof without quorum is not ready", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
		service, err := New(testConfig(t, &now))
		if err != nil {
			t.Fatal(err)
		}
		defer service.Close()
		delivery, messageID := vaultDeliveryTestDelivery(t, now, 3)
		vaultDeliveryAssertPutStatus(t, service, messageID, delivery, http.StatusConflict, `"status":"not_ready"`)
	})

	t.Run("path mismatch is invalid", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
		service, err := New(testConfig(t, &now))
		if err != nil {
			t.Fatal(err)
		}
		defer service.Close()
		delivery, _ := vaultDeliveryTestDelivery(t, now, 4)
		vaultDeliveryAssertPutStatus(t, service, "fy1:"+strings.Repeat("0", 64), delivery, http.StatusUnprocessableEntity, `"status":"invalid_message"`)
	})

	t.Run("proof for a different receipt is invalid", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
		service, err := New(testConfig(t, &now))
		if err != nil {
			t.Fatal(err)
		}
		defer service.Close()
		delivery, messageID := vaultDeliveryTestDelivery(t, now, 4)
		delivery.Proof.Leaf.ReceiptHash = strings.Repeat("0", 64)
		vaultDeliveryAssertPutStatus(t, service, messageID, delivery, http.StatusUnprocessableEntity, `"status":"invalid_message"`)
	})

	t.Run("broken audit path is invalid", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
		service, err := New(testConfig(t, &now))
		if err != nil {
			t.Fatal(err)
		}
		defer service.Close()
		delivery, messageID := vaultDeliveryTestDelivery(t, now, 4)
		delivery.Proof.AuditPath = []wire.AuditNode{{Hash: strings.Repeat("0", 64), Sum: "1"}}
		vaultDeliveryAssertPutStatus(t, service, messageID, delivery, http.StatusUnprocessableEntity, `"status":"invalid_message"`)
	})

	t.Run("missing proof is a schema failure", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
		service, err := New(testConfig(t, &now))
		if err != nil {
			t.Fatal(err)
		}
		defer service.Close()
		delivery, messageID := vaultDeliveryTestDelivery(t, now, 4)
		body, err := json.Marshal(map[string]json.RawMessage{"message": delivery.Message})
		if err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/v1/vault/messages/"+messageID, bytes.NewReader(body)))
		if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), `"status":"invalid_message"`) {
			t.Fatalf("missing proof response = %d %s", response.Code, response.Body)
		}
	})

	t.Run("receipt amount above the scheduled fee is invalid", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
		service, err := New(testConfig(t, &now))
		if err != nil {
			t.Fatal(err)
		}
		defer service.Close()
		delivery, messageID := vaultDeliveryTestDeliveryFor(t, now, 4, func(receipt *wire.SubmissionReceipt) {
			amount, err := fee.ParseAmount(receipt.AmountBaseUnits)
			if err != nil {
				t.Fatal(err)
			}
			receipt.AmountBaseUnits = amount.Add(amount, big.NewInt(1)).String()
		})
		vaultDeliveryAssertPutStatus(t, service, messageID, delivery, http.StatusUnprocessableEntity, `"status":"invalid_message"`)
	})

	t.Run("receipt fee schedule network must match", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
		service, err := New(testConfig(t, &now))
		if err != nil {
			t.Fatal(err)
		}
		defer service.Close()
		delivery, messageID := vaultDeliveryTestDeliveryFor(t, now, 4, func(receipt *wire.SubmissionReceipt) {
			receipt.Network = "ethereum-mainnet"
		})
		vaultDeliveryAssertPutStatus(t, service, messageID, delivery, http.StatusUnprocessableEntity, `"status":"invalid_message"`)
	})

	t.Run("witness merge is valid-only and idempotent", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
		service, err := New(testConfig(t, &now))
		if err != nil {
			t.Fatal(err)
		}
		defer service.Close()
		first, messageID := vaultDeliveryTestDelivery(t, now, 4)
		firstBody, err := canon.Encode(first)
		if err != nil {
			t.Fatal(err)
		}
		firstResponse := httptest.NewRecorder()
		service.Handler().ServeHTTP(firstResponse, httptest.NewRequest(http.MethodPut, "/v1/vault/messages/"+messageID, bytes.NewReader(firstBody)))
		if firstResponse.Code != http.StatusCreated {
			t.Fatalf("first delivery = %d %s", firstResponse.Code, firstResponse.Body)
		}
		incoming, _ := vaultDeliveryTestDelivery(t, now, 5)
		corrupt := incoming.Proof.Witnesses[4]
		corrupt.WitnessSignature = strings.Repeat("A", len(corrupt.WitnessSignature))
		incoming.Proof.Witnesses = append(incoming.Proof.Witnesses, corrupt)
		incomingBody, err := canon.Encode(incoming)
		if err != nil {
			t.Fatal(err)
		}
		secondResponse := httptest.NewRecorder()
		service.Handler().ServeHTTP(secondResponse, httptest.NewRequest(http.MethodPut, "/v1/vault/messages/"+messageID, bytes.NewReader(incomingBody)))
		if secondResponse.Code != http.StatusOK || secondResponse.Body.String() != firstResponse.Body.String() || len(service.LogEntries(-1, 10)) != 1 {
			t.Fatalf("merged delivery = %d %s; log entries = %d", secondResponse.Code, secondResponse.Body, len(service.LogEntries(-1, 10)))
		}
		record, ok := service.VaultRecord(messageID)
		if !ok || len(record.Proof.Witnesses) != 5 {
			t.Fatalf("merged witnesses = %d, found %v", len(record.Proof.Witnesses), ok)
		}
		repeat := httptest.NewRecorder()
		service.Handler().ServeHTTP(repeat, httptest.NewRequest(http.MethodPut, "/v1/vault/messages/"+messageID, bytes.NewReader(firstBody)))
		record, _ = service.VaultRecord(messageID)
		if repeat.Code != http.StatusOK || len(record.Proof.Witnesses) != 5 {
			t.Fatalf("repeat delivery = %d; witness count = %d", repeat.Code, len(record.Proof.Witnesses))
		}
	})

	t.Run("expired Foundation certificate delays receipts", func(t *testing.T) {
		future := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
		cfg := testConfig(t, &future)
		cfg.Certificates = func(time.Time) []wire.FoundationKeyCertificate { return nil }
		service, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer service.Close()
		delivery, messageID := vaultDeliveryTestDelivery(t, future, 4)
		body, err := canon.Encode(delivery)
		if err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/v1/vault/messages/"+messageID, bytes.NewReader(body)))
		if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), `"retry_after_seconds":30`) {
			t.Fatalf("expired certificate response = %d %s", response.Code, response.Body)
		}
	})

	t.Run("receipt lookup distinguishes invalid, missing and held IDs", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
		service, err := New(testConfig(t, &now))
		if err != nil {
			t.Fatal(err)
		}
		defer service.Close()
		delivery, messageID := vaultDeliveryTestDelivery(t, now, 4)
		body, err := canon.Encode(delivery)
		if err != nil {
			t.Fatal(err)
		}
		put := httptest.NewRecorder()
		service.Handler().ServeHTTP(put, httptest.NewRequest(http.MethodPut, "/v1/vault/messages/"+messageID, bytes.NewReader(body)))
		if put.Code != http.StatusCreated {
			t.Fatalf("delivery = %d %s", put.Code, put.Body)
		}
		held := httptest.NewRecorder()
		service.Handler().ServeHTTP(held, httptest.NewRequest(http.MethodGet, "/v1/vault/receipts/"+messageID, nil))
		if held.Code != http.StatusOK || held.Body.String() != put.Body.String() {
			t.Fatalf("held receipt = %d %s", held.Code, held.Body)
		}
		missing := httptest.NewRecorder()
		service.Handler().ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/v1/vault/receipts/fy1:"+strings.Repeat("0", 64), nil))
		if missing.Code != http.StatusNotFound {
			t.Fatalf("missing receipt = %d %s", missing.Code, missing.Body)
		}
		invalid := httptest.NewRecorder()
		service.Handler().ServeHTTP(invalid, httptest.NewRequest(http.MethodGet, "/v1/vault/receipts/fy1:XYZ", nil))
		if invalid.Code != http.StatusBadRequest {
			t.Fatalf("invalid receipt ID = %d %s", invalid.Code, invalid.Body)
		}
	})

	t.Run("chain naming five distinct successions is stored", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
		service, err := New(testConfig(t, &now))
		if err != nil {
			t.Fatal(err)
		}
		defer service.Close()
		delivery, messageID, successions := vaultDeliveryTestChainDelivery(t, now, 5)
		service.successionSource = vaultDeliveryTestSuccessionSource(successions)
		vaultDeliveryAssertPutStatus(t, service, messageID, delivery, http.StatusCreated, `"message_id":"`+messageID+`"`)
	})

	t.Run("succession lookups beyond the budget are unavailable, not invalid", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
		service, err := New(testConfig(t, &now))
		if err != nil {
			t.Fatal(err)
		}
		defer service.Close()
		delivery, messageID, successions := vaultDeliveryTestChainDelivery(t, now, 5)
		service.successionSource = vaultDeliveryTestSuccessionSource(successions)
		service.successionBudget = 4
		vaultDeliveryAssertPutStatus(t, service, messageID, delivery, http.StatusServiceUnavailable, `{"retry_after_seconds":30,"status":"unavailable"}`)
	})

	t.Run("chain longer than 1,000 claims is invalid, not unavailable", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
		service, err := New(testConfig(t, &now))
		if err != nil {
			t.Fatal(err)
		}
		defer service.Close()
		delivery, messageID, successions := vaultDeliveryTestChainDelivery(t, now, 1001)
		lookups := 0
		answer := vaultDeliveryTestSuccessionSource(successions)
		service.successionSource = func(ctx context.Context, providerID string) ([]wire.KeySuccession, error) {
			lookups++
			return answer(ctx, providerID)
		}
		vaultDeliveryAssertPutStatus(t, service, messageID, delivery, http.StatusUnprocessableEntity, `"status":"invalid_message"`)
		if lookups != 0 {
			t.Fatalf("succession lookups = %d, want 0", lookups)
		}
	})

	t.Run("committed legal restriction refuses delivery", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
		service, err := New(testConfig(t, &now))
		if err != nil {
			t.Fatal(err)
		}
		defer service.Close()
		delivery, messageID := vaultDeliveryTestDelivery(t, now, 4)
		if _, err := service.CommitLegalRestriction(messageID); err != nil {
			t.Fatal(err)
		}
		vaultDeliveryAssertPutStatus(t, service, messageID, delivery, http.StatusForbidden, `"status":"legal_restricted"`)
	})

	t.Run("four MiB body bound rejects oversized input", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
		service, err := New(testConfig(t, &now))
		if err != nil {
			t.Fatal(err)
		}
		defer service.Close()
		body := &vaultDeliveryCountingReader{reader: bytes.NewReader(bytes.Repeat([]byte{' '}, 4*vaultDeliveryMaxBody))}
		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/v1/vault/messages/fy1:"+strings.Repeat("0", 64), body))
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("oversized body response = %d %s", response.Code, response.Body)
		}
		if body.read > vaultDeliveryMaxBody+1 {
			t.Fatalf("handler read %d bytes of a %d-byte body; want at most %d", body.read, 4*vaultDeliveryMaxBody, vaultDeliveryMaxBody+1)
		}
	})
}

func vaultDeliveryAssertPutStatus(t *testing.T, service *Service, pathID string, delivery wire.VaultDelivery, wantStatus int, wantBody string) {
	t.Helper()
	body, err := canon.Encode(delivery)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	service.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/v1/vault/messages/"+pathID, bytes.NewReader(body)))
	if response.Code != wantStatus || !strings.Contains(response.Body.String(), wantBody) {
		t.Fatalf("delivery response = %d %s; want %d containing %s", response.Code, response.Body, wantStatus, wantBody)
	}
}

// vaultDeliveryTestSuccessionSource answers succession lookups from successions keyed by old provider DID.
func vaultDeliveryTestSuccessionSource(successions map[string]wire.KeySuccession) func(context.Context, string) ([]wire.KeySuccession, error) {
	return func(_ context.Context, providerID string) ([]wire.KeySuccession, error) {
		if item, ok := successions[providerID]; ok {
			return []wire.KeySuccession{item}, nil
		}
		return []wire.KeySuccession{}, nil
	}
}

// vaultDeliveryTestChainDelivery extends a settled delivery with links provider key changes, each named by a succession,
// and re-signs the committee witnesses on the final claim of the chain.
func vaultDeliveryTestChainDelivery(t *testing.T, now time.Time, links int) (wire.VaultDelivery, string, map[string]wire.KeySuccession) {
	t.Helper()
	delivery, messageID := vaultDeliveryTestDelivery(t, now, 4)
	successions := make(map[string]wire.KeySuccession, links)
	chain := make([]wire.SettlementClaim, 0, links)
	previous := delivery.Proof.Claim
	providerKey := devnet.ProviderKey(0)
	for index := 1; index <= links; index++ {
		effectiveAt, err := wire.ParseTime(previous.PeriodEnd)
		if err != nil {
			t.Fatal(err)
		}
		nextKey := devnet.ProviderKey(index)
		item, err := succession.New(providerKey, nextKey, effectiveAt)
		if err != nil {
			t.Fatal(err)
		}
		successions[item.OldID] = item
		successionID := item.SuccessionID
		claim, err := settlement.BuildClaim(nextKey, settlement.ClaimInput{
			Network: previous.Network, Previous: &previous, PeriodStart: effectiveAt, PeriodEnd: effectiveAt.Add(time.Minute),
			DeclarationID: previous.DeclarationID, SuccessionID: &successionID, ReceiptCount: 1, Total: big.NewInt(1), ReceiptsRoot: strings.Repeat("0", 64),
		})
		if err != nil {
			t.Fatal(err)
		}
		chain = append(chain, claim)
		previous = claim
		providerKey = nextKey
	}
	final := chain[len(chain)-1]
	finalEnd, err := wire.ParseTime(final.PeriodEnd)
	if err != nil {
		t.Fatal(err)
	}
	witnessAt := now
	if finalEnd.After(now) {
		witnessAt = finalEnd
	}
	witnesses := make([]wire.SettlementWitness, 0, 4)
	for index := 0; index < 4; index++ {
		witness, err := settlement.SignWitness(devnet.MonitorKey(index), final, big.NewInt(100), witnessAt)
		if err != nil {
			t.Fatal(err)
		}
		witnesses = append(witnesses, witness)
	}
	delivery.Proof.Chain = chain
	delivery.Proof.Witnesses = witnesses
	return delivery, messageID, successions
}

type vaultDeliveryCountingReader struct {
	reader io.Reader
	read   int
}

func (r *vaultDeliveryCountingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.read += n
	return n, err
}

func vaultDeliveryTestDelivery(t *testing.T, now time.Time, witnessCount int) (wire.VaultDelivery, string) {
	t.Helper()
	return vaultDeliveryTestDeliveryFor(t, now, witnessCount, nil)
}

// vaultDeliveryTestDeliveryFor builds a settled delivery whose proof binds the receipt after adjust changed it.
func vaultDeliveryTestDeliveryFor(t *testing.T, now time.Time, witnessCount int, adjust func(*wire.SubmissionReceipt)) (wire.VaultDelivery, string) {
	t.Helper()
	authorKey := devnet.ReplicaKey()
	providerKey := devnet.ProviderKey(0)
	intent := wire.Intent{AuthorID: keys.DIDOf(authorKey), Timestamp: wire.FormatTime(now), MessageType: "post", Version: wire.Version{Major: 2, Minor: 0}, Payload: wire.PostPayload{ContentFormat: "text/plain", Text: "vault delivery"}}
	messageID, err := message.ID(intent)
	if err != nil {
		t.Fatal(err)
	}
	schedule := devnet.FeeSchedules()[0].Schedule
	billable, err := canon.Encode(intent)
	if err != nil {
		t.Fatal(err)
	}
	amount, err := fee.Amount(schedule, len(billable))
	if err != nil {
		t.Fatal(err)
	}
	quoteSuffix := strings.Repeat("0", 64)
	receipt := wire.SubmissionReceipt{ProviderID: keys.DIDOf(providerKey), AuthorID: intent.AuthorID, MessageID: messageID, QuoteID: "fyfq2:" + quoteSuffix, ScheduleID: schedule.ScheduleID, Network: schedule.Network, AmountBaseUnits: amount.String(), AuthorizationAt: wire.FormatTime(now), IssuedAt: wire.FormatTime(now)}
	receipt.ReceiptID, err = fee.NewReceiptID(bytes.NewReader(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	if adjust != nil {
		adjust(&receipt)
	}
	receipt, err = fee.SignReceipt(receipt, providerKey)
	if err != nil {
		t.Fatal(err)
	}
	posted, err := message.Assemble(intent, receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := message.Sign(&posted, authorKey); err != nil {
		t.Fatal(err)
	}
	leaf, err := settlement.LeafOf(receipt)
	if err != nil {
		t.Fatal(err)
	}
	root, err := settlement.TreeRoot([]wire.SettlementLeaf{leaf})
	if err != nil {
		t.Fatal(err)
	}
	periodEnd := now.Add(-time.Hour)
	claim, err := settlement.BuildClaim(providerKey, settlement.ClaimInput{Network: receipt.Network, PeriodStart: periodEnd.Add(-24 * time.Hour), PeriodEnd: periodEnd, DeclarationID: "fyrd2:" + quoteSuffix, ReceiptCount: 1, Total: root.Sum, ReceiptsRoot: hex.EncodeToString(root.Hash[:])})
	if err != nil {
		t.Fatal(err)
	}
	first, err := settlement.SignWitness(devnet.MonitorKey(0), claim, big.NewInt(100), now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := settlement.SignWitness(devnet.MonitorKey(1), claim, big.NewInt(100), now)
	if err != nil {
		t.Fatal(err)
	}
	third, err := settlement.SignWitness(devnet.MonitorKey(2), claim, big.NewInt(100), now)
	if err != nil {
		t.Fatal(err)
	}
	witnesses := []wire.SettlementWitness{first, second, third}
	if witnessCount >= 4 {
		fourth, err := settlement.SignWitness(devnet.MonitorKey(3), claim, big.NewInt(100), now)
		if err != nil {
			t.Fatal(err)
		}
		witnesses = append(witnesses, fourth)
	}
	if witnessCount >= 5 {
		fifth, err := settlement.SignWitness(devnet.MonitorKey(4), claim, big.NewInt(100), now)
		if err != nil {
			t.Fatal(err)
		}
		witnesses = append(witnesses, fifth)
	}
	proof := wire.SettlementProof{Claim: claim, Leaf: leaf, LeafIndex: 0, AuditPath: []wire.AuditNode{}, Witnesses: witnesses}
	messageBytes, err := canon.Encode(posted)
	if err != nil {
		t.Fatal(err)
	}
	return wire.VaultDelivery{Message: messageBytes, Proof: proof}, messageID
}
