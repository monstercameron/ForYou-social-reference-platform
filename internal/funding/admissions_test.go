package funding

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/wire"
)

func TestOneDepositPaysForSeveralPosts(t *testing.T) {
	f := newFixture(t)
	f.fund(t, author, faux.BitcoinMainnet, "50000", "fund")
	checkBalance(t, f.ledger, author, faux.BitcoinMainnet, 50_000_000, 0, 1)
	first, created, err := f.admit("message-1", "quote-1", "136")
	if err != nil || !created {
		t.Fatalf("first Admit = %v, %v", created, err)
	}
	if _, created, err := f.admit("message-2", "quote-2", "272"); err != nil || !created {
		t.Fatalf("second Admit = %v, %v", created, err)
	}
	checkBalance(t, f.ledger, author, faux.BitcoinMainnet, 49_999_592, 0, 3)
	t.Run("receipt", func(t *testing.T) {
		want := wire.SubmissionReceipt{ProviderID: testProvider, AuthorID: author, MessageID: "message-1", ReceiptID: first.Receipt.ReceiptID, QuoteID: "quote-1", ScheduleID: "fyfs2:schedule", Network: faux.BitcoinMainnet, AmountBaseUnits: "136", AuthorizationAt: "2026-11-11T00:00:00Z", IssuedAt: "2026-11-11T00:00:00Z", ProviderSignature: "test-signature"}
		if first.Receipt != want || len(first.Receipt.ReceiptID) != 43 {
			t.Errorf("receipt = %+v; want %+v with a 32-byte base64url receipt ID", first.Receipt, want)
		}
		if first.Request != draft("message-1", "quote-1", "136").Request {
			t.Errorf("request = %+v; want the admitted request", first.Request)
		}
	})
	t.Run("a quote bound to another message conflicts", func(t *testing.T) {
		d := draft("message-3", "quote-1", "136")
		d.Quote.MessageID = "message-1"
		if _, _, err := f.ledger.Admit(context.Background(), d, allow, sealReceipt); !errors.Is(err, ErrQuoteConflict) {
			t.Errorf("Admit = %v; want ErrQuoteConflict", err)
		}
		if _, _, err := f.admit("message-3", "quote-1", "136"); !errors.Is(err, ErrQuoteConflict) {
			t.Errorf("Admit with a used quote = %v; want ErrQuoteConflict", err)
		}
		checkBalance(t, f.ledger, author, faux.BitcoinMainnet, 49_999_592, 0, 3)
	})
	t.Run("receipts in commit order", func(t *testing.T) {
		got := f.ledger.Receipts(faux.BitcoinMainnet, 1, 10)
		if len(got) != 1 || got[0].Sequence != 2 || got[0].Receipt.MessageID != "message-2" {
			t.Errorf("Receipts after 1 = %+v; want message-2 at sequence 2", got)
		}
		if got := f.ledger.Receipts(faux.BitcoinMainnet, 0, 1); len(got) != 1 || got[0].Sequence != 1 {
			t.Errorf("Receipts limit 1 = %+v; want sequence 1 only", got)
		}
	})
}

func TestCommittedAdmissionRetryPrecedesNewWorkChecks(t *testing.T) {
	f := newFixture(t)
	f.fund(t, author, faux.BitcoinMainnet, "50000", "fund")
	original, _, err := f.admit("message", "quote-1", "136")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	refuse := func(time.Time) error {
		calls++
		return errors.New("delegation revoked")
	}
	seal := func(r wire.SubmissionReceipt) (wire.SubmissionReceipt, error) {
		calls++
		return sealReceipt(r)
	}
	f.now = start.Add(2 * time.Hour)
	got, created, err := f.ledger.Admit(context.Background(), draft("message", "quote-2", "272"), refuse, seal)
	if err != nil || created || !reflect.DeepEqual(got, original) {
		t.Errorf("retry = %+v, %v, %v; want the original record, not created", got, created, err)
	}
	if calls != 0 {
		t.Errorf("check and seal called %d times; want 0", calls)
	}
	checkBalance(t, f.ledger, author, faux.BitcoinMainnet, 49_999_864, 0, 2)
}

func TestFailedAdmissionCreatesNoDebit(t *testing.T) {
	f := newFixture(t)
	f.fund(t, author, faux.BitcoinMainnet, "50000", "fund")
	unchanged := func(t *testing.T) {
		t.Helper()
		checkBalance(t, f.ledger, author, faux.BitcoinMainnet, 50_000_000, 0, 1)
		if got := f.ledger.Receipts(faux.BitcoinMainnet, 0, 10); len(got) != 0 {
			t.Errorf("Receipts = %+v; want none", got)
		}
	}
	t.Run("check refuses", func(t *testing.T) {
		revoked := errors.New("delegation revoked")
		_, _, err := f.ledger.Admit(context.Background(), draft("message", "quote", "136"), func(time.Time) error { return revoked }, sealReceipt)
		if !errors.Is(err, revoked) {
			t.Errorf("Admit = %v; want the check error", err)
		}
		unchanged(t)
	})
	t.Run("seal refuses", func(t *testing.T) {
		tooLarge := errors.New("message budget exceeded")
		_, _, err := f.ledger.Admit(context.Background(), draft("message", "quote", "136"), allow, func(wire.SubmissionReceipt) (wire.SubmissionReceipt, error) {
			return wire.SubmissionReceipt{}, tooLarge
		})
		if !errors.Is(err, tooLarge) {
			t.Errorf("Admit = %v; want the seal error", err)
		}
		unchanged(t)
	})
	t.Run("seal changes the amount", func(t *testing.T) {
		_, _, err := f.ledger.Admit(context.Background(), draft("message", "quote", "136"), allow, func(r wire.SubmissionReceipt) (wire.SubmissionReceipt, error) {
			r.AmountBaseUnits = "1"
			return sealReceipt(r)
		})
		if !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("Admit = %v; want ErrInvalidRequest", err)
		}
		unchanged(t)
	})
	t.Run("insufficient balance", func(t *testing.T) {
		if _, _, err := f.admit("message", "quote", "50000001"); !errors.Is(err, ErrInsufficientBalance) {
			t.Errorf("Admit = %v; want ErrInsufficientBalance", err)
		}
		unchanged(t)
	})
	t.Run("authorization time too far from now", func(t *testing.T) {
		d := draft("message", "quote", "136")
		d.At = start.Add(61 * time.Second)
		if _, _, err := f.ledger.Admit(context.Background(), d, allow, sealReceipt); !errors.Is(err, ErrUnavailable) {
			t.Errorf("Admit = %v; want ErrUnavailable", err)
		}
		unchanged(t)
	})
	t.Run("the quote is still usable", func(t *testing.T) {
		if _, created, err := f.admit("message", "quote", "136"); err != nil || !created {
			t.Errorf("Admit = %v, %v; want created", created, err)
		}
	})
}

func TestAssetsNeverCross(t *testing.T) {
	f := newFixture(t)
	f.fund(t, author, faux.EthereumMainnet, "48900000000000", "eth")
	if _, _, err := f.admit("message", "quote", "136"); !errors.Is(err, ErrInsufficientBalance) {
		t.Errorf("BTC Admit with only ETH funds = %v; want ErrInsufficientBalance", err)
	}
	checkBalance(t, f.ledger, author, faux.BitcoinMainnet, 0, 0, 0)
}

func TestConcurrentAdmissions(t *testing.T) {
	type result struct {
		record  wire.PreparedAdmissionRecord
		created bool
		err     error
	}
	race := func(f *fixture, a, b AdmissionDraft) (result, result) {
		var wg sync.WaitGroup
		var first, second result
		wg.Add(2)
		go func() {
			defer wg.Done()
			first.record, first.created, first.err = f.ledger.Admit(context.Background(), a, allow, sealReceipt)
		}()
		go func() {
			defer wg.Done()
			second.record, second.created, second.err = f.ledger.Admit(context.Background(), b, allow, sealReceipt)
		}()
		wg.Wait()
		return first, second
	}
	t.Run("different messages cannot overspend", func(t *testing.T) {
		f := newFixture(t)
		f.fund(t, author, faux.BitcoinMainnet, "100", "fund")
		first, second := race(f, draft("message-1", "quote-1", "75000"), draft("message-2", "quote-2", "75000"))
		if (first.err == nil) == (second.err == nil) {
			t.Fatalf("errors = %v, %v; want exactly one success", first.err, second.err)
		}
		if first.err != nil && !errors.Is(first.err, ErrInsufficientBalance) || second.err != nil && !errors.Is(second.err, ErrInsufficientBalance) {
			t.Errorf("errors = %v, %v; want ErrInsufficientBalance for the loser", first.err, second.err)
		}
		checkBalance(t, f.ledger, author, faux.BitcoinMainnet, 25_000, 0, 2)
		if got := f.ledger.Receipts(faux.BitcoinMainnet, 0, 10); len(got) != 1 {
			t.Errorf("Receipts = %d; want 1", len(got))
		}
	})
	t.Run("identical retries share one debit", func(t *testing.T) {
		f := newFixture(t)
		f.fund(t, author, faux.BitcoinMainnet, "100", "fund")
		first, second := race(f, draft("message", "quote", "75000"), draft("message", "quote", "75000"))
		if first.err != nil || second.err != nil || first.created == second.created || !reflect.DeepEqual(first.record, second.record) {
			t.Errorf("results = %+v, %+v; want one created and one retry of the same record", first, second)
		}
		checkBalance(t, f.ledger, author, faux.BitcoinMainnet, 25_000, 0, 2)
		if got := f.ledger.Receipts(faux.BitcoinMainnet, 0, 10); len(got) != 1 {
			t.Errorf("Receipts = %d; want 1", len(got))
		}
	})
}

func TestRestoreFee(t *testing.T) {
	notAccepted := func() (bool, error) { return false, nil }
	setup := func(t *testing.T) *fixture {
		t.Helper()
		f := newFixture(t)
		f.fund(t, author, faux.BitcoinMainnet, "50000", "fund")
		if _, _, err := f.admit("message", "quote", "136"); err != nil {
			t.Fatal(err)
		}
		return f
	}
	t.Run("restores once at the deadline", func(t *testing.T) {
		f := setup(t)
		f.now = start.Add(preparationWindow - time.Second)
		if _, err := f.ledger.RestoreFee(author, "message", notAccepted); !errors.Is(err, ErrConflict) {
			t.Fatalf("RestoreFee before the deadline = %v; want ErrConflict", err)
		}
		checkBalance(t, f.ledger, author, faux.BitcoinMainnet, 49_999_864, 0, 2)
		f.now = start.Add(preparationWindow)
		if restored, err := f.ledger.RestoreFee(author, "message", notAccepted); err != nil || !restored {
			t.Fatalf("RestoreFee = %v, %v; want restored", restored, err)
		}
		if restored, err := f.ledger.RestoreFee(author, "message", notAccepted); err != nil || restored {
			t.Errorf("repeated RestoreFee = %v, %v; want no second restore", restored, err)
		}
		checkBalance(t, f.ledger, author, faux.BitcoinMainnet, 50_000_000, 0, 3)
		if !f.ledger.Restored(author, "message") {
			t.Error("Restored = false; want true")
		}
	})
	t.Run("an accepted post is never refunded", func(t *testing.T) {
		f := setup(t)
		f.now = start.Add(preparationWindow)
		if _, err := f.ledger.RestoreFee(author, "message", func() (bool, error) { return true, nil }); !errors.Is(err, ErrConflict) {
			t.Errorf("RestoreFee = %v; want ErrConflict", err)
		}
		if _, err := f.ledger.RestoreFee(author, "message", nil); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("RestoreFee without acceptance state = %v; want ErrInvalidRequest", err)
		}
		checkBalance(t, f.ledger, author, faux.BitcoinMainnet, 49_999_864, 0, 2)
		if f.ledger.Restored(author, "message") {
			t.Error("Restored = true; want false")
		}
	})
	t.Run("unknown admission", func(t *testing.T) {
		f := setup(t)
		if _, err := f.ledger.RestoreFee(author, "other", notAccepted); !errors.Is(err, ErrNotFound) {
			t.Errorf("RestoreFee = %v; want ErrNotFound", err)
		}
	})
}
