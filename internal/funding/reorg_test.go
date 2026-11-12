package funding

import (
	"context"
	"errors"
	"math/big"
	"reflect"
	"testing"
	"time"

	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/wire"
)

func checkFrozen(t *testing.T, l *Ledger, available, frozen int64) {
	t.Helper()
	b, err := l.Balance(author, faux.BitcoinMainnet)
	if err != nil {
		t.Fatal(err)
	}
	if b.Available.Cmp(big.NewInt(available)) != 0 || b.Frozen.Cmp(big.NewInt(frozen)) != 0 {
		t.Errorf("Balance = available %s frozen %s; want available %d frozen %d", b.Available, b.Frozen, available, frozen)
	}
}

// reorgFixture credits 50,000 sat, spends 136 msat of it and returns the proof of
// the credited transfer.
func reorgFixture(t *testing.T) (*fixture, wire.DepositProof, wire.DepositCredit, string) {
	t.Helper()
	f := newFixture(t)
	inv := f.invoice(t, author, faux.BitcoinMainnet, "50000", "fund")
	proof := f.pay(t, inv, inv.Recipient, "50000", false)
	credit, outcome, err := f.observe(proof)
	if err != nil || outcome != Created {
		t.Fatalf("Observe = %v, %v; want Created", outcome, err)
	}
	admitted, created, err := f.admit("fy1:first", "quote-1", "136")
	if err != nil || !created {
		t.Fatalf("admit = %v, %v", created, err)
	}
	return f, proof, credit, admitted.Receipt.ReceiptID
}

func (f *fixture) orphan(t *testing.T, p wire.DepositProof) {
	t.Helper()
	if err := f.chain.Orphan(context.Background(), p.Transfer.Network, p.Transfer.TxID); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) reconcile(t *testing.T) {
	t.Helper()
	if err := f.ledger.Reconcile(context.Background(), f.chain); err != nil {
		t.Fatal(err)
	}
}

func TestReorganizedCreditIsFrozen(t *testing.T) {
	f, proof, _, receiptID := reorgFixture(t)
	f.orphan(t, proof)
	f.reconcile(t)
	t.Run("the unspent part is frozen", func(t *testing.T) {
		checkFrozen(t, f.ledger, 0, 49_999_864)
	})
	t.Run("a new admission is refused", func(t *testing.T) {
		if _, _, err := f.admit("fy1:second", "quote-2", "136"); !errors.Is(err, ErrInsufficientBalance) {
			t.Errorf("admit = %v; want ErrInsufficientBalance", err)
		}
	})
	t.Run("the retry of an accepted admission still succeeds", func(t *testing.T) {
		got, created, err := f.admit("fy1:first", "quote-1", "136")
		if err != nil || created || got.Receipt.ReceiptID != receiptID {
			t.Errorf("retry = receipt %s, created %v, %v; want the original receipt", got.Receipt.ReceiptID, created, err)
		}
	})
	t.Run("a withdrawal cannot draw on frozen funds", func(t *testing.T) {
		if _, _, err := f.ledger.RequestWithdrawal(withdrawalRequest("withdraw", "1000")); !errors.Is(err, ErrInsufficientBalance) {
			t.Errorf("RequestWithdrawal = %v; want ErrInsufficientBalance", err)
		}
	})
	t.Run("freezing is not a history entry", func(t *testing.T) {
		if got, _ := historyLines(t, f.ledger, 0); len(got) != 2 {
			t.Errorf("history = %+v; want only the credit and the debit", got)
		}
	})
	t.Run("checking again changes nothing", func(t *testing.T) {
		size := journalSize(t, f.path)
		f.reconcile(t)
		if got := journalSize(t, f.path); got != size {
			t.Errorf("journal grew from %d to %d bytes", size, got)
		}
	})
	t.Run("the freeze survives a restart", func(t *testing.T) {
		checkFrozen(t, f.reopen(t), 0, 49_999_864)
	})
	if _, err := f.chain.Reinclude(context.Background(), faux.BitcoinMainnet, proof.Transfer.TxID); err != nil {
		t.Fatal(err)
	}
	f.reconcile(t)
	t.Run("a transfer that is final again releases the funds once", func(t *testing.T) {
		checkFrozen(t, f.ledger, 49_999_864, 0)
		f.reconcile(t)
		checkFrozen(t, f.ledger, 49_999_864, 0)
		if got, _ := historyLines(t, f.ledger, 0); len(got) != 2 {
			t.Errorf("history = %+v; want only the credit and the debit", got)
		}
	})
}

func TestMissingCreditIsReversedAfterTheDelay(t *testing.T) {
	f, proof, credit, receiptID := reorgFixture(t)
	f.orphan(t, proof)
	f.reconcile(t)
	f.now = start.Add(24*time.Hour - time.Second)
	f.reconcile(t)
	t.Run("it stays frozen before the delay", func(t *testing.T) {
		checkFrozen(t, f.ledger, 0, 49_999_864)
	})
	f.now = start.Add(24 * time.Hour)
	f.reconcile(t)
	reversed := []historyLine{
		{1, HistoryDepositCredit, "50000000", credit.CreditID},
		{2, HistoryDebit, "136", receiptID},
		{3, HistoryDepositReversal, "49999864", credit.CreditID},
	}
	t.Run("the frozen part leaves the balance as a reversal entry", func(t *testing.T) {
		checkFrozen(t, f.ledger, 0, 0)
		if got, _ := historyLines(t, f.ledger, 0); !sameHistory(got, reversed) {
			t.Errorf("history = %+v; want %+v", got, reversed)
		}
	})
	t.Run("the reversal survives a restart", func(t *testing.T) {
		l := f.reopen(t)
		checkFrozen(t, l, 0, 0)
		if got, _ := historyLines(t, l, 0); !sameHistory(got, reversed) {
			t.Errorf("history after restart = %+v; want %+v", got, reversed)
		}
	})
	t.Run("the author's retry returns the original credit while it is missing", func(t *testing.T) {
		got, outcome, err := f.observe(proof)
		if err != nil || outcome != Existing || !reflect.DeepEqual(got, credit) {
			t.Errorf("Observe = %+v, %v, %v; want the original credit, Existing", got, outcome, err)
		}
		checkFrozen(t, f.ledger, 0, 0)
	})
	if _, err := f.chain.Reinclude(context.Background(), faux.BitcoinMainnet, proof.Transfer.TxID); err != nil {
		t.Fatal(err)
	}
	t.Run("a transfer mined again restores the original credit and no second one", func(t *testing.T) {
		got, outcome, err := f.observe(proof)
		if err != nil || outcome != Existing || !reflect.DeepEqual(got, credit) {
			t.Fatalf("Observe = %+v, %v, %v; want the original credit, Existing", got, outcome, err)
		}
		if _, outcome, err := f.observe(proof); err != nil || outcome != Existing {
			t.Fatalf("second Observe = %v, %v; want Existing", outcome, err)
		}
		checkFrozen(t, f.ledger, 49_999_864, 0)
		restored := append(append([]historyLine(nil), reversed...), historyLine{4, HistoryDepositCredit, "49999864", credit.CreditID})
		if got, _ := historyLines(t, f.ledger, 0); !sameHistory(got, restored) {
			t.Errorf("history = %+v; want %+v", got, restored)
		}
	})
}

func TestReorganizationNeverMakesABalanceNegative(t *testing.T) {
	t.Run("a spent credit freezes nothing and the provider carries it", func(t *testing.T) {
		f := newFixture(t)
		inv := f.invoice(t, author, faux.BitcoinMainnet, "1000", "fund")
		proof := f.pay(t, inv, inv.Recipient, "1000", false)
		if _, outcome, err := f.observe(proof); err != nil || outcome != Created {
			t.Fatalf("Observe = %v, %v; want Created", outcome, err)
		}
		if _, _, err := f.admit("fy1:all", "quote-all", "1000000"); err != nil {
			t.Fatal(err)
		}
		f.orphan(t, proof)
		f.reconcile(t)
		checkFrozen(t, f.ledger, 0, 0)
		f.now = start.Add(24 * time.Hour)
		f.reconcile(t)
		checkFrozen(t, f.ledger, 0, 0)
		if got, _ := historyLines(t, f.ledger, 0); len(got) != 2 {
			t.Errorf("history = %+v; want only the credit and the debit", got)
		}
	})
	t.Run("only the affected credit's amount is frozen", func(t *testing.T) {
		f := newFixture(t)
		inv := f.invoice(t, author, faux.BitcoinMainnet, "1000", "first")
		proof := f.pay(t, inv, inv.Recipient, "1000", false)
		if _, outcome, err := f.observe(proof); err != nil || outcome != Created {
			t.Fatalf("Observe = %v, %v; want Created", outcome, err)
		}
		f.fund(t, author, faux.BitcoinMainnet, "2000", "second")
		if _, _, err := f.admit("fy1:half", "quote-half", "1500000"); err != nil {
			t.Fatal(err)
		}
		f.orphan(t, proof)
		f.reconcile(t)
		checkFrozen(t, f.ledger, 500_000, 1_000_000)
	})
}

func TestRetryRechecksAWatchedCredit(t *testing.T) {
	f, proof, credit, _ := reorgFixture(t)
	f.orphan(t, proof)
	got, outcome, err := f.observe(proof)
	t.Run("the retry still returns the original credit", func(t *testing.T) {
		if err != nil || outcome != Existing || !reflect.DeepEqual(got, credit) {
			t.Errorf("Observe = %+v, %v, %v; want the original credit, Existing", got, outcome, err)
		}
	})
	t.Run("the missing transfer is frozen without waiting for the loop", func(t *testing.T) {
		checkFrozen(t, f.ledger, 0, 49_999_864)
	})
}

type unreadableChain struct{ faux.Chain }

func (unreadableChain) Transfer(context.Context, string, string) (faux.Transfer, error) {
	return faux.Transfer{}, errors.New("chain unreachable")
}

func TestReconcileLimits(t *testing.T) {
	t.Run("an unreadable chain freezes nothing", func(t *testing.T) {
		f, _, _, _ := reorgFixture(t)
		if err := f.ledger.Reconcile(context.Background(), unreadableChain{f.chain}); err == nil {
			t.Error("Reconcile = nil; want the chain error")
		}
		checkFrozen(t, f.ledger, 49_999_864, 0)
	})
	t.Run("a credit past the watch window is not re-checked", func(t *testing.T) {
		f, proof, _, _ := reorgFixture(t)
		f.now = start.Add(24 * time.Hour)
		f.orphan(t, proof)
		f.reconcile(t)
		checkFrozen(t, f.ledger, 49_999_864, 0)
	})
}

func TestDroppedPendingTransfer(t *testing.T) {
	issue := func(f *fixture, amount, requestID string) error {
		_, _, err := f.ledger.IssueInvoice(depositRequest(author, faux.BitcoinMainnet, amount, requestID), btcCap, testSealer{})
		return err
	}
	f := newFixture(t)
	held := f.invoice(t, author, faux.BitcoinMainnet, "100000", "held")
	proof := f.pay(t, held, held.Recipient, "100000", true)
	if _, outcome, err := f.observe(proof); err != nil || outcome != Pending {
		t.Fatalf("Observe = %v, %v; want Pending", outcome, err)
	}
	f.now = start.Add(24*time.Hour + time.Second)
	t.Run("a pending transfer counts toward custody after expiry", func(t *testing.T) {
		if err := issue(f, "36001", "over-cap"); !errors.Is(err, ErrCustodyCap) {
			t.Errorf("36001 sat = %v; want ErrCustodyCap", err)
		}
	})
	f.orphan(t, proof)
	f.reconcile(t)
	t.Run("a transfer the chain dropped no longer counts", func(t *testing.T) {
		if err := issue(f, "36001", "under-cap"); err != nil {
			t.Errorf("36001 sat = %v; want an invoice", err)
		}
	})
	t.Run("the drop survives a restart", func(t *testing.T) {
		f.ledger = f.reopen(t)
		if err := issue(f, "99999", "still-under-cap"); err != nil {
			t.Errorf("99999 sat = %v; want an invoice", err)
		}
	})
	if _, err := f.chain.Reinclude(context.Background(), faux.BitcoinMainnet, proof.Transfer.TxID); err != nil {
		t.Fatal(err)
	}
	t.Run("mined after expiry it is a late claim, never forfeited", func(t *testing.T) {
		if _, _, err := f.observe(proof); !errors.Is(err, ErrClaim) {
			t.Fatalf("Observe = %v; want ErrClaim", err)
		}
		claims := f.ledger.Claims(author)
		if len(claims) != 1 || claims[0].Reason != ClaimLate || claims[0].AmountChainUnits != "100000" {
			t.Errorf("Claims = %+v; want one late claim for 100000", claims)
		}
	})
}
