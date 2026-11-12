package faux

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestReorganization(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "faux.jsonl")
	l, err := Open(Config{Now: func() time.Time { return now }, JournalPath: path})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	alice := DevAddress(BitcoinMainnet, "alice")
	paid, err := l.Submit(ctx, SubmitRequest{Network: BitcoinMainnet, Recipient: alice, AmountChainUnits: "42", ClientRef: "pay-1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Orphan(ctx, BitcoinMainnet, paid.TxID); err != nil {
		t.Fatal(err)
	}
	t.Run("an orphaned transfer is not on the chain", func(t *testing.T) {
		if _, err := l.Transfer(ctx, BitcoinMainnet, paid.TxID); !errors.Is(err, ErrNotFound) {
			t.Errorf("Transfer = %v; want ErrNotFound", err)
		}
		listed, err := l.Transfers(ctx, Query{Network: BitcoinMainnet})
		if err != nil || len(listed) != 0 {
			t.Errorf("Transfers = %+v, %v; want none", listed, err)
		}
		totals, err := l.Address(ctx, BitcoinMainnet, alice)
		if err != nil || totals.Received != "0" {
			t.Errorf("Address received = %q, %v; want 0", totals.Received, err)
		}
	})
	t.Run("it cannot be orphaned twice", func(t *testing.T) {
		if err := l.Orphan(ctx, BitcoinMainnet, paid.TxID); !errors.Is(err, ErrNotFound) {
			t.Errorf("Orphan = %v; want ErrNotFound", err)
		}
	})
	t.Run("the same client_ref answers the unmined transfer", func(t *testing.T) {
		again, err := l.Submit(ctx, SubmitRequest{Network: BitcoinMainnet, Recipient: alice, AmountChainUnits: "42", ClientRef: "pay-1"})
		if err != nil || again.TxID != paid.TxID || again.Confirmations != 0 || again.Finalized || again.BlockHeight != 0 {
			t.Errorf("Submit = %+v, %v; want the transfer in no block", again, err)
		}
	})
	t.Run("orphaning survives a restart", func(t *testing.T) {
		reopened, err := Open(Config{Now: func() time.Time { return now }, JournalPath: path})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := reopened.Transfer(ctx, BitcoinMainnet, paid.TxID); !errors.Is(err, ErrNotFound) {
			t.Errorf("Transfer after reopen = %v; want ErrNotFound", err)
		}
	})
	now = now.Add(time.Hour)
	back, err := l.Reinclude(ctx, BitcoinMainnet, paid.TxID)
	t.Run("reinclusion mines it into a new final block", func(t *testing.T) {
		if err != nil {
			t.Fatal(err)
		}
		if back.TxID != paid.TxID || back.BlockHeight != 7 || back.BlockTime != "2026-11-11T13:00:00Z" || back.Confirmations != 6 || !back.Finalized {
			t.Errorf("Reinclude = %+v; want the same transfer at height 7, final", back)
		}
		got, err := l.Transfer(ctx, BitcoinMainnet, paid.TxID)
		if err != nil || got.BlockHeight != 7 || !got.Finalized {
			t.Errorf("Transfer = %+v, %v; want height 7, final", got, err)
		}
	})
	t.Run("a transfer on the chain cannot be reincluded", func(t *testing.T) {
		if _, err := l.Reinclude(ctx, BitcoinMainnet, paid.TxID); !errors.Is(err, ErrConflict) {
			t.Errorf("Reinclude = %v; want ErrConflict", err)
		}
	})
	t.Run("reinclusion survives a restart", func(t *testing.T) {
		reopened, err := Open(Config{Now: func() time.Time { return now }, JournalPath: path})
		if err != nil {
			t.Fatal(err)
		}
		got, err := reopened.Transfer(ctx, BitcoinMainnet, paid.TxID)
		if err != nil || got.BlockHeight != 7 || got.BlockTime != "2026-11-11T13:00:00Z" {
			t.Errorf("Transfer after reopen = %+v, %v; want height 7 at 13:00", got, err)
		}
	})
}

func TestReorganizationOverHTTP(t *testing.T) {
	l, err := Open(Config{Now: fixedClock()})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewHandler(l))
	defer server.Close()
	client := NewClient(server.URL, server.Client())
	ctx := context.Background()
	paid, err := client.Submit(ctx, SubmitRequest{Network: EthereumMainnet, Recipient: DevAddress(EthereumMainnet, "alice"), AmountChainUnits: "42"})
	if err != nil {
		t.Fatal(err)
	}
	t.Run("orphan", func(t *testing.T) {
		if err := client.Orphan(ctx, EthereumMainnet, paid.TxHash); err != nil {
			t.Fatal(err)
		}
		if _, err := client.Transfer(ctx, EthereumMainnet, paid.TxHash); !errors.Is(err, ErrNotFound) {
			t.Errorf("Transfer = %v; want ErrNotFound", err)
		}
	})
	t.Run("reinclude", func(t *testing.T) {
		back, err := client.Reinclude(ctx, EthereumMainnet, paid.TxHash)
		if err != nil || back.TxHash != paid.TxHash || back.BlockHeight != 3 || !back.Finalized {
			t.Errorf("Reinclude = %+v, %v; want the transfer at height 3, final", back, err)
		}
	})
	t.Run("an unknown transfer", func(t *testing.T) {
		if err := client.Orphan(ctx, EthereumMainnet, "00"+paid.TxHash[2:]); !errors.Is(err, ErrNotFound) {
			t.Errorf("Orphan = %v; want ErrNotFound", err)
		}
	})
}
