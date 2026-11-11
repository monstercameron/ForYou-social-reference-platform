package foundation

import (
	"bytes"
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/wire"
)

func TestVaultRefreshHeldProofAfterCommitteeChange(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	service, err := New(testConfig(t, &now))
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	delivery, messageID := vaultDeliveryTestDelivery(t, now, 4)
	body, err := json.Marshal(delivery)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	service.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/v1/vault/messages/"+messageID, bytes.NewReader(body)))
	if response.Code != http.StatusCreated {
		t.Fatalf("delivery = %d %s", response.Code, response.Body)
	}
	record, ok := service.VaultRecord(messageID)
	if !ok {
		t.Fatal("delivered record is missing")
	}
	claim := record.Proof.Claim
	newMonitorKey := devnet.ReplicaKey()
	newMonitorID := keys.DIDOf(newMonitorKey)
	witness, err := settlement.SignWitness(newMonitorKey, claim, big.NewInt(100), now)
	if err != nil {
		t.Fatal(err)
	}
	monitor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/witnesses" || r.URL.Query().Get("provider_id") != claim.ProviderID || r.URL.Query().Get("network") != claim.Network || r.URL.Query().Get("after_sequence") != "0" {
			t.Errorf("witness query = %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(wire.MonitorWitnessesPage{Witnesses: []wire.SettlementWitness{witness}, Refusals: []wire.SettlementRefusal{}, Holds: []wire.SettlementHold{}, Claims: []wire.SettlementClaim{}})
	}))
	defer monitor.Close()
	service.cfg.Monitors[newMonitorID] = monitor.URL
	policy := service.Policies()[0]
	policy.Monitors = []string{keys.DIDOf(devnet.MonitorKey(0)), keys.DIDOf(devnet.MonitorKey(1)), keys.DIDOf(devnet.MonitorKey(2)), keys.DIDOf(devnet.MonitorKey(4)), newMonitorID}
	policy.EffectiveAt = wire.FormatTime(now.Add(-time.Hour))
	service.cfg.Policies = append(service.cfg.Policies, policy)
	vaultRefreshHeld(context.Background(), service, now)
	refreshed, ok := service.VaultRecord(messageID)
	if !ok || len(refreshed.Proof.Witnesses) != 5 {
		t.Fatalf("refreshed record = %+v, found %v", refreshed.Proof.Witnesses, ok)
	}
	if refreshed.Receipt != record.Receipt || refreshed.Sequence != record.Sequence {
		t.Fatalf("refresh changed receipt or sequence: got %+v, want %+v", refreshed, record)
	}
	if _, err := settlement.VerifyProof(vaultRefreshReceipt(refreshed), refreshed.Proof, settlement.ProofContext{Committee: policy.Monitors}); err != nil {
		t.Fatalf("refreshed proof did not verify under new committee: %v", err)
	}
}
