package funding

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/wire"
)

const (
	testProvider = "did:key:provider"
	author       = "did:key:author"
	otherAuthor  = "did:key:other"
)

var (
	start = time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	// btcCap is the custody cap of fee schedule 1 for Bitcoin: 1,000,000 × 136 msat.
	btcCap = big.NewInt(136_000_000)
	ethCap = new(big.Int).Mul(big.NewInt(48_900_000_000), big.NewInt(1_000_000))
)

// testSealer fills IDs with distinct valid-looking values; signing is tested with the objects.
type testSealer struct{}

func (testSealer) SealInvoice(v wire.DepositInvoice) (wire.DepositInvoice, error) {
	v.DepositID = "fydi2:" + hexDigest(v.Recipient)
	v.ProviderSignature = "test-signature"
	return v, nil
}

func (testSealer) SealCredit(v wire.DepositCredit) (wire.DepositCredit, error) {
	v.CreditID = "fydc2:" + hexDigest(v.DepositID)
	v.ProviderSignature = "test-signature"
	return v, nil
}

func hexDigest(s string) string {
	d := sha256.Sum256([]byte(s))
	return hex.EncodeToString(d[:])
}

func sealReceipt(r wire.SubmissionReceipt) (wire.SubmissionReceipt, error) {
	r.ProviderSignature = "test-signature"
	return r, nil
}

func allow(time.Time) error { return nil }

// failingJournal wraps a journal; when fail is set, Append stores nothing and
// fails, and when crash is set, Append stores the record and then fails, as a
// crash between the journal write and the reply would.
type failingJournal struct {
	Journal
	fail, crash bool
}

func (j *failingJournal) Append(b []byte) error {
	if j.fail {
		return errors.New("disk unavailable")
	}
	if err := j.Journal.Append(b); err != nil {
		return err
	}
	if j.crash {
		return errors.New("crashed after write")
	}
	return nil
}

type fixture struct {
	now     time.Time
	path    string
	journal Journal
	chain   *faux.Ledger
	ledger  *Ledger
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	return newFixtureWith(t, 0, nil)
}

// newFixtureWith opens a ledger on a file journal in a temporary directory; wrap,
// when set, wraps that journal.
func newFixtureWith(t *testing.T, maxOpen int, wrap func(Journal) Journal) *fixture {
	t.Helper()
	f := &fixture{now: start, path: filepath.Join(t.TempDir(), "funding.jsonl")}
	chain, err := faux.Open(faux.Config{Now: f.clock})
	if err != nil {
		t.Fatal(err)
	}
	f.chain = chain
	j, err := NewFileJournal(f.path)
	if err != nil {
		t.Fatal(err)
	}
	if wrap != nil {
		j = wrap(j)
	}
	f.journal = j
	f.ledger = f.open(t, maxOpen)
	return f
}

func (f *fixture) clock() time.Time { return f.now }

func (f *fixture) open(t *testing.T, maxOpen int) *Ledger {
	t.Helper()
	l, err := Open(Config{ProviderID: testProvider, Journal: f.journal, Now: f.clock, RecipientLabel: "test", MaxOpenInvoices: maxOpen})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func (f *fixture) reopen(t *testing.T) *Ledger {
	t.Helper()
	return f.open(t, 0)
}

func depositRequest(authorID, network, amount, requestID string) wire.DepositRequest {
	return wire.DepositRequest{ProviderID: testProvider, AuthorID: authorID, Network: network, AmountChainUnits: amount, RequestID: requestID, NotAfter: "2026-11-11T00:01:00Z"}
}

func (f *fixture) invoice(t *testing.T, authorID, network, amount, requestID string) wire.DepositInvoice {
	t.Helper()
	limit := btcCap
	if network == faux.EthereumMainnet {
		limit = ethCap
	}
	inv, created, err := f.ledger.IssueInvoice(depositRequest(authorID, network, amount, requestID), limit, testSealer{})
	if err != nil || !created {
		t.Fatalf("IssueInvoice(%s, %s) = created %v, %v", requestID, amount, created, err)
	}
	return inv
}

// pay sends amount to recipient on the faux chain and returns a proof for inv.
func (f *fixture) pay(t *testing.T, inv wire.DepositInvoice, recipient, amount string, pending bool) wire.DepositProof {
	t.Helper()
	transfer, err := f.chain.Submit(context.Background(), faux.SubmitRequest{Network: inv.Network, Recipient: recipient, AmountChainUnits: amount, Pending: pending})
	if err != nil {
		t.Fatal(err)
	}
	return proofFor(inv, transfer)
}

func proofFor(inv wire.DepositInvoice, t faux.Transfer) wire.DepositProof {
	transfer := wire.DepositTransfer{Network: t.Network, TxID: t.TxID, TxHash: t.TxHash}
	if t.OutputIndex != nil {
		index := int64(*t.OutputIndex)
		transfer.OutputIndex = &index
	}
	return wire.DepositProof{ProviderID: testProvider, AuthorID: inv.AuthorID, DepositID: inv.DepositID, Transfer: transfer}
}

func (f *fixture) observe(p wire.DepositProof) (wire.DepositCredit, Outcome, error) {
	return f.ledger.Observe(context.Background(), p, f.chain, testSealer{})
}

// fund credits amount chain units to the author through an exactly paid invoice.
func (f *fixture) fund(t *testing.T, authorID, network, amount, requestID string) wire.DepositCredit {
	t.Helper()
	inv := f.invoice(t, authorID, network, amount, requestID)
	credit, outcome, err := f.observe(f.pay(t, inv, inv.Recipient, amount, false))
	if err != nil || outcome != Created {
		t.Fatalf("Observe(%s) = %v, %v; want Created", requestID, outcome, err)
	}
	return credit
}

func draft(messageID, quoteID, amount string) AdmissionDraft {
	q := wire.FeeQuote{ProviderID: testProvider, AuthorID: author, MessageID: messageID, ScheduleID: "fyfs2:schedule", QuoteID: quoteID, BillableBytes: 1, Network: faux.BitcoinMainnet, AmountBaseUnits: amount, PreflightAt: "2026-11-11T00:00:00Z", ExpiresAt: "2026-11-11T01:00:00Z", ProviderSignature: "quote-signature"}
	return AdmissionDraft{Request: wire.AdmissionRequest{ProviderID: testProvider, Authorization: "authorization", QuoteID: quoteID}, AuthorID: author, MessageID: messageID, Quote: q}
}

func (f *fixture) admit(messageID, quoteID, amount string) (wire.PreparedAdmissionRecord, bool, error) {
	return f.ledger.Admit(context.Background(), draft(messageID, quoteID, amount), allow, sealReceipt)
}

func checkBalance(t *testing.T, l *Ledger, authorID, network string, available, reserved int64, revision uint64) {
	t.Helper()
	b, err := l.Balance(authorID, network)
	if err != nil {
		t.Fatal(err)
	}
	if b.Available.Cmp(big.NewInt(available)) != 0 || b.Reserved.Cmp(big.NewInt(reserved)) != 0 || b.Frozen.Sign() != 0 || b.Revision != revision {
		t.Errorf("Balance(%s) = available %s reserved %s frozen %s revision %d; want available %d reserved %d frozen 0 revision %d", network, b.Available, b.Reserved, b.Frozen, b.Revision, available, reserved, revision)
	}
}

func journalSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}

func TestBalanceOfUntouchedAccount(t *testing.T) {
	f := newFixture(t)
	f.now = start.Add(90 * time.Second)
	b, err := f.ledger.Balance(author, faux.BitcoinMainnet)
	if err != nil {
		t.Fatal(err)
	}
	if b.Available.Sign() != 0 || b.Frozen.Sign() != 0 || b.Reserved.Sign() != 0 || b.Revision != 0 || !b.UpdatedAt.Equal(f.now) {
		t.Errorf("untouched Balance = %+v; want zero amounts, revision 0, updated at %s", b, f.now)
	}
}

func TestReplayRestoresCommittedState(t *testing.T) {
	f := newFixture(t)
	credit := f.fund(t, author, faux.BitcoinMainnet, "50000", "fund")
	first, _, err := f.admit("message-1", "quote-1", "136")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.admit("message-2", "quote-2", "272"); err != nil {
		t.Fatal(err)
	}
	over := f.invoice(t, author, faux.BitcoinMainnet, "1000", "over")
	if _, _, err := f.observe(f.pay(t, over, over.Recipient, "1001", false)); !errors.Is(err, ErrClaim) {
		t.Fatalf("overpayment = %v; want ErrClaim", err)
	}
	if _, _, err := f.ledger.RequestWithdrawal(wire.WithdrawalRequest{ProviderID: testProvider, AuthorID: author, Network: faux.BitcoinMainnet, AmountBaseUnits: "1000000", Recipient: faux.DevAddress(faux.BitcoinMainnet, "payout"), RequestID: "withdraw"}); err != nil {
		t.Fatal(err)
	}
	f.now = start.Add(preparationWindow)
	if _, err := f.ledger.RestoreFee(author, "message-1", func() (bool, error) { return false, nil }); err != nil {
		t.Fatal(err)
	}
	wantBalance, _ := f.ledger.Balance(author, faux.BitcoinMainnet)
	wantReceipts := f.ledger.Receipts(faux.BitcoinMainnet, 0, 10)
	wantClaims := f.ledger.Claims(author)
	wantWithdrawal, _ := f.ledger.Withdrawal(author, "withdraw")
	_, wantInvoice, _ := f.ledger.InvoiceForRequest(author, "fund")

	l := f.reopen(t)
	t.Run("balance", func(t *testing.T) {
		checkBalance(t, l, author, faux.BitcoinMainnet, 50_000_000-136-272+136-1_000_000, 1_000_000, 5)
		got, _ := l.Balance(author, faux.BitcoinMainnet)
		if !got.UpdatedAt.Equal(wantBalance.UpdatedAt) {
			t.Errorf("UpdatedAt = %s; want %s", got.UpdatedAt, wantBalance.UpdatedAt)
		}
	})
	t.Run("invoice", func(t *testing.T) {
		_, got, ok := l.InvoiceForRequest(author, "fund")
		if !ok || got != wantInvoice {
			t.Errorf("InvoiceForRequest = %+v, %v; want %+v", got, ok, wantInvoice)
		}
	})
	t.Run("credit", func(t *testing.T) {
		f.ledger = l
		got, outcome, err := f.observe(wire.DepositProof{ProviderID: testProvider, AuthorID: author, DepositID: credit.DepositID, Transfer: credit.Transfer})
		if err != nil || outcome != Existing || !reflect.DeepEqual(got, credit) {
			t.Errorf("Observe after reopen = %+v, %v, %v; want the original credit, Existing", got, outcome, err)
		}
	})
	t.Run("receipts", func(t *testing.T) {
		got := l.Receipts(faux.BitcoinMainnet, 0, 10)
		if len(got) != 2 || got[0].Sequence != 1 || got[1].Sequence != 2 || !reflect.DeepEqual(got, wantReceipts) {
			t.Errorf("Receipts = %+v; want %+v", got, wantReceipts)
		}
		record, ok := l.Admission(author, "message-1")
		if !ok || !reflect.DeepEqual(record, first) {
			t.Errorf("Admission = %+v, %v; want %+v", record, ok, first)
		}
		if !l.Restored(author, "message-1") || l.Restored(author, "message-2") {
			t.Error("Restored flags not replayed")
		}
	})
	t.Run("claims and withdrawals", func(t *testing.T) {
		if got := l.Claims(author); !reflect.DeepEqual(got, wantClaims) {
			t.Errorf("Claims = %+v; want %+v", got, wantClaims)
		}
		if got, ok := l.Withdrawal(author, "withdraw"); !ok || !reflect.DeepEqual(got, wantWithdrawal) {
			t.Errorf("Withdrawal = %+v, %v; want %+v", got, ok, wantWithdrawal)
		}
	})
}

func TestCrashAfterJournalWriteKeepsOneDebit(t *testing.T) {
	var crashing *failingJournal
	f := newFixtureWith(t, 0, func(j Journal) Journal {
		crashing = &failingJournal{Journal: j}
		return crashing
	})
	f.fund(t, author, faux.BitcoinMainnet, "50000", "fund")
	crashing.crash = true
	if _, _, err := f.admit("message", "quote", "136"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Admit during crash = %v; want ErrUnavailable", err)
	}
	crashing.crash = false
	f.ledger = f.reopen(t)
	record, created, err := f.admit("message", "quote", "136")
	if err != nil || created || record.Receipt.MessageID != "message" {
		t.Fatalf("retry after restart = %+v, created %v, %v; want the committed record", record, created, err)
	}
	checkBalance(t, f.ledger, author, faux.BitcoinMainnet, 49_999_864, 0, 2)
}

func TestFailedJournalFailsClosed(t *testing.T) {
	var failing *failingJournal
	f := newFixtureWith(t, 0, func(j Journal) Journal {
		failing = &failingJournal{Journal: j}
		return failing
	})
	f.fund(t, author, faux.BitcoinMainnet, "50000", "fund")
	committed, _, err := f.admit("message-1", "quote-1", "136")
	if err != nil {
		t.Fatal(err)
	}
	failing.fail = true
	if _, _, err := f.admit("message-2", "quote-2", "136"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Admit with failing journal = %v; want ErrUnavailable", err)
	}
	failing.fail = false
	t.Run("later mutations stay unavailable", func(t *testing.T) {
		if _, _, err := f.admit("message-3", "quote-3", "136"); !errors.Is(err, ErrUnavailable) {
			t.Errorf("Admit after failure = %v; want ErrUnavailable", err)
		}
		_, _, err := f.ledger.IssueInvoice(depositRequest(author, faux.BitcoinMainnet, "1000", "later"), btcCap, testSealer{})
		if !errors.Is(err, ErrUnavailable) {
			t.Errorf("IssueInvoice after failure = %v; want ErrUnavailable", err)
		}
	})
	t.Run("reads keep working", func(t *testing.T) {
		checkBalance(t, f.ledger, author, faux.BitcoinMainnet, 49_999_864, 0, 2)
		if got, ok := f.ledger.Admission(author, "message-1"); !ok || !reflect.DeepEqual(got, committed) {
			t.Errorf("Admission = %+v, %v; want the committed record", got, ok)
		}
		if _, ok := f.ledger.Admission(author, "message-2"); ok {
			t.Error("failed admission was retained")
		}
		if _, _, ok := f.ledger.InvoiceForRequest(author, "fund"); !ok {
			t.Error("InvoiceForRequest lost the invoice")
		}
		if got := f.ledger.Receipts(faux.BitcoinMainnet, 0, 10); len(got) != 1 {
			t.Errorf("Receipts = %d entries; want 1", len(got))
		}
	})
	t.Run("committed retry still answers", func(t *testing.T) {
		got, created, err := f.admit("message-1", "quote-1", "136")
		if err != nil || created || !reflect.DeepEqual(got, committed) {
			t.Errorf("retry = %+v, %v, %v; want the original record", got, created, err)
		}
	})
}

func TestReplayRejectsUnappliableRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "funding.jsonl")
	receipt := wire.SubmissionReceipt{ProviderID: testProvider, AuthorID: author, MessageID: "message", ReceiptID: "receipt", QuoteID: "quote", ScheduleID: "schedule", Network: faux.BitcoinMainnet, AmountBaseUnits: "136", AuthorizationAt: "2026-11-11T00:00:00Z", IssuedAt: "2026-11-11T00:00:00Z", ProviderSignature: "test-signature"}
	b, err := json.Marshal(record{Kind: kindAdmission, At: "2026-11-11T00:00:00Z", Admission: &wire.PreparedAdmissionRecord{Receipt: receipt}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	j, err := NewFileJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	l, err := Open(Config{ProviderID: testProvider, Journal: j, Now: func() time.Time { return start }})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := l.Admission(author, "message"); ok {
		t.Error("a debit without funds was replayed")
	}
	_, _, err = l.IssueInvoice(depositRequest(author, faux.BitcoinMainnet, "1000", "request"), btcCap, testSealer{})
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("IssueInvoice after a bad replay = %v; want ErrUnavailable", err)
	}
}

func TestFileJournalRecovery(t *testing.T) {
	setup := func(t *testing.T) *fixture {
		t.Helper()
		f := newFixture(t)
		f.invoice(t, author, faux.BitcoinMainnet, "1000", "first")
		f.invoice(t, author, faux.BitcoinMainnet, "1000", "second")
		return f
	}
	t.Run("torn final record is truncated", func(t *testing.T) {
		f := setup(t)
		size := journalSize(t, f.path)
		file, err := os.OpenFile(f.path, os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteString(`{"kind":"invoice","at":"2026-`); err != nil {
			t.Fatal(err)
		}
		file.Close()
		f.ledger = f.reopen(t)
		if got := journalSize(t, f.path); got != size {
			t.Errorf("journal size = %d; want %d", got, size)
		}
		f.invoice(t, author, faux.BitcoinMainnet, "1000", "third")
	})
	t.Run("final record without newline is kept", func(t *testing.T) {
		f := setup(t)
		size := journalSize(t, f.path)
		if err := os.Truncate(f.path, size-1); err != nil {
			t.Fatal(err)
		}
		f.ledger = f.reopen(t)
		if _, _, ok := f.ledger.InvoiceForRequest(author, "second"); !ok {
			t.Error("final record was dropped")
		}
		if got := journalSize(t, f.path); got != size {
			t.Errorf("journal size = %d; want %d", got, size)
		}
		f.invoice(t, author, faux.BitcoinMainnet, "1000", "third")
	})
	t.Run("damaged middle record fails closed", func(t *testing.T) {
		f := setup(t)
		data, err := os.ReadFile(f.path)
		if err != nil {
			t.Fatal(err)
		}
		damaged := bytes.Replace(data, []byte(`"kind":"invoice"`), []byte(`"kind":"invo!ce"`), 1)
		if err := os.WriteFile(f.path, damaged, 0600); err != nil {
			t.Fatal(err)
		}
		f.ledger = f.reopen(t)
		_, _, err = f.ledger.IssueInvoice(depositRequest(author, faux.BitcoinMainnet, "1000", "third"), btcCap, testSealer{})
		if !errors.Is(err, ErrUnavailable) {
			t.Errorf("IssueInvoice = %v; want ErrUnavailable", err)
		}
		if got, err := os.ReadFile(f.path); err != nil || !bytes.Equal(got, damaged) {
			t.Error("damaged journal was changed")
		}
	})
}

func TestClockRollbackStopsIssuance(t *testing.T) {
	f := newFixture(t)
	f.invoice(t, author, faux.BitcoinMainnet, "1000", "first")
	t.Run("60 seconds back still issues", func(t *testing.T) {
		f.now = start.Add(-60 * time.Second)
		f.invoice(t, author, faux.BitcoinMainnet, "1000", "second")
	})
	t.Run("61 seconds back is unavailable", func(t *testing.T) {
		f.now = start.Add(-61 * time.Second)
		_, _, err := f.ledger.IssueInvoice(depositRequest(author, faux.BitcoinMainnet, "1000", "third"), btcCap, testSealer{})
		if !errors.Is(err, ErrUnavailable) {
			t.Errorf("IssueInvoice = %v; want ErrUnavailable", err)
		}
	})
}
