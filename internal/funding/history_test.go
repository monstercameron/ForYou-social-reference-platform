package funding

import (
	"errors"
	"testing"
	"time"

	"foryou.local/foryou/internal/faux"
)

type historyLine struct {
	sequence  uint64
	kind      HistoryKind
	amount    string
	reference string
}

func historyLines(t *testing.T, l *Ledger, after uint64) ([]historyLine, bool) {
	t.Helper()
	entries, more, err := l.History(author, faux.BitcoinMainnet, after)
	if err != nil {
		t.Fatal(err)
	}
	lines := make([]historyLine, len(entries))
	for i, e := range entries {
		lines[i] = historyLine{e.Sequence, e.Kind, e.Amount.String(), e.Reference}
	}
	return lines, more
}

func sameHistory(got, want []historyLine) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestAccountHistory(t *testing.T) {
	setup := func(t *testing.T) (*fixture, string, string) {
		t.Helper()
		f := newFixture(t)
		credit := f.fund(t, author, faux.BitcoinMainnet, "50000", "fund")
		admitted, created, err := f.admit("fy1:history", "quote-history", "136")
		if err != nil || !created {
			t.Fatalf("admit = %v, %v", created, err)
		}
		if _, _, err := f.ledger.RequestWithdrawal(withdrawalRequest("withdraw", "30000000")); err != nil {
			t.Fatal(err)
		}
		if _, err := f.ledger.MarkWithdrawalFailed(author, "withdraw"); err != nil {
			t.Fatal(err)
		}
		return f, credit.CreditID, admitted.Receipt.ReceiptID
	}
	t.Run("every balance change is one entry in commit order", func(t *testing.T) {
		f, creditID, receiptID := setup(t)
		got, more := historyLines(t, f.ledger, 0)
		want := []historyLine{
			{1, HistoryDepositCredit, "50000000", creditID},
			{2, HistoryDebit, "136", receiptID},
			{3, HistoryWithdrawal, "30000000", "withdraw"},
			{4, HistoryWithdrawalReturn, "30000000", "withdraw"},
		}
		if !sameHistory(got, want) || more {
			t.Errorf("history = %+v, more %v; want %+v and no more", got, more, want)
		}
		checkBalance(t, f.ledger, author, faux.BitcoinMainnet, 49_999_864, 0, 4)
	})
	t.Run("a restored fee is an entry naming its receipt", func(t *testing.T) {
		f, _, receiptID := setup(t)
		f.now = f.now.Add(31 * 24 * time.Hour)
		if restored, err := f.ledger.RestoreFee(author, "fy1:history", func() (bool, error) { return false, nil }); err != nil || !restored {
			t.Fatalf("RestoreFee = %v, %v", restored, err)
		}
		got, _ := historyLines(t, f.ledger, 4)
		if !sameHistory(got, []historyLine{{5, HistoryFeeRestore, "136", receiptID}}) {
			t.Errorf("entries after 4 = %+v; want the restore of 136 as entry 5", got)
		}
	})
	t.Run("a restart rebuilds the same history from the journal", func(t *testing.T) {
		f, _, _ := setup(t)
		before, _ := historyLines(t, f.ledger, 0)
		after, _ := historyLines(t, f.reopen(t), 0)
		if len(before) != 4 || !sameHistory(after, before) {
			t.Errorf("history after restart = %+v; want %+v", after, before)
		}
	})
	t.Run("after skips what the reader already holds", func(t *testing.T) {
		f, _, _ := setup(t)
		got, more := historyLines(t, f.ledger, 3)
		if !sameHistory(got, []historyLine{{4, HistoryWithdrawalReturn, "30000000", "withdraw"}}) || more {
			t.Errorf("entries after 3 = %+v, more %v; want only entry 4", got, more)
		}
		if got, more := historyLines(t, f.ledger, 4); len(got) != 0 || more {
			t.Errorf("entries after the last = %+v, more %v; want none", got, more)
		}
	})
	t.Run("another author's account stays empty", func(t *testing.T) {
		f, _, _ := setup(t)
		entries, more, err := f.ledger.History("did:key:z6MkOther", faux.BitcoinMainnet, 0)
		if err != nil || len(entries) != 0 || more {
			t.Errorf("other author's history = %+v, %v, %v; want empty", entries, more, err)
		}
	})
	t.Run("an unknown network is refused", func(t *testing.T) {
		f, _, _ := setup(t)
		if _, _, err := f.ledger.History(author, "dogecoin-mainnet", 0); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("History on an unknown network = %v; want ErrInvalidRequest", err)
		}
	})
}
