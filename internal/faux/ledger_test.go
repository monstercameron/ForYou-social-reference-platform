package faux

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func fixedClock() func() time.Time {
	return func() time.Time { return time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC) }
}

func TestLedgerSubmissions(t *testing.T) {
	l, err := Open(Config{Now: fixedClock()})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	btcReq := SubmitRequest{Network: BitcoinMainnet, Recipient: DevAddress(BitcoinMainnet, "alice"), AmountChainUnits: "42", Data: "001122", ClientRef: "send-1"}
	t.Run("btc_transfer_is_final_and_has_recipient_script", func(t *testing.T) {
		got, err := l.Submit(ctx, btcReq)
		if err != nil {
			t.Fatal(err)
		}
		if got.Confirmations != 6 || !got.Finalized || got.OutputIndex == nil || *got.OutputIndex != 0 || got.Script != "0014f7a526d6568e4335e9b1286cf5c9ff7d350a0a09" {
			t.Fatalf("transfer = %+v", got)
		}
	})
	t.Run("identical_client_ref_replay_adds_no_block", func(t *testing.T) {
		before, _ := l.Tip(ctx, BitcoinMainnet)
		got, err := l.Submit(ctx, btcReq)
		after, _ := l.Tip(ctx, BitcoinMainnet)
		if err != nil || got.Sequence != 1 || before != after {
			t.Fatalf("retry = %+v, %v; tips %+v %+v", got, err, before, after)
		}
	})
	t.Run("changed_client_ref_conflicts", func(t *testing.T) {
		changed := btcReq
		changed.AmountChainUnits = "43"
		if _, err := l.Submit(ctx, changed); !errors.Is(err, ErrConflict) {
			t.Fatalf("error = %v; want ErrConflict", err)
		}
	})
	t.Run("client_ref_is_global_across_networks", func(t *testing.T) {
		changed := btcReq
		changed.Network = EthereumMainnet
		changed.Recipient = DevAddress(EthereumMainnet, "alice")
		if _, err := l.Submit(ctx, changed); !errors.Is(err, ErrConflict) {
			t.Fatalf("error = %v; want ErrConflict", err)
		}
	})
	t.Run("pending_btc_finalizes_after_five_blocks", func(t *testing.T) {
		pending := SubmitRequest{Network: BitcoinMainnet, Recipient: btcReq.Recipient, AmountChainUnits: "7", Pending: true}
		got, err := l.Submit(ctx, pending)
		if err != nil || got.Confirmations != 1 || got.Finalized {
			t.Fatalf("pending = %+v, %v", got, err)
		}
		if _, err := l.Mine(ctx, BitcoinMainnet, 5); err != nil {
			t.Fatal(err)
		}
		got, err = l.Transfer(ctx, BitcoinMainnet, got.TxID)
		if err != nil || got.Confirmations != 6 || !got.Finalized {
			t.Fatalf("mined = %+v, %v", got, err)
		}
	})
	t.Run("ethereum_finalizes_at_two_confirmations", func(t *testing.T) {
		got, err := l.Submit(ctx, SubmitRequest{Network: EthereumMainnet, Recipient: DevAddress(EthereumMainnet, "alice"), AmountChainUnits: "1"})
		if err != nil || got.Confirmations != 2 || !got.Finalized || got.TxHash == "" || got.TxID != "" || got.Script != "" {
			t.Fatalf("ethereum = %+v, %v", got, err)
		}
		if _, err := l.Transfer(ctx, BitcoinMainnet, got.TxHash); !errors.Is(err, ErrNotFound) {
			t.Fatalf("cross-network transfer lookup error = %v; want ErrNotFound", err)
		}
	})
	t.Run("data_filter_and_validation", func(t *testing.T) {
		data := "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
		got, err := l.Submit(ctx, SubmitRequest{Network: BitcoinMainnet, Recipient: btcReq.Recipient, AmountChainUnits: "3", Data: data})
		if err != nil || got.Data != data {
			t.Fatalf("transfer = %+v, %v", got, err)
		}
		found, err := l.Transfers(ctx, Query{Network: BitcoinMainnet, Data: data})
		if err != nil || len(found) != 1 || found[0].Sequence != got.Sequence {
			t.Fatalf("data-filtered transfers = %+v, %v", found, err)
		}
		if _, err := l.Submit(ctx, SubmitRequest{Network: BitcoinMainnet, Recipient: btcReq.Recipient, AmountChainUnits: "4", Data: strings.Repeat("00", 81)}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("oversized data error = %v; want ErrInvalid", err)
		}
		if _, err := l.Submit(ctx, SubmitRequest{Network: BitcoinMainnet, Recipient: "bad", AmountChainUnits: "4"}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid recipient error = %v; want ErrInvalid", err)
		}
		if _, err := l.Submit(ctx, SubmitRequest{Network: BitcoinMainnet, Recipient: btcReq.Recipient, AmountChainUnits: "0"}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("zero amount error = %v; want ErrInvalid", err)
		}
	})
	t.Run("block_time_never_moves_backwards", func(t *testing.T) {
		clock := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
		ledger, err := Open(Config{Now: func() time.Time { return clock }})
		if err != nil {
			t.Fatal(err)
		}
		first, err := ledger.Mine(ctx, EthereumMainnet, 1)
		if err != nil {
			t.Fatal(err)
		}
		clock = clock.Add(-time.Hour)
		second, err := ledger.Mine(ctx, EthereumMainnet, 1)
		if err != nil || second.Time != first.Time {
			t.Fatalf("tip = %+v, %v; want time %q", second, err, first.Time)
		}
	})
}

func TestLedgerJournalReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "faux.jsonl")
	cfg := Config{Now: fixedClock(), JournalPath: path}
	l, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	firstRequest := SubmitRequest{Network: BitcoinMainnet, Recipient: DevAddress(BitcoinMainnet, "alice"), AmountChainUnits: "100", ClientRef: "journal"}
	if _, err := l.Submit(ctx, firstRequest); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Submit(ctx, SubmitRequest{Network: BitcoinMainnet, Recipient: DevAddress(BitcoinMainnet, "alice"), AmountChainUnits: "101", Pending: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Submit(ctx, SubmitRequest{Network: EthereumMainnet, Recipient: DevAddress(EthereumMainnet, "alice"), AmountChainUnits: "102"}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Mine(ctx, BitcoinMainnet, 3); err != nil {
		t.Fatal(err)
	}
	btcBefore, err := l.Transfers(ctx, Query{Network: BitcoinMainnet, Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	ethBefore, err := l.Transfers(ctx, Query{Network: EthereumMainnet, Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	btcTipBefore, _ := l.Tip(ctx, BitcoinMainnet)
	ethTipBefore, _ := l.Tip(ctx, EthereumMainnet)
	reopened, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	btcAfter, err := reopened.Transfers(ctx, Query{Network: BitcoinMainnet, Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	ethAfter, err := reopened.Transfers(ctx, Query{Network: EthereumMainnet, Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	btcTipAfter, _ := reopened.Tip(ctx, BitcoinMainnet)
	ethTipAfter, _ := reopened.Tip(ctx, EthereumMainnet)
	if !reflect.DeepEqual(btcAfter, btcBefore) || !reflect.DeepEqual(ethAfter, ethBefore) || btcTipAfter != btcTipBefore || ethTipAfter != ethTipBefore {
		t.Fatalf("replayed state differs: BTC %v / %v, ETH %v / %v", btcAfter, btcBefore, ethAfter, ethBefore)
	}
	tipBeforeReplay := btcTipAfter
	replay, err := reopened.Submit(ctx, firstRequest)
	tipAfterReplay, _ := reopened.Tip(ctx, BitcoinMainnet)
	if err != nil || replay.Sequence != 1 || tipBeforeReplay != tipAfterReplay {
		t.Fatalf("replay = %+v, %v; tips %+v %+v", replay, err, tipBeforeReplay, tipAfterReplay)
	}
}

func TestLedgerMutationFailures(t *testing.T) {
	t.Run("journal_failure_keeps_reads_and_blocks_mutations", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "faux")
		l, err := Open(Config{Now: fixedClock(), JournalPath: filepath.Join(dir, "faux.jsonl")})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(dir); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dir, nil, 0600); err != nil {
			t.Fatal(err)
		}
		_, err = l.Submit(context.Background(), SubmitRequest{Network: BitcoinMainnet, Recipient: DevAddress(BitcoinMainnet, "alice"), AmountChainUnits: "1"})
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("submit error = %v; want ErrUnavailable", err)
		}
		if _, err := l.Mine(context.Background(), BitcoinMainnet, 1); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("mine error = %v; want ErrUnavailable", err)
		}
		tip, err := l.Tip(context.Background(), BitcoinMainnet)
		if err != nil || tip.Height != 0 {
			t.Fatalf("tip = %+v, %v", tip, err)
		}
		items, err := l.Transfers(context.Background(), Query{Network: BitcoinMainnet})
		if err != nil || len(items) != 0 {
			t.Fatalf("transfers = %+v, %v", items, err)
		}
	})
	t.Run("transfer_limit", func(t *testing.T) {
		l, err := Open(Config{Now: fixedClock(), MaxTransfers: 1})
		if err != nil {
			t.Fatal(err)
		}
		request := SubmitRequest{Network: EthereumMainnet, Recipient: DevAddress(EthereumMainnet, "alice"), AmountChainUnits: "1"}
		if _, err := l.Submit(context.Background(), request); err != nil {
			t.Fatal(err)
		}
		request.AmountChainUnits = "2"
		if _, err := l.Submit(context.Background(), request); !errors.Is(err, ErrFull) {
			t.Fatalf("error = %v; want ErrFull", err)
		}
	})
	t.Run("journal_rejects_malformed_records", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "invalid.jsonl")
		if err := os.WriteFile(path, []byte("{invalid}\n{}\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(Config{JournalPath: path, Now: fixedClock()}); err == nil {
			t.Fatal("opened malformed journal")
		}
	})
}

func journalLinesWithThreeTransfers(t *testing.T, path string) [][]byte {
	t.Helper()
	ledger, err := Open(Config{JournalPath: path, Now: fixedClock()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Submit(context.Background(), SubmitRequest{Network: BitcoinMainnet, Recipient: DevAddress(BitcoinMainnet, "alice"), AmountChainUnits: "1", ClientRef: "first"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Submit(context.Background(), SubmitRequest{Network: BitcoinMainnet, Recipient: DevAddress(BitcoinMainnet, "alice"), AmountChainUnits: "2", ClientRef: "second"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Submit(context.Background(), SubmitRequest{Network: BitcoinMainnet, Recipient: DevAddress(BitcoinMainnet, "alice"), AmountChainUnits: "3", ClientRef: "torn-third"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Split(data[:len(data)-1], []byte{'\n'})
}

func TestLedgerJournalRecoversTornFinalRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "faux.jsonl")
	lines := journalLinesWithThreeTransfers(t, path)
	validPrefix := append(append(append([]byte{}, lines[0]...), '\n'), lines[1]...)
	validPrefix = append(validPrefix, '\n')
	torn := append(append([]byte{}, validPrefix...), lines[2][:len(lines[2])/2]...)
	if err := os.WriteFile(path, torn, 0600); err != nil {
		t.Fatal(err)
	}

	ledger, err := Open(Config{JournalPath: path, Now: fixedClock()})
	if err != nil {
		t.Fatal(err)
	}
	transfers, err := ledger.Transfers(context.Background(), Query{Network: BitcoinMainnet, Limit: 1000})
	if err != nil || len(transfers) != 2 {
		t.Fatalf("transfers = %+v, %v; want the first two records", transfers, err)
	}
	gotJournal, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(gotJournal, validPrefix) {
		t.Fatalf("journal after recovery = %q, %v; want %q", gotJournal, err, validPrefix)
	}

	retry, err := ledger.Submit(context.Background(), SubmitRequest{Network: BitcoinMainnet, Recipient: DevAddress(BitcoinMainnet, "alice"), AmountChainUnits: "3", ClientRef: "torn-third"})
	if err != nil || retry.Sequence != 3 {
		t.Fatalf("retry = %+v, %v; want a new third transfer", retry, err)
	}
}

func TestLedgerJournalRejectsDamagedMiddleRecordWithoutChangingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "faux.jsonl")
	lines := journalLinesWithThreeTransfers(t, path)
	damaged := append([]byte{}, lines[1]...)
	damaged[0] = '['
	contents := append(append(append(append([]byte{}, lines[0]...), '\n'), damaged...), '\n')
	contents = append(contents, lines[2]...)
	contents = append(contents, '\n')
	if err := os.WriteFile(path, contents, 0600); err != nil {
		t.Fatal(err)
	}

	if _, err := Open(Config{JournalPath: path, Now: fixedClock()}); err == nil {
		t.Fatal("opened journal with a damaged middle record")
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, contents) {
		t.Fatalf("journal after failed open = %q, %v; want unchanged %q", got, err, contents)
	}
}

func TestLedgerJournalAcceptsValidFinalRecordWithoutNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "faux.jsonl")
	lines := journalLinesWithThreeTransfers(t, path)
	if err := os.WriteFile(path, append([]byte{}, lines[0]...), 0600); err != nil {
		t.Fatal(err)
	}
	request := SubmitRequest{Network: BitcoinMainnet, Recipient: DevAddress(BitcoinMainnet, "alice"), AmountChainUnits: "4", ClientRef: "after-unterminated"}
	ledger, err := Open(Config{JournalPath: path, Now: fixedClock()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Submit(context.Background(), request); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(Config{JournalPath: path, Now: fixedClock()})
	if err != nil {
		t.Fatal(err)
	}
	transfers, err := reopened.Transfers(context.Background(), Query{Network: BitcoinMainnet, Limit: 1000})
	if err != nil || len(transfers) != 2 || transfers[1].ClientRef != request.ClientRef {
		t.Fatalf("replayed transfers = %+v, %v; want original and appended transfer", transfers, err)
	}
}
