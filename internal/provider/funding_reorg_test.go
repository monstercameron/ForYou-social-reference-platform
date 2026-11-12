package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

func TestFundingReorganization(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	cfg, err := TestConfig(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	cfg.ReversalDelay = time.Hour
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	author := fdTestAuthor(t)
	authorID := keys.DIDOf(author)
	const remote = "192.0.2.10:1234"
	request := wire.DepositRequest{ProviderID: s.ProviderID(), AuthorID: authorID, Network: wire.BitcoinMainnet, AmountChainUnits: "50000", RequestID: keys.EncodeB64URL(bytes.Repeat([]byte{7}, 32)), NotAfter: wire.FormatTime(now.Add(time.Minute))}
	var invoice wire.DepositInvoice
	if got := fdTestServe(t, s, author, http.MethodPost, "/v1/deposits", fdTestJSON(t, request), true, remote); got.Code != http.StatusCreated || json.Unmarshal(got.Body.Bytes(), &invoice) != nil {
		t.Fatalf("POST /v1/deposits = %d %s", got.Code, got.Body)
	}
	chain := s.Chain.(*faux.Ledger)
	transfer, err := chain.Submit(context.Background(), faux.SubmitRequest{Network: request.Network, Recipient: invoice.Recipient, AmountChainUnits: request.AmountChainUnits})
	if err != nil {
		t.Fatal(err)
	}
	proof := wire.DepositProof{ProviderID: s.ProviderID(), AuthorID: authorID, DepositID: invoice.DepositID, Transfer: wire.DepositTransfer{Network: transfer.Network, TxID: transfer.TxID, OutputIndex: fdTestOutputIndex(transfer.OutputIndex)}}
	var credit wire.DepositCredit
	if got := fdTestServe(t, s, author, http.MethodPost, "/v1/deposits/observe", fdTestJSON(t, proof), true, remote); got.Code != http.StatusCreated || json.Unmarshal(got.Body.Bytes(), &credit) != nil {
		t.Fatalf("POST /v1/deposits/observe = %d %s", got.Code, got.Body)
	}
	balance := func(t *testing.T) wire.BalanceStatus {
		t.Helper()
		var status wire.BalanceStatus
		got := fdTestServe(t, s, author, http.MethodGet, "/v1/balances/bitcoin-mainnet", nil, true, remote)
		if got.Code != http.StatusOK || json.Unmarshal(got.Body.Bytes(), &status) != nil {
			t.Fatalf("GET /v1/balances = %d %s", got.Code, got.Body)
		}
		return status
	}
	if err := chain.Orphan(context.Background(), transfer.Network, transfer.TxID); err != nil {
		t.Fatal(err)
	}
	if err := s.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Run("the signed balance shows the deposit frozen", func(t *testing.T) {
		if got := balance(t); got.Available != "0" || got.Frozen != "50000000" {
			t.Errorf("balance = available %s frozen %s; want available 0 frozen 50000000", got.Available, got.Frozen)
		}
	})
	now = now.Add(time.Hour)
	if err := s.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Run("after the delay the signed history shows the reversal", func(t *testing.T) {
		if got := balance(t); got.Available != "0" || got.Frozen != "0" {
			t.Errorf("balance = available %s frozen %s; want both 0", got.Available, got.Frozen)
		}
		var page wire.AccountHistoryPage
		got := fdTestServe(t, s, author, http.MethodGet, "/v1/account-history/bitcoin-mainnet/0", nil, true, remote)
		if got.Code != http.StatusOK || json.Unmarshal(got.Body.Bytes(), &page) != nil {
			t.Fatalf("GET /v1/account-history = %d %s", got.Code, got.Body)
		}
		if len(page.Entries) != 2 {
			t.Fatalf("entries = %+v; want the credit and its reversal", page.Entries)
		}
		reversal := page.Entries[1]
		if reversal.Sequence != 2 || reversal.Kind != "deposit_reversal" || reversal.AmountBaseUnits != "50000000" || reversal.Reference != credit.CreditID {
			t.Errorf("entry 2 = %+v; want a deposit_reversal of 50000000 naming %s", reversal, credit.CreditID)
		}
	})
}
