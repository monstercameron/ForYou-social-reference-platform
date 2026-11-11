package foundation

import (
	"encoding/json"
	"math/big"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/distribution"
	"foryou.local/foryou/internal/governance"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

func TestVaultStatusWaitsForStatementsAndCountsQuarterEndRecords(t *testing.T) {
	now := time.Date(2027, 4, 15, 0, 0, 0, 0, time.UTC)
	service, err := New(testConfig(t, &now))
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	quarterEnd := time.Date(2027, 4, 1, 0, 0, 0, 0, time.UTC)

	first := vaultStatusTestMessage(t, "fy1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "receipt-a")
	second := vaultStatusTestMessage(t, "fy1:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "receipt-b")
	third := vaultStatusTestMessage(t, "fy1:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", "receipt-c")
	if _, _, err := service.StoreVaultRecord(first, wire.SettlementProof{}, quarterEnd.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.StoreVaultRecord(second, wire.SettlementProof{}, quarterEnd.Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.StoreVaultRecord(third, wire.SettlementProof{}, quarterEnd.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := service.vaultStatusPublish(quarterEnd); err != nil {
		t.Fatal(err)
	}
	if _, exists := service.VaultStatus(wire.FormatTime(quarterEnd)); exists {
		t.Fatal("vault status published before both network statements existed")
	}
	vaultStatusTestStatement(t, service, wire.BitcoinMainnet, quarterEnd)
	vaultStatusTestStatement(t, service, wire.EthereumMainnet, quarterEnd)
	if err := service.vaultStatusPublish(quarterEnd); err != nil {
		t.Fatal(err)
	}
	raw, exists := service.VaultStatus(wire.FormatTime(quarterEnd))
	if !exists {
		t.Fatal("vault status was not published after both statements")
	}
	var status wire.VaultStatus
	if err := json.Unmarshal(raw, &status); err != nil {
		t.Fatal(err)
	}
	if status.RecordCount != 2 || status.StoredBytes != int64(len(first)+len(second)) {
		t.Fatalf("vault record count/bytes = %d/%d, want 2/%d", status.RecordCount, status.StoredBytes, len(first)+len(second))
	}
	if len(status.Funding) != 2 || status.Funding[0].Network != wire.BitcoinMainnet || status.Funding[0].ReceivedBaseUnits != "9500000" || status.Funding[0].SpentBaseUnits != "0" || status.Funding[1].ReceivedBaseUnits != "0" {
		t.Fatalf("vault funding = %+v, want 9,500,000 kept bitcoin base units and zero ethereum", status.Funding)
	}
	signedAt, err := wire.ParseTime(status.PeriodEnd)
	if err != nil || service.Trust().VerifyFoundation(status, signedAt, "vault-status", nil) != nil {
		t.Fatal("published vault status did not verify at period_end")
	}
}

func vaultStatusTestMessage(t *testing.T, messageID, receiptID string) []byte {
	t.Helper()
	data, err := canon.Encode(map[string]any{"message_id": messageID, "submission_receipt": map[string]any{"receipt_id": receiptID}})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func vaultStatusTestStatement(t *testing.T, service *Service, network string, end time.Time) {
	t.Helper()
	policy := service.Policies()[0]
	start := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	seats := map[string]distribution.SeatEvidence{
		policy.Monitors[0]: {Counted: 1, Witnessed: 1},
		policy.Monitors[1]: {Counted: 1, Witnessed: 1},
		policy.Monitors[2]: {Counted: 1, Witnessed: 1},
		policy.Monitors[3]: {Counted: 1, Witnessed: 1},
		policy.Monitors[4]: {Counted: 1, Witnessed: 1},
	}
	providers := []distribution.ProviderInput{}
	monitorTransfers := map[string]wire.DepositTransfer{}
	if network == wire.BitcoinMainnet {
		providers = []distribution.ProviderInput{{ChainID: keys.DIDOf(devnet.ProviderKey(0)), SettledFees: big.NewInt(10_000_000), DeclaredSeconds: []int64{10 * 31536000}}}
		outputIndex := int64(0)
		monitorTransfers[policy.Monitors[0]] = wire.DepositTransfer{Network: network, TxID: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd", OutputIndex: &outputIndex}
		monitorTransfers[policy.Monitors[1]] = wire.DepositTransfer{Network: network, TxID: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", OutputIndex: &outputIndex}
		monitorTransfers[policy.Monitors[2]] = wire.DepositTransfer{Network: network, TxID: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", OutputIndex: &outputIndex}
		monitorTransfers[policy.Monitors[3]] = wire.DepositTransfer{Network: network, TxID: "1111111111111111111111111111111111111111111111111111111111111111", OutputIndex: &outputIndex}
		monitorTransfers[policy.Monitors[4]] = wire.DepositTransfer{Network: network, TxID: "2222222222222222222222222222222222222222222222222222222222222222", OutputIndex: &outputIndex}
	} else {
		seats = map[string]distribution.SeatEvidence{policy.Monitors[0]: {}, policy.Monitors[1]: {}, policy.Monitors[2]: {}, policy.Monitors[3]: {}, policy.Monitors[4]: {}}
	}
	plan, err := distribution.Compute(distribution.Input{Policy: policy, Network: network, Start: start, End: end, Day: 24 * time.Hour, CutOff: end.Add(14 * 24 * time.Hour), Seats: seats, Providers: providers})
	if err != nil {
		t.Fatal(err)
	}
	statement, err := plan.Statement(keys.DIDOf(service.cfg.Key), map[string]wire.DepositTransfer{}, monitorTransfers)
	if err != nil {
		t.Fatal(err)
	}
	kind := governance.LogKindStatement
	if _, _, err := service.SignAndLog(kind, "distribution-statement", statement); err != nil {
		t.Fatal(err)
	}
}
