package foundation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"foryou.local/foryou/internal/governance"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

func TestSignAndLogCertificateBoundary(t *testing.T) {
	t.Run("valid certificate", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		service, err := New(testConfig(t, &now))
		if err != nil {
			t.Fatal(err)
		}
		defer service.Close()
		entry, signed := signedReceipt(t, service, "fy1:"+strings.Repeat("0", 64), "2026-11-11T00:00:00Z")
		var object map[string]any
		if err := json.Unmarshal(signed, &object); err != nil {
			t.Fatal(err)
		}
		hash, err := governance.ObjectHash(object)
		if err != nil {
			t.Fatal(err)
		}
		if entry.Index != 0 || entry.ObjectHash != hash {
			t.Fatalf("entry = %#v, object's hash = %s", entry, hash)
		}
	})

	t.Run("expired certificate", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		service, err := New(testConfig(t, &now))
		if err != nil {
			t.Fatal(err)
		}
		defer service.Close()
		_, _, err = service.SignAndLog(governance.LogKindVaultReceipt, "vault-receipt", wire.VaultReceipt{
			FoundationID: keys.DIDOf(service.cfg.Key), MessageID: "fy1:" + strings.Repeat("1", 64), StoredAt: "2027-08-01T00:00:00Z",
		})
		if !errors.Is(err, ErrNoValidCertificate) {
			t.Fatalf("SignAndLog() error = %v, want ErrNoValidCertificate", err)
		}
		if got := service.log.Len(); got != 0 {
			t.Fatalf("log size after rejected receipt = %d, want 0", got)
		}
	})
}

func TestHeadTickSignsGrowthOnce(t *testing.T) {
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	service, err := New(testConfig(t, &now))
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if made, err := service.headTick(now.Add(time.Hour)); err != nil || made {
		t.Fatalf("empty head tick = (%t, %v), want (false, nil)", made, err)
	}
	signedReceipt(t, service, "fy1:"+strings.Repeat("2", 64), "2026-11-11T00:00:00Z")
	if made, err := service.headTick(now.Add(time.Hour)); err != nil || !made {
		t.Fatalf("growth head tick = (%t, %v), want (true, nil)", made, err)
	}
	head, ok := service.LatestHead()
	if !ok || head.Size != 1 {
		t.Fatalf("LatestHead() = (%#v, %t), want size 1", head, ok)
	}
	if err := governance.VerifyLogHead(head); err != nil {
		t.Fatalf("VerifyLogHead() error = %v", err)
	}
	entry := service.LogEntries(-1, 1)[0]
	proof, err := service.log.Proof(entry.ObjectHash, head.Size)
	if err != nil {
		t.Fatal(err)
	}
	root, err := governance.RootFromProof(entry.ObjectHash, proof)
	if err != nil || root != head.Root {
		t.Fatalf("RootFromProof() = (%q, %v), want %q", root, err, head.Root)
	}
	if made, err := service.headTick(now.Add(2 * time.Hour)); err != nil || made {
		t.Fatalf("same-size head tick = (%t, %v), want (false, nil)", made, err)
	}
	if got := len(service.SignedHeads()); got != 1 {
		t.Fatalf("signed head count = %d, want 1", got)
	}
}

func TestRestartReplaysLogVaultAndSequence(t *testing.T) {
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	cfg := testConfig(t, &now)
	service, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	signedReceipt(t, service, "fy1:"+strings.Repeat("3", 64), "2026-11-11T00:00:00Z")
	signedReceipt(t, service, "fy1:"+strings.Repeat("4", 64), "2026-11-11T00:00:00Z")
	messageID := "fy1:" + strings.Repeat("5", 64)
	message := vaultMessage(messageID, "settlement-receipt-1")
	first, created, err := service.StoreVaultRecord(message, wire.SettlementProof{}, now)
	if err != nil || !created || first.Sequence != 1 {
		t.Fatalf("StoreVaultRecord() = (sequence %d, created %t, error %v), want (1, true, nil)", first.Sequence, created, err)
	}
	if byReceipt, ok := service.VaultRecordByReceiptID("settlement-receipt-1"); !ok || byReceipt.MessageID != messageID {
		t.Fatalf("settlement receipt lookup before restart = (%#v, %t), want message %q", byReceipt, ok, messageID)
	}
	logSizeAfterFirst := service.log.Len()
	duplicate, created, err := service.StoreVaultRecord(message, wire.SettlementProof{}, now.Add(time.Minute))
	if err != nil || created || duplicate.Sequence != first.Sequence || duplicate.Receipt != first.Receipt || service.log.Len() != logSizeAfterFirst {
		t.Fatalf("idempotent StoreVaultRecord() = (%#v, %t, %v), log size %d", duplicate, created, err, service.log.Len())
	}
	if err := service.ReplaceVaultProof(messageID, wire.SettlementProof{LeafIndex: 7}); err != nil {
		t.Fatal(err)
	}
	refreshed, ok := service.VaultRecord(messageID)
	if !ok || refreshed.Sequence != first.Sequence || refreshed.Receipt != first.Receipt || refreshed.Proof.LeafIndex != 7 {
		t.Fatalf("refreshed record = (%#v, %t), want same sequence and receipt with refreshed proof", refreshed, ok)
	}
	if made, err := service.headTick(now.Add(time.Hour)); err != nil || !made {
		t.Fatalf("head tick = (%t, %v), want (true, nil)", made, err)
	}
	wantSize := service.log.Len()
	wantHead, ok := service.LatestHead()
	if !ok {
		t.Fatal("missing head before restart")
	}
	wantRoot, err := service.log.Root(wantSize)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if got := restarted.log.Len(); got != wantSize {
		t.Fatalf("replayed log size = %d, want %d", got, wantSize)
	}
	gotRoot, err := restarted.log.Root(wantSize)
	if err != nil || gotRoot != wantRoot {
		t.Fatalf("replayed log root = (%q, %v), want %q", gotRoot, err, wantRoot)
	}
	gotHead, ok := restarted.LatestHead()
	if !ok || gotHead != wantHead {
		t.Fatalf("replayed head = (%#v, %t), want %#v", gotHead, ok, wantHead)
	}
	gotRecord, ok := restarted.VaultRecord(messageID)
	if !ok || gotRecord.Sequence != 1 || gotRecord.Receipt != first.Receipt {
		t.Fatalf("replayed record = (%#v, %t), want sequence 1 and same receipt", gotRecord, ok)
	}
	byReceipt, ok := restarted.VaultRecordByReceiptID("settlement-receipt-1")
	if !ok || byReceipt.MessageID != messageID {
		t.Fatalf("settlement receipt lookup = (%#v, %t), want message %q", byReceipt, ok, messageID)
	}
	secondMessageID := "fy1:" + strings.Repeat("6", 64)
	second, created, err := restarted.StoreVaultRecord(vaultMessage(secondMessageID, "settlement-receipt-2"), wire.SettlementProof{}, now)
	if err != nil || !created || second.Sequence != 2 {
		t.Fatalf("next vault sequence = (%d, %t, %v), want (2, true, nil)", second.Sequence, created, err)
	}
}

func vaultMessage(messageID, receiptID string) []byte {
	return []byte(`{"message_id":"` + messageID + `","submission_receipt":{"receipt_id":"` + receiptID + `"}}`)
}

func TestRunStopsOnContextCancellation(t *testing.T) {
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	service, err := New(testConfig(t, &now))
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := service.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestSignAndLogRefusesSecondStatusForQuarter(t *testing.T) {
	now := time.Date(2027, 1, 2, 0, 0, 0, 0, time.UTC)
	service, err := New(testConfig(t, &now))
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	status := wire.VaultStatus{
		FoundationID: keys.DIDOf(service.cfg.Key), PeriodStart: "2026-10-01T00:00:00Z", PeriodEnd: "2027-01-01T00:00:00Z",
		RecordCount: 1, StoredBytes: 100, Funding: []wire.VaultFunding{{Network: "bitcoin-mainnet", ReceivedBaseUnits: "0", SpentBaseUnits: "0"}},
	}
	if _, _, err := service.SignAndLog(governance.LogKindVaultStatus, "vault-status", status); err != nil {
		t.Fatalf("first SignAndLog() error = %v", err)
	}
	status.RecordCount = 2
	if _, _, err := service.SignAndLog(governance.LogKindVaultStatus, "vault-status", status); err == nil {
		t.Fatal("SignAndLog() accepted a second vault status for the same period_end")
	}
	if got := service.log.Len(); got != 1 {
		t.Fatalf("log size = %d, want 1", got)
	}
}
