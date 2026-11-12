package funding

import (
	"context"
	"errors"
	"math/big"
	"reflect"
	"strings"
	"testing"
	"time"

	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/wire"
)

func TestPendingTransferNeverCredits(t *testing.T) {
	f := newFixture(t)
	inv := f.invoice(t, author, faux.BitcoinMainnet, "50000", "fund")
	proof := f.pay(t, inv, inv.Recipient, "50000", true)
	t.Run("one confirmation is pending", func(t *testing.T) {
		if _, outcome, err := f.observe(proof); err != nil || outcome != Pending {
			t.Fatalf("Observe = %v, %v; want Pending", outcome, err)
		}
		checkBalance(t, f.ledger, author, faux.BitcoinMainnet, 0, 0, 0)
	})
	if _, err := f.chain.Mine(context.Background(), faux.BitcoinMainnet, 5); err != nil {
		t.Fatal(err)
	}
	credit, outcome, err := f.observe(proof)
	t.Run("six confirmations credit", func(t *testing.T) {
		if err != nil || outcome != Created {
			t.Fatalf("Observe = %v, %v; want Created", outcome, err)
		}
		want := wire.DepositCredit{CreditID: "fydc2:" + hexDigest(inv.DepositID), ProviderID: testProvider, AuthorID: author, DepositID: inv.DepositID, Network: faux.BitcoinMainnet, Transfer: proof.Transfer, AmountChainUnits: "50000", AmountAccountingUnits: "50000000", CreditSequence: 1, CreditedAt: "2026-11-11T00:00:00Z", ProviderSignature: "test-signature"}
		if !reflect.DeepEqual(credit, want) {
			t.Errorf("credit = %+v; want %+v", credit, want)
		}
		checkBalance(t, f.ledger, author, faux.BitcoinMainnet, 50_000_000, 0, 1)
	})
	t.Run("retry returns the original credit", func(t *testing.T) {
		again, outcome, err := f.observe(proof)
		if err != nil || outcome != Existing || !reflect.DeepEqual(again, credit) {
			t.Errorf("Observe = %+v, %v, %v; want the original credit, Existing", again, outcome, err)
		}
		checkBalance(t, f.ledger, author, faux.BitcoinMainnet, 50_000_000, 0, 1)
	})
}

func TestTransfersThatDoNotQualifyAreClaims(t *testing.T) {
	f := newFixture(t)
	inv := f.invoice(t, author, faux.BitcoinMainnet, "50000", "fund")
	claim := func(t *testing.T, p wire.DepositProof, reason string, count int) {
		t.Helper()
		if _, _, err := f.observe(p); !errors.Is(err, ErrClaim) {
			t.Fatalf("Observe = %v; want ErrClaim", err)
		}
		if _, _, err := f.observe(p); !errors.Is(err, ErrClaim) {
			t.Fatalf("repeated Observe = %v; want ErrClaim", err)
		}
		claims := f.ledger.Claims(author)
		if len(claims) != count || claims[count-1].Reason != reason || claims[count-1].AmountChainUnits == "" {
			t.Fatalf("Claims = %+v; want %d claims, the last %q", claims, count, reason)
		}
	}
	t.Run("underpaid", func(t *testing.T) {
		claim(t, f.pay(t, inv, inv.Recipient, "49999", false), ClaimUnderpaid, 1)
		checkBalance(t, f.ledger, author, faux.BitcoinMainnet, 0, 0, 0)
	})
	t.Run("overpaid", func(t *testing.T) {
		claim(t, f.pay(t, inv, inv.Recipient, "50001", false), ClaimOverpaid, 2)
		checkBalance(t, f.ledger, author, faux.BitcoinMainnet, 0, 0, 0)
	})
	exact := f.pay(t, inv, inv.Recipient, "50000", false)
	if _, outcome, err := f.observe(exact); err != nil || outcome != Created {
		t.Fatalf("exact payment = %v, %v; want Created", outcome, err)
	}
	t.Run("second exact payment", func(t *testing.T) {
		claim(t, f.pay(t, inv, inv.Recipient, "50000", false), ClaimInvoiceAlreadyCredited, 3)
		checkBalance(t, f.ledger, author, faux.BitcoinMainnet, 50_000_000, 0, 1)
	})
	t.Run("credited transfer hinted against another invoice", func(t *testing.T) {
		other := f.invoice(t, author, faux.BitcoinMainnet, "1000", "other")
		p := exact
		p.DepositID = other.DepositID
		if _, _, err := f.observe(p); !errors.Is(err, ErrClaim) {
			t.Errorf("Observe = %v; want ErrClaim", err)
		}
		checkBalance(t, f.ledger, author, faux.BitcoinMainnet, 50_000_000, 0, 1)
	})
	t.Run("late", func(t *testing.T) {
		late := f.invoice(t, author, faux.BitcoinMainnet, "1000", "late")
		f.now = start.Add(24*time.Hour + time.Second)
		claim(t, f.pay(t, late, late.Recipient, "1000", false), ClaimLate, 4)
		checkBalance(t, f.ledger, author, faux.BitcoinMainnet, 50_000_000, 0, 1)
	})
}

func TestObservationHints(t *testing.T) {
	f := newFixture(t)
	inv := f.invoice(t, author, faux.BitcoinMainnet, "50000", "fund")
	t.Run("wrong recipient", func(t *testing.T) {
		p := f.pay(t, inv, faux.DevAddress(faux.BitcoinMainnet, "elsewhere"), "50000", false)
		if _, _, err := f.observe(p); !errors.Is(err, ErrInvalidHint) {
			t.Errorf("Observe = %v; want ErrInvalidHint", err)
		}
	})
	t.Run("unknown transfer stores nothing", func(t *testing.T) {
		size := journalSize(t, f.path)
		p := wire.DepositProof{ProviderID: testProvider, AuthorID: author, DepositID: inv.DepositID, Transfer: wire.DepositTransfer{Network: faux.BitcoinMainnet, TxID: strings.Repeat("ab", 32), OutputIndex: new(int64)}}
		if _, outcome, err := f.observe(p); err != nil || outcome != Pending {
			t.Errorf("Observe = %v, %v; want Pending", outcome, err)
		}
		if got := journalSize(t, f.path); got != size {
			t.Errorf("journal grew from %d to %d bytes", size, got)
		}
	})
	t.Run("another payment output stores nothing", func(t *testing.T) {
		size := journalSize(t, f.path)
		p := f.pay(t, inv, inv.Recipient, "50000", false)
		*p.Transfer.OutputIndex = 1
		if _, outcome, err := f.observe(p); err != nil || outcome != Pending {
			t.Errorf("Observe = %v, %v; want Pending", outcome, err)
		}
		if got := journalSize(t, f.path); got != size {
			t.Errorf("journal grew from %d to %d bytes", size, got)
		}
	})
	t.Run("another author's proof", func(t *testing.T) {
		p := f.pay(t, inv, inv.Recipient, "50000", false)
		p.AuthorID = otherAuthor
		if _, _, err := f.observe(p); !errors.Is(err, ErrNotFound) {
			t.Errorf("Observe = %v; want ErrNotFound", err)
		}
	})
	t.Run("transfer on another network", func(t *testing.T) {
		p := wire.DepositProof{ProviderID: testProvider, AuthorID: author, DepositID: inv.DepositID, Transfer: wire.DepositTransfer{Network: faux.EthereumMainnet, TxHash: strings.Repeat("ab", 32)}}
		if _, _, err := f.observe(p); !errors.Is(err, ErrInvalidHint) {
			t.Errorf("Observe = %v; want ErrInvalidHint", err)
		}
	})
	checkBalance(t, f.ledger, author, faux.BitcoinMainnet, 0, 0, 0)
}

func TestInvoiceLimits(t *testing.T) {
	issue := func(f *fixture, authorID, amount, requestID string) error {
		_, _, err := f.ledger.IssueInvoice(depositRequest(authorID, faux.BitcoinMainnet, amount, requestID), btcCap, testSealer{})
		return err
	}
	t.Run("four outstanding invoices per account", func(t *testing.T) {
		f := newFixture(t)
		f.invoice(t, author, faux.BitcoinMainnet, "1000", "one")
		f.now = start.Add(time.Hour)
		f.invoice(t, author, faux.BitcoinMainnet, "1000", "two")
		f.invoice(t, author, faux.BitcoinMainnet, "1000", "three")
		f.invoice(t, author, faux.BitcoinMainnet, "1000", "four")
		if err := issue(f, author, "1000", "five"); !errors.Is(err, ErrInvoiceLimit) {
			t.Fatalf("fifth invoice = %v; want ErrInvoiceLimit", err)
		}
		f.now = start.Add(24*time.Hour + time.Second)
		f.invoice(t, author, faux.BitcoinMainnet, "1000", "five")
		if err := issue(f, author, "1000", "six"); !errors.Is(err, ErrInvoiceLimit) {
			t.Errorf("sixth invoice = %v; want ErrInvoiceLimit", err)
		}
	})
	t.Run("an expired invoice with a pending transfer frees its slot", func(t *testing.T) {
		f := newFixture(t)
		held := f.invoice(t, author, faux.BitcoinMainnet, "1000", "held")
		proof := f.pay(t, held, held.Recipient, "1000", true)
		if _, outcome, err := f.observe(proof); err != nil || outcome != Pending {
			t.Fatalf("Observe = %v, %v; want Pending", outcome, err)
		}
		f.now = start.Add(24*time.Hour + time.Second)
		f.invoice(t, author, faux.BitcoinMainnet, "1000", "two")
		f.invoice(t, author, faux.BitcoinMainnet, "1000", "three")
		f.invoice(t, author, faux.BitcoinMainnet, "1000", "four")
		f.invoice(t, author, faux.BitcoinMainnet, "1000", "five")
		if err := issue(f, author, "1000", "six"); !errors.Is(err, ErrInvoiceLimit) {
			t.Errorf("sixth invoice = %v; want ErrInvoiceLimit", err)
		}
		if _, err := f.chain.Mine(context.Background(), faux.BitcoinMainnet, 5); err != nil {
			t.Fatal(err)
		}
		if _, outcome, err := f.observe(proof); err != nil || outcome != Created {
			t.Errorf("Observe once final = %v, %v; want Created", outcome, err)
		}
		checkBalance(t, f.ledger, author, faux.BitcoinMainnet, 1_000_000, 0, 1)
	})
	t.Run("custody cap on an empty account", func(t *testing.T) {
		f := newFixture(t)
		f.invoice(t, author, faux.BitcoinMainnet, "136000", "at-cap")
		if err := issue(f, otherAuthor, "136001", "over-cap"); !errors.Is(err, ErrCustodyCap) {
			t.Errorf("136001 sat = %v; want ErrCustodyCap", err)
		}
	})
	t.Run("custody cap counts open invoices", func(t *testing.T) {
		f := newFixture(t)
		f.invoice(t, author, faux.BitcoinMainnet, "100000", "open")
		if err := issue(f, author, "36001", "over-cap"); !errors.Is(err, ErrCustodyCap) {
			t.Errorf("36001 sat = %v; want ErrCustodyCap", err)
		}
		f.invoice(t, author, faux.BitcoinMainnet, "36000", "at-cap")
	})
	t.Run("custody cap counts confirmed funds", func(t *testing.T) {
		f := newFixture(t)
		f.fund(t, author, faux.BitcoinMainnet, "100000", "funded")
		if err := issue(f, author, "36001", "over-cap"); !errors.Is(err, ErrCustodyCap) {
			t.Errorf("36001 sat = %v; want ErrCustodyCap", err)
		}
	})
	t.Run("provider saturation", func(t *testing.T) {
		f := newFixtureWith(t, 1, nil)
		f.invoice(t, author, faux.BitcoinMainnet, "1000", "one")
		if err := issue(f, otherAuthor, "1000", "two"); !errors.Is(err, ErrSaturated) {
			t.Errorf("second invoice = %v; want ErrSaturated", err)
		}
	})
}

func TestInvoiceRequestRetry(t *testing.T) {
	f := newFixture(t)
	first := f.invoice(t, author, faux.BitcoinMainnet, "50000", "first")
	t.Run("identical request returns the original", func(t *testing.T) {
		got, created, err := f.ledger.IssueInvoice(depositRequest(author, faux.BitcoinMainnet, "50000", "first"), btcCap, testSealer{})
		if err != nil || created || got != first {
			t.Errorf("IssueInvoice = %+v, %v, %v; want the original, not created", got, created, err)
		}
	})
	t.Run("same request ID with another amount", func(t *testing.T) {
		_, _, err := f.ledger.IssueInvoice(depositRequest(author, faux.BitcoinMainnet, "50001", "first"), btcCap, testSealer{})
		if !errors.Is(err, ErrRequestConflict) {
			t.Errorf("IssueInvoice = %v; want ErrRequestConflict", err)
		}
	})
	t.Run("recipients are never reassigned", func(t *testing.T) {
		second := f.invoice(t, author, faux.BitcoinMainnet, "10000", "second")
		if second.Recipient == first.Recipient {
			t.Fatalf("two invoices share recipient %s", first.Recipient)
		}
		f.ledger = f.reopen(t)
		if _, got, ok := f.ledger.InvoiceForRequest(author, "second"); !ok || got != second {
			t.Errorf("InvoiceForRequest after reopen = %+v, %v; want %+v", got, ok, second)
		}
		third := f.invoice(t, author, faux.BitcoinMainnet, "10000", "third")
		if third.Recipient == first.Recipient || third.Recipient == second.Recipient {
			t.Errorf("recipient %s reassigned after reopen", third.Recipient)
		}
	})
	t.Run("invoice fields", func(t *testing.T) {
		want := wire.DepositInvoice{DepositID: first.DepositID, ProviderID: testProvider, AuthorID: author, Network: faux.BitcoinMainnet, AmountChainUnits: "50000", Recipient: faux.DevAddress(faux.BitcoinMainnet, "test/deposit/1"), IssuedAt: "2026-11-11T00:00:00Z", ExpiresAt: "2026-11-12T00:00:00Z", ProviderSignature: "test-signature"}
		if first != want {
			t.Errorf("invoice = %+v; want %+v", first, want)
		}
	})
}

func TestClaimDisposition(t *testing.T) {
	f := newFixture(t)
	inv := f.invoice(t, author, faux.BitcoinMainnet, "50000", "fund")
	overpaid := f.pay(t, inv, inv.Recipient, "50001", false)
	underpaid := f.pay(t, inv, inv.Recipient, "49999", false)
	if _, _, err := f.observe(overpaid); !errors.Is(err, ErrClaim) {
		t.Fatal(err)
	}
	if _, _, err := f.observe(underpaid); !errors.Is(err, ErrClaim) {
		t.Fatal(err)
	}
	claims := f.ledger.Claims(author)
	t.Run("credited once", func(t *testing.T) {
		c, err := f.ledger.DisposeClaim(claims[0].TransferID, DispositionCredited)
		if err != nil || c.Disposition != DispositionCredited || !c.DisposedAt.Equal(start) {
			t.Fatalf("DisposeClaim = %+v, %v", c, err)
		}
		if _, err := f.ledger.DisposeClaim(claims[0].TransferID, DispositionCredited); err != nil {
			t.Fatalf("repeated DisposeClaim = %v", err)
		}
		checkBalance(t, f.ledger, author, faux.BitcoinMainnet, 50_001_000, 0, 1)
	})
	t.Run("a different disposition is refused", func(t *testing.T) {
		if _, err := f.ledger.DisposeClaim(claims[0].TransferID, DispositionReturned); !errors.Is(err, ErrConflict) {
			t.Errorf("DisposeClaim(returned) after credited = %v; want ErrConflict", err)
		}
		if _, err := f.ledger.DisposeClaim(claims[1].TransferID, DispositionReturned); err != nil {
			t.Fatal(err)
		}
		if _, err := f.ledger.DisposeClaim(claims[1].TransferID, DispositionCredited); !errors.Is(err, ErrConflict) {
			t.Errorf("DisposeClaim(credited) after returned = %v; want ErrConflict", err)
		}
		checkBalance(t, f.ledger, author, faux.BitcoinMainnet, 50_001_000, 0, 1)
	})
	t.Run("survives reopen", func(t *testing.T) {
		l := f.reopen(t)
		if _, err := l.DisposeClaim(claims[1].TransferID, DispositionCredited); !errors.Is(err, ErrConflict) {
			t.Errorf("DisposeClaim after reopen = %v; want ErrConflict", err)
		}
		checkBalance(t, l, author, faux.BitcoinMainnet, 50_001_000, 0, 1)
	})
}

func TestEthereumDeposit(t *testing.T) {
	f := newFixture(t)
	credit := f.fund(t, author, faux.EthereumMainnet, "48900000000000", "eth")
	if credit.AmountAccountingUnits != "48900000000000" || credit.Transfer.OutputIndex != nil {
		t.Errorf("credit = %+v; want 48900000000000 wei with a tx_hash identity", credit)
	}
	b, err := f.ledger.Balance(author, faux.EthereumMainnet)
	if err != nil || b.Available.Cmp(big.NewInt(48_900_000_000_000)) != 0 {
		t.Errorf("ETH balance = %s, %v; want 48900000000000", b.Available, err)
	}
}
