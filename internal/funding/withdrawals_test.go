package funding

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/wire"
)

func withdrawalRequest(requestID, amount string) wire.WithdrawalRequest {
	return wire.WithdrawalRequest{ProviderID: testProvider, AuthorID: author, Network: faux.BitcoinMainnet, AmountBaseUnits: amount, Recipient: faux.DevAddress(faux.BitcoinMainnet, "payout"), RequestID: requestID}
}

func payout(index int64) wire.DepositTransfer {
	return wire.DepositTransfer{Network: faux.BitcoinMainnet, TxID: strings.Repeat("cd", 32), OutputIndex: &index}
}

func TestWithdrawals(t *testing.T) {
	setup := func(t *testing.T) *fixture {
		t.Helper()
		f := newFixture(t)
		f.fund(t, author, faux.BitcoinMainnet, "50000", "fund")
		w, created, err := f.ledger.RequestWithdrawal(withdrawalRequest("withdraw", "30000000"))
		if err != nil || !created || w.Status != WithdrawalPending || w.Transfer != nil {
			t.Fatalf("RequestWithdrawal = %+v, %v, %v; want a new pending withdrawal", w, created, err)
		}
		checkBalance(t, f.ledger, author, faux.BitcoinMainnet, 20_000_000, 30_000_000, 2)
		return f
	}
	t.Run("failed restores once", func(t *testing.T) {
		f := setup(t)
		if _, err := f.ledger.MarkWithdrawalFailed(author, "withdraw"); err != nil {
			t.Fatal(err)
		}
		w, err := f.ledger.MarkWithdrawalFailed(author, "withdraw")
		if err != nil || w.Status != WithdrawalFailed {
			t.Fatalf("repeated MarkWithdrawalFailed = %+v, %v", w, err)
		}
		checkBalance(t, f.ledger, author, faux.BitcoinMainnet, 50_000_000, 0, 3)
		if _, err := f.ledger.MarkWithdrawalSent(author, "withdraw", payout(0)); !errors.Is(err, ErrConflict) {
			t.Errorf("MarkWithdrawalSent after failed = %v; want ErrConflict", err)
		}
	})
	t.Run("sent removes the reserve", func(t *testing.T) {
		f := setup(t)
		w, err := f.ledger.MarkWithdrawalSent(author, "withdraw", payout(0))
		if err != nil || w.Status != WithdrawalSent || w.Transfer == nil || !sameTransfer(*w.Transfer, payout(0)) {
			t.Fatalf("MarkWithdrawalSent = %+v, %v", w, err)
		}
		checkBalance(t, f.ledger, author, faux.BitcoinMainnet, 20_000_000, 0, 3)
		if again, err := f.ledger.MarkWithdrawalSent(author, "withdraw", payout(0)); err != nil || !reflect.DeepEqual(again, w) {
			t.Errorf("repeated MarkWithdrawalSent = %+v, %v; want the same record", again, err)
		}
		if _, err := f.ledger.MarkWithdrawalSent(author, "withdraw", payout(1)); !errors.Is(err, ErrConflict) {
			t.Errorf("MarkWithdrawalSent with another transfer = %v; want ErrConflict", err)
		}
		if _, err := f.ledger.MarkWithdrawalFailed(author, "withdraw"); !errors.Is(err, ErrConflict) {
			t.Errorf("MarkWithdrawalFailed after sent = %v; want ErrConflict", err)
		}
		checkBalance(t, f.ledger, author, faux.BitcoinMainnet, 20_000_000, 0, 3)
	})
	t.Run("requests", func(t *testing.T) {
		f := setup(t)
		if _, _, err := f.ledger.RequestWithdrawal(withdrawalRequest("too-large", "20000001")); !errors.Is(err, ErrInsufficientBalance) {
			t.Errorf("larger withdrawal = %v; want ErrInsufficientBalance", err)
		}
		if _, _, err := f.ledger.RequestWithdrawal(withdrawalRequest("withdraw", "1000")); !errors.Is(err, ErrRequestConflict) {
			t.Errorf("same request ID, other amount = %v; want ErrRequestConflict", err)
		}
		w, created, err := f.ledger.RequestWithdrawal(withdrawalRequest("withdraw", "30000000"))
		if err != nil || created || w.Status != WithdrawalPending {
			t.Errorf("identical retry = %+v, %v, %v; want the original, not created", w, created, err)
		}
		checkBalance(t, f.ledger, author, faux.BitcoinMainnet, 20_000_000, 30_000_000, 2)
	})
	t.Run("pending list", func(t *testing.T) {
		f := setup(t)
		if _, _, err := f.ledger.RequestWithdrawal(withdrawalRequest("second", "1000")); err != nil {
			t.Fatal(err)
		}
		if _, err := f.ledger.MarkWithdrawalFailed(author, "withdraw"); err != nil {
			t.Fatal(err)
		}
		got := f.ledger.PendingWithdrawals()
		if len(got) != 1 || got[0].Request.RequestID != "second" {
			t.Errorf("PendingWithdrawals = %+v; want only second", got)
		}
	})
}
