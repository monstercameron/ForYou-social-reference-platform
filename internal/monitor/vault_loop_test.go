package monitor

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"foryou.local/foryou/internal/apierr"
	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/committee"
	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/fee"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/message"
	"foryou.local/foryou/internal/retention"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/store"
	"foryou.local/foryou/internal/wire"
)

var vaultTestNow = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)

// vaultTestWorld is a provider and a vault served by httptest, with a receipt under dev schedule 1
// in a one-leaf claim of devnet provider 0.
type vaultTestWorld struct {
	service     *Service
	chain       *testChain
	message     []byte
	receipt     wire.SubmissionReceipt
	issuedAt    time.Time
	now         time.Time
	declaration wire.RetentionDeclaration
	claim       wire.SettlementClaim
	leaves      []wire.SettlementLeaf
	witnesses   []wire.SettlementWitness

	mu                sync.Mutex
	serveProof        bool
	shownWitnesses    int
	vaultHolds        bool
	vaultNotReady     bool
	changesAfter      []string
	changesProviderID string
	deliveries        []wire.VaultDelivery
	deliveredBytes    []json.RawMessage
}

func TestVaultDelivery(t *testing.T) {
	t.Run("settled message the vault lacks is delivered once with the provider's proof", func(t *testing.T) {
		w := vaultNewWorld(t)
		w.serveProof = true
		w.runOnce(t)
		w.runOnce(t)
		w.mu.Lock()
		defer w.mu.Unlock()
		if len(w.deliveries) != 1 {
			t.Fatalf("deliveries = %d; want 1", len(w.deliveries))
		}
		if string(w.deliveredBytes[0]) != string(w.message) {
			t.Errorf("delivered message = %s; want the exact served bytes %s", w.deliveredBytes[0], w.message)
		}
		if got := w.deliveries[0].Proof; got.Claim.ClaimID != w.claim.ClaimID || len(got.Witnesses) != 4 {
			t.Errorf("delivered proof claim %s with %d witnesses; want the provider's proof for %s with 4", got.Claim.ClaimID, len(got.Witnesses), w.claim.ClaimID)
		}
		if strings.Join(w.changesAfter, ",") != "0,1" {
			t.Errorf("changes requested after %v; want 0 then the advanced cursor 1", w.changesAfter)
		}
	})
	t.Run("proof is assembled from claims, leaves and member pages when the provider has none", func(t *testing.T) {
		w := vaultNewWorld(t)
		w.shownWitnesses = 0
		w.auditByMembers(t, 0, 1, 2, 3)
		w.runOnce(t)
		w.mu.Lock()
		defer w.mu.Unlock()
		if len(w.deliveries) != 1 {
			t.Fatalf("deliveries = %d; want 1", len(w.deliveries))
		}
		proof := w.deliveries[0].Proof
		if _, err := settlement.VerifyProof(w.receipt, proof, settlement.ProofContext{Committee: devnet.Genesis().DistributionPolicy.Monitors}); err != nil {
			t.Errorf("VerifyProof(delivered proof) = %v; want nil", err)
		}
		if len(proof.Witnesses) != 4 || proof.Claim.ClaimID != w.claim.ClaimID || proof.LeafIndex != 0 {
			t.Errorf("assembled proof = claim %s, leaf %d, %d witnesses; want claim %s, leaf 0, 4 witnesses", proof.Claim.ClaimID, proof.LeafIndex, len(proof.Witnesses), w.claim.ClaimID)
		}
	})
	t.Run("unsettled receipt is delivered on the pass after a fourth witness appears", func(t *testing.T) {
		w := vaultNewWorld(t)
		w.shownWitnesses = 3
		w.runOnce(t)
		w.mu.Lock()
		if len(w.deliveries) != 0 {
			t.Errorf("deliveries with 3 witnesses = %d; want 0", len(w.deliveries))
		}
		w.shownWitnesses = 4
		w.mu.Unlock()
		w.runOnce(t)
		w.mu.Lock()
		defer w.mu.Unlock()
		if len(w.deliveries) != 1 {
			t.Errorf("deliveries with 4 witnesses = %d; want 1", len(w.deliveries))
		}
	})
	t.Run("message the vault already holds is not delivered", func(t *testing.T) {
		w := vaultNewWorld(t)
		w.serveProof = true
		w.vaultHolds = true
		w.runOnce(t)
		w.mu.Lock()
		defer w.mu.Unlock()
		if len(w.deliveries) != 0 {
			t.Errorf("deliveries = %d; want 0", len(w.deliveries))
		}
	})
	t.Run("not_ready is retried on the next pass", func(t *testing.T) {
		w := vaultNewWorld(t)
		w.serveProof = true
		w.vaultNotReady = true
		w.runOnce(t)
		w.mu.Lock()
		w.vaultNotReady = false
		w.mu.Unlock()
		w.runOnce(t)
		w.runOnce(t)
		w.mu.Lock()
		defer w.mu.Unlock()
		if len(w.deliveries) != 2 {
			t.Errorf("delivery attempts = %d; want the refused one and one retry", len(w.deliveries))
		}
	})
	t.Run("message that settles 40 days after issuance is delivered on a later pass", func(t *testing.T) {
		w := vaultNewWorld(t)
		w.shownWitnesses = 3
		w.runOnce(t)
		w.now = w.issuedAt.Add(40 * 24 * time.Hour)
		w.runOnce(t)
		w.mu.Lock()
		w.shownWitnesses = 4
		w.mu.Unlock()
		w.now = w.now.Add(time.Hour)
		w.runOnce(t)
		w.mu.Lock()
		defer w.mu.Unlock()
		if len(w.deliveries) != 1 {
			t.Fatalf("deliveries = %d; want 1 once the fourth witness appears 40 days after issuance", len(w.deliveries))
		}
	})
	t.Run("retry waits for its backoff", func(t *testing.T) {
		w := vaultNewWorld(t)
		w.shownWitnesses = 3
		w.runOnce(t)
		w.runOnce(t)
		w.mu.Lock()
		w.shownWitnesses = 4
		w.mu.Unlock()
		w.runOnce(t)
		w.mu.Lock()
		if len(w.deliveries) != 0 {
			t.Errorf("deliveries before the backoff ends = %d; want 0", len(w.deliveries))
		}
		w.mu.Unlock()
		w.now = w.now.Add(time.Hour)
		w.runOnce(t)
		w.mu.Lock()
		defer w.mu.Unlock()
		if len(w.deliveries) != 1 {
			t.Fatalf("deliveries after the one-hour backoff = %d; want 1", len(w.deliveries))
		}
	})
	t.Run("pending records that never settle do not starve new changes", func(t *testing.T) {
		previous := vaultRecordsPerPass
		vaultRecordsPerPass = 2
		t.Cleanup(func() { vaultRecordsPerPass = previous })
		w := vaultNewWorld(t)
		w.seedPending(t, 100, vaultTestNeverSettles(t, vaultTestNow.Add(-30*time.Hour)))
		w.seedPending(t, 101, vaultTestNeverSettles(t, vaultTestNow.Add(-31*time.Hour)))
		w.runOnce(t)
		w.mu.Lock()
		defer w.mu.Unlock()
		if len(w.deliveries) != 1 {
			t.Fatalf("deliveries = %d; want 1 for the new settled change in the same pass as two due retries", len(w.deliveries))
		}
	})
	t.Run("provider identity mismatch does not deliver or advance the cursor", func(t *testing.T) {
		w := vaultNewWorld(t)
		w.changesProviderID = keys.DIDOf(devnet.ProviderKey(1))
		w.runOnce(t)
		w.runOnce(t)
		w.mu.Lock()
		defer w.mu.Unlock()
		if len(w.deliveries) != 0 {
			t.Errorf("deliveries = %d; want 0", len(w.deliveries))
		}
		if strings.Join(w.changesAfter, ",") != "0,0" {
			t.Errorf("changes requested after %v; want 0 on both passes", w.changesAfter)
		}
	})
}

func vaultNewWorld(t *testing.T) *vaultTestWorld {
	t.Helper()
	providerKey := devnet.ProviderKey(0)
	providerID := keys.DIDOf(providerKey)
	issuedAt := vaultTestNow.Add(-2 * time.Hour)
	w := &vaultTestWorld{shownWitnesses: 4, issuedAt: issuedAt, now: vaultTestNow}
	w.message, w.receipt = vaultTestMessage(t, providerKey, issuedAt)
	payout := wire.PayoutRecipients{BitcoinMainnet: devnet.Recipient(devnet.ProviderLabelPrefix+"0", faux.BitcoinMainnet)}
	var err error
	w.declaration, err = retention.Declare(providerKey, retention.MinSeconds, payout, vaultTestNow.Add(-48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := settlement.LeafOf(w.receipt)
	if err != nil {
		t.Fatal(err)
	}
	w.leaves = []wire.SettlementLeaf{leaf}
	root, err := settlement.TreeRoot(w.leaves)
	if err != nil {
		t.Fatal(err)
	}
	w.claim, err = settlement.BuildClaim(providerKey, settlement.ClaimInput{
		Network: faux.BitcoinMainnet, PeriodStart: vaultTestNow.Add(-24 * time.Hour), PeriodEnd: vaultTestNow.Add(-time.Hour),
		DeclarationID: w.declaration.DeclarationID, ReceiptCount: 1, Total: root.Sum, ReceiptsRoot: hex.EncodeToString(root.Hash[:]),
	})
	if err != nil {
		t.Fatal(err)
	}
	w.witnesses = []wire.SettlementWitness{
		vaultTestWitness(t, 0, w.claim), vaultTestWitness(t, 1, w.claim), vaultTestWitness(t, 2, w.claim), vaultTestWitness(t, 3, w.claim),
	}
	provider := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		defer w.mu.Unlock()
		rw.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/changes":
			after := r.URL.Query().Get("after")
			w.changesAfter = append(w.changesAfter, after)
			changesProviderID := w.changesProviderID
			if changesProviderID == "" {
				changesProviderID = providerID
			}
			if after == "0" {
				_, _ = io.WriteString(rw, `{"items":[{"message":`+string(w.message)+`,"sequence":1}],"more":false,"next_after":1,"provider_id":"`+changesProviderID+`"}`)
				return
			}
			_, _ = io.WriteString(rw, `{"items":[],"more":false,"next_after":1,"provider_id":"`+changesProviderID+`"}`)
		case r.URL.Path == "/v1/settlements/"+w.receipt.ReceiptID && w.serveProof:
			proof, err := settlement.BuildProof(w.claim, w.leaves, 0, w.witnesses, nil)
			if err != nil {
				t.Errorf("BuildProof: %v", err)
			}
			_ = json.NewEncoder(rw).Encode(proof)
		case r.URL.Path == "/v1/settlement-claims":
			_ = json.NewEncoder(rw).Encode(wire.SettlementClaimsPage{Claims: []wire.SettlementClaim{w.claim}})
		case r.URL.Path == "/v1/settlement-claims/"+w.claim.ClaimID+"/leaves":
			_ = json.NewEncoder(rw).Encode(wire.SettlementLeavesPage{ClaimID: w.claim.ClaimID, Offset: 0, Leaves: w.leaves})
		case r.URL.Path == "/v1/settlement-claims/"+w.claim.ClaimID+"/witnesses":
			_ = json.NewEncoder(rw).Encode(wire.SettlementWitnessesPage{Witnesses: w.witnesses[:w.shownWitnesses]})
		default:
			apierr.Write(rw, apierr.PublicReadError{Status: apierr.NotFound})
		}
	}))
	t.Cleanup(provider.Close)

	vault := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		defer w.mu.Unlock()
		rw.Header().Set("Content-Type", "application/json")
		messageID := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/v1/vault/receipts/"), "/v1/vault/messages/")
		switch {
		case r.Method == http.MethodGet && w.vaultHolds:
			_ = json.NewEncoder(rw).Encode(vaultTestReceipt(t, messageID))
		case r.Method == http.MethodPut:
			var delivery wire.VaultDelivery
			body, _ := io.ReadAll(r.Body)
			if err := wire.Decode(body, "VaultDelivery", &delivery); err != nil {
				t.Errorf("decode delivery: %v", err)
			}
			var exact struct {
				Message json.RawMessage `json:"message"`
			}
			_ = json.Unmarshal(body, &exact)
			w.deliveries = append(w.deliveries, delivery)
			w.deliveredBytes = append(w.deliveredBytes, exact.Message)
			if w.vaultNotReady {
				apierr.Write(rw, apierr.PublicReadError{Status: apierr.NotReady})
				return
			}
			w.vaultHolds = true
			rw.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(rw).Encode(vaultTestReceipt(t, messageID))
		default:
			apierr.Write(rw, apierr.PublicReadError{Status: apierr.NotFound})
		}
	}))
	t.Cleanup(vault.Close)

	w.chain = &testChain{}
	w.service, err = New(Config{
		Genesis: devnet.Genesis(), DataDir: t.TempDir(), Now: func() time.Time { return w.now },
		Peers: map[string]string{"foundation": vault.URL, "provider0": provider.URL}, Faux: w.chain,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.service.Close() })
	return w
}

func (w *vaultTestWorld) runOnce(t *testing.T) {
	t.Helper()
	if err := w.service.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
}

func (w *vaultTestWorld) seedPending(t *testing.T, sequence int64, message []byte) {
	t.Helper()
	provider := w.service.ProviderList()[0]
	record := vaultPendingRecord{ProviderURL: provider.URL, ProviderID: provider.ID, Sequence: sequence, Attempts: 1, NextAttempt: w.now, Message: message}
	if err := w.service.DB().Update(func(tx store.Tx) error { return vaultPending.Put(tx, vaultPendingKey(record), record) }); err != nil {
		t.Fatal(err)
	}
}

// vaultTestNeverSettles returns a message whose issued_at lies outside every claim period, so no proof can ever exist.
func vaultTestNeverSettles(t *testing.T, issuedAt time.Time) []byte {
	t.Helper()
	message, _ := vaultTestMessage(t, devnet.ProviderKey(0), issuedAt)
	return message
}

// auditByMembers funds the provider and lets the given hosted members witness the claim, so the
// witnesses are on their pages and not on the provider's.
func (w *vaultTestWorld) auditByMembers(t *testing.T, members ...int) {
	t.Helper()
	commitment, err := settlement.DepositCommitment(keys.DIDOf(devnet.ProviderKey(0)), faux.BitcoinMainnet)
	if err != nil {
		t.Fatal(err)
	}
	recipient := devnet.Genesis().FoundationKeyCertificates[0].Recipients.BitcoinMainnet
	w.chain.transfers = []faux.Transfer{{Network: faux.BitcoinMainnet, Recipient: recipient, AmountChainUnits: "10000", Data: hex.EncodeToString(commitment[:]), Sequence: 1, BlockTime: "2026-12-31T00:00:00Z", Finalized: true, Success: true}}
	for _, n := range members {
		err := w.service.Members()[n].Do(func(m *committee.Monitor) error {
			if err := m.RegisterDeclaration(w.declaration); err != nil {
				return err
			}
			outcome, err := m.Audit(w.claim, w.leaves, vaultTestNow, vaultTestNow)
			if err == nil && outcome.Witness == nil {
				t.Errorf("member %d outcome = %+v; want a witness", n, outcome)
			}
			return err
		})
		if err != nil {
			t.Fatalf("member %d audit: %v", n, err)
		}
	}
}

func vaultTestMessage(t *testing.T, providerKey keys.PrivateKey, issuedAt time.Time) ([]byte, wire.SubmissionReceipt) {
	t.Helper()
	author := devnet.ProviderKey(90)
	intent := wire.Intent{
		AuthorID: keys.DIDOf(author), Timestamp: wire.FormatTime(issuedAt.Add(-30 * time.Second)), MessageType: "post",
		Version: wire.Version{Major: 2, Minor: 0}, Payload: wire.PostPayload{ContentFormat: "text/plain", Text: "Kept by the Lifetime Vault."},
	}
	id, err := message.ID(intent)
	if err != nil {
		t.Fatal(err)
	}
	schedule := devnet.Genesis().FeeSchedules[0]
	receipt := wire.SubmissionReceipt{
		ProviderID: keys.DIDOf(providerKey), AuthorID: intent.AuthorID, MessageID: id,
		ReceiptID: keys.EncodeB64URL([]byte(strings.Repeat("r", 32))), QuoteID: "fyfq2:" + strings.Repeat("a", 64),
		ScheduleID: schedule.ScheduleID, Network: schedule.Network, AmountBaseUnits: "1",
		AuthorizationAt: wire.FormatTime(issuedAt), IssuedAt: wire.FormatTime(issuedAt),
	}
	size, err := message.FinalSize(intent, receipt)
	if err != nil {
		t.Fatal(err)
	}
	amount, err := fee.Amount(schedule, size)
	if err != nil {
		t.Fatal(err)
	}
	receipt.AmountBaseUnits = amount.String()
	receipt, err = fee.SignReceipt(receipt, providerKey)
	if err != nil {
		t.Fatal(err)
	}
	post, err := message.Assemble(intent, receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := message.Sign(&post, author); err != nil {
		t.Fatal(err)
	}
	raw, err := canon.Encode(post)
	if err != nil {
		t.Fatal(err)
	}
	return raw, receipt
}

func vaultTestReceipt(t *testing.T, messageID string) wire.VaultReceipt {
	t.Helper()
	receipt := wire.VaultReceipt{FoundationID: keys.DIDOf(devnet.FoundationKey()), MessageID: messageID, StoredAt: wire.FormatTime(vaultTestNow)}
	unsigned, err := canon.EncodeWithout(receipt, "foundation_signature")
	if err != nil {
		t.Fatal(err)
	}
	receipt.FoundationSignature, err = keys.Sign(devnet.FoundationKey(), "vault-receipt", unsigned)
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func vaultTestWitness(t *testing.T, index int, claim wire.SettlementClaim) wire.SettlementWitness {
	t.Helper()
	witness, err := settlement.SignWitness(devnet.MonitorKey(index), claim, big.NewInt(1000), vaultTestNow)
	if err != nil {
		t.Fatal(err)
	}
	return witness
}
