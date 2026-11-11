package provider

import (
	"context"
	"crypto/sha256"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/content"
	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/message"
	"foryou.local/foryou/internal/sdk"
	"foryou.local/foryou/internal/wire"
)

func TestProviderVaultDeliveryRetriesAndPersistsOutcome(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	dataDir := t.TempDir()
	cfg, err := TestConfig(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	cfg.DataDir = dataDir
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	settlementFundAuthor(t, s, now)
	messageBytes := vaultTestAcceptedMessage(t, s, now, 92001)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/v1/vault/messages/"+vaultTestMessageID(t, messageBytes) {
			t.Errorf("vault request = %s %s", r.Method, r.URL.Path)
		}
		count := requests.Add(1)
		if count == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"status":"not_ready"}`))
			return
		}
		var delivery wire.VaultDelivery
		if err := wire.Decode(vaultTestBody(t, r), "VaultDelivery", &delivery); err != nil {
			t.Errorf("decode vault delivery: %v", err)
		} else if string(delivery.Message) != string(messageBytes) {
			t.Errorf("vault message bytes = %s; want exact stored bytes %s", delivery.Message, messageBytes)
		}
		receiptBody := vaultTestReceipt(t, vaultTestMessageID(t, messageBytes), now)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(receiptBody)
	}))
	defer server.Close()
	vault, err := sdk.New(server.URL, sdk.Options{ServiceID: keys.DIDOf(devnet.FoundationKey()), AllowLoopbackHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	s.SetVault(vault)
	now = time.Date(2026, 11, 11, 12, 3, 0, 0, time.UTC)
	if err := s.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	claims, err := s.Settlement().Claims(wire.BitcoinMainnet, 0, 10)
	if err != nil || len(claims) != 1 {
		t.Fatalf("published claims = %d, %v; want one", len(claims), err)
	}
	if requests.Load() != 0 {
		t.Fatalf("PUT count before settlement = %d; want 0", requests.Load())
	}
	now = now.Add(time.Minute)
	settleProofWitnesses(t, s, claims[0], now)
	if err := s.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 {
		t.Fatalf("first vault pass PUT count = %d; want 1", requests.Load())
	}
	if err := s.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 {
		t.Fatalf("retry vault pass PUT count = %d; want 2", requests.Load())
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.SetVault(vault)
	if err := s.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 {
		t.Fatalf("post-restart PUT count = %d; want no redelivery", requests.Load())
	}
}

func vaultTestAcceptedMessage(t *testing.T, s *Service, now time.Time, index int) []byte {
	t.Helper()
	key := devnet.ReplicaKey()
	intent := wire.Intent{
		AuthorID:    keys.DIDOf(key),
		Timestamp:   wire.FormatTime(now),
		MessageType: "post",
		Version:     wire.Version{Major: 2},
		Payload:     wire.PostPayload{ContentFormat: "text/plain", Text: "vault delivery record"},
	}
	if err := content.ValidatePostContent(intent.Payload); err != nil {
		t.Fatal(err)
	}
	messageID, err := message.ID(intent)
	if err != nil {
		t.Fatal(err)
	}
	receipt := settlementAdmitMessage(t, s, now.Add(10*time.Second), messageID, index)
	posted, err := message.Assemble(intent, receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := message.Sign(&posted, key); err != nil {
		t.Fatal(err)
	}
	messageBytes, err := canon.Encode(posted)
	if err != nil {
		t.Fatal(err)
	}
	intentBytes, err := canon.Encode(intent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Records().Accept(Acceptance{MessageID: messageID, Bytes: messageBytes, IntentDigest: sha256.Sum256(intentBytes), ReceiptID: receipt.ReceiptID, IssuedAt: now.Add(10 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	return messageBytes
}

func vaultTestReceipt(t *testing.T, messageID string, now time.Time) []byte {
	t.Helper()
	receipt := wire.VaultReceipt{FoundationID: keys.DIDOf(devnet.FoundationKey()), MessageID: messageID, StoredAt: wire.FormatTime(now)}
	unsigned, err := canon.EncodeWithout(receipt, "foundation_signature")
	if err != nil {
		t.Fatal(err)
	}
	receipt.FoundationSignature, err = keys.Sign(devnet.FoundationKey(), "vault-receipt", unsigned)
	if err != nil {
		t.Fatal(err)
	}
	body, err := wire.Encode("VaultReceipt", receipt)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func vaultTestMessageID(t *testing.T, messageBytes []byte) string {
	t.Helper()
	var posted wire.PostMessage
	if err := wire.Decode(messageBytes, "PostMessage", &posted); err != nil {
		t.Fatal(err)
	}
	return posted.MessageID
}

func vaultTestBody(t *testing.T, r *http.Request) []byte {
	t.Helper()
	data, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
