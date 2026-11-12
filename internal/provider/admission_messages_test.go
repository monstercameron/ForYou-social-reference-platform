package provider

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/content"
	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/fee"
	"foryou.local/foryou/internal/funding"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/message"
	"foryou.local/foryou/internal/store"
	"foryou.local/foryou/internal/wire"
)

type finalizeFixture struct {
	service *Service
	clock   *time.Time
	intent  wire.Intent
	message wire.PostMessage
	key     keys.PrivateKey
	storage *finalizeFailingStore
}

type finalizeFailingStore struct {
	store.Store
	fail bool
}

func (s *finalizeFailingStore) Update(fn func(store.Tx) error) error {
	if s.fail {
		return fmt.Errorf("injected commit failure: %w", errors.New("storage unavailable"))
	}
	return s.Store.Update(fn)
}

func newFinalizeFixture(t *testing.T, text string) *finalizeFixture {
	t.Helper()
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	cfg, err := TestConfig(clock)
	if err != nil {
		t.Fatal(err)
	}
	storage := &finalizeFailingStore{Store: store.NewMemory()}
	cfg.ContentStore = storage
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	seed := sha256.Sum256([]byte("provider finalization test author"))
	authorKey := keys.PrivateKey(ed25519.NewKeyFromSeed(seed[:]))
	intent := wire.Intent{
		AuthorID:    keys.DIDOf(authorKey),
		Timestamp:   wire.FormatTime(now),
		MessageType: "post",
		Version:     wire.Version{Major: 2},
		Payload:     wire.PostPayload{ContentFormat: "text/plain", Text: text},
	}
	if err := content.ValidatePostContent(intent.Payload); err != nil {
		t.Fatal(err)
	}
	messageID, err := message.ID(intent)
	if err != nil {
		t.Fatal(err)
	}
	chain, err := faux.Open(faux.Config{Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	providerKey := s.Key()
	sealer := funding.Signer{Key: providerKey}
	cap := new(big.Int).Exp(big.NewInt(10), big.NewInt(15), nil)
	invoice, created, err := s.Ledger.IssueInvoice(wire.DepositRequest{
		ProviderID: s.ProviderID(), AuthorID: intent.AuthorID, Network: faux.BitcoinMainnet,
		AmountChainUnits: "50000", RequestID: "finalize-fund", NotAfter: wire.FormatTime(now.Add(time.Minute)),
	}, cap, sealer)
	if err != nil || !created {
		t.Fatalf("IssueInvoice = created %t, %v", created, err)
	}
	transfer, err := chain.Submit(context.Background(), faux.SubmitRequest{Network: invoice.Network, Recipient: invoice.Recipient, AmountChainUnits: invoice.AmountChainUnits})
	if err != nil {
		t.Fatal(err)
	}
	index := int64(0)
	proof := wire.DepositProof{ProviderID: s.ProviderID(), AuthorID: intent.AuthorID, DepositID: invoice.DepositID, Transfer: wire.DepositTransfer{Network: transfer.Network, TxID: transfer.TxID, OutputIndex: &index}}
	if _, outcome, err := s.Ledger.Observe(context.Background(), proof, chain, sealer); err != nil || outcome != funding.Created {
		t.Fatalf("Observe = %v, %v", outcome, err)
	}
	intentBytes, err := canon.Encode(intent)
	if err != nil {
		t.Fatal(err)
	}
	quoteID := "fyfq2:" + strings.Repeat("0", 64)
	scheduleID := s.FeeSchedules()[0].ScheduleID
	quote := wire.FeeQuote{
		ProviderID: s.ProviderID(), AuthorID: intent.AuthorID, MessageID: messageID,
		ScheduleID: scheduleID, QuoteID: quoteID, BillableBytes: int64(len(intentBytes)),
		Network: faux.BitcoinMainnet, AmountBaseUnits: "136", PreflightAt: wire.FormatTime(now),
		ExpiresAt: wire.FormatTime(now.Add(time.Hour)), ProviderSignature: "unused-by-ledger",
	}
	draft := funding.AdmissionDraft{
		Request:  wire.AdmissionRequest{ProviderID: s.ProviderID(), Intent: intent, Authorization: "unused-by-ledger", QuoteID: quoteID},
		AuthorID: intent.AuthorID, MessageID: messageID, Quote: quote,
	}
	prepared, created, err := s.Ledger.Admit(context.Background(), draft, func(time.Time) error { return nil }, func(r wire.SubmissionReceipt) (wire.SubmissionReceipt, error) {
		return fee.SignReceipt(r, providerKey)
	})
	if err != nil || !created {
		t.Fatalf("Admit = created %t, %v", created, err)
	}
	msg, err := message.Assemble(intent, prepared.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err = message.Sign(&msg, authorKey); err != nil {
		t.Fatal(err)
	}
	return &finalizeFixture{service: s, clock: &now, intent: intent, message: msg, key: authorKey, storage: storage}
}

func finalizeRequest(t *testing.T, s *Service, msg wire.PostMessage) *httptest.ResponseRecorder {
	t.Helper()
	b, err := canon.Encode(msg)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(b)))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func TestFinalizeMessageAcceptsAndRecoversRetry(t *testing.T) {
	f := newFinalizeFixture(t, "accepted post")
	before, err := f.service.Ledger.Balance(f.intent.AuthorID, faux.BitcoinMainnet)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("first finalization commits", func(t *testing.T) {
		w := finalizeRequest(t, f.service, f.message)
		if w.Code != http.StatusCreated || w.Body.String() != `{"message_id":"`+f.message.MessageID+`","status":"accepted"}` {
			t.Fatalf("first finalization = %d %s", w.Code, w.Body.String())
		}
	})
	t.Run("retry recovers accepted state without debit", func(t *testing.T) {
		w := finalizeRequest(t, f.service, f.message)
		if w.Code != http.StatusOK || w.Body.String() != `{"message_id":"`+f.message.MessageID+`","status":"accepted"}` {
			t.Fatalf("retry = %d %s", w.Code, w.Body.String())
		}
		after, err := f.service.Ledger.Balance(f.intent.AuthorID, faux.BitcoinMainnet)
		if err != nil || before.Available.Cmp(after.Available) != 0 || before.Revision != after.Revision {
			t.Fatalf("retry changed balance: before=%+v after=%+v err=%v", before, after, err)
		}
	})
	t.Run("another valid receipt is retained as evidence", func(t *testing.T) {
		changed := f.message
		changed.SubmissionReceipt.ReceiptID = keys.EncodeB64URL([]byte(strings.Repeat("r", 32)))
		changed.SubmissionReceipt, err = fee.SignReceipt(changed.SubmissionReceipt, f.service.Key())
		if err != nil {
			t.Fatal(err)
		}
		if err = message.Sign(&changed, f.key); err != nil {
			t.Fatal(err)
		}
		w := finalizeRequest(t, f.service, changed)
		wantBody := `{"message_id":"` + f.message.MessageID + `","status":"accepted"}`
		if w.Code != http.StatusOK || w.Body.String() != wantBody {
			t.Fatalf("alternate receipt retry = %d %s", w.Code, w.Body.String())
		}
		var found bool
		if err = f.service.Store().View(func(tx store.Tx) error {
			_, found, err = tx.Get("provider.messages.acceptance_evidence", f.message.MessageID+"\n"+changed.SubmissionReceipt.ReceiptID)
			return err
		}); err != nil || !found {
			t.Fatalf("alternate receipt evidence found=%t err=%v", found, err)
		}
	})
}

func TestFinalizeMessageDeadlineAndRetryOrdering(t *testing.T) {
	f := newFinalizeFixture(t, "deadline post")
	*f.clock = f.messageTime().Add(30*24*time.Hour - time.Second)
	w := finalizeRequest(t, f.service, f.message)
	if w.Code != http.StatusCreated {
		t.Fatalf("last-second finalization = %d %s", w.Code, w.Body.String())
	}
	*f.clock = f.messageTime().Add(1000 * 24 * time.Hour)
	w = finalizeRequest(t, f.service, f.message)
	if w.Code != http.StatusOK {
		t.Fatalf("late accepted retry = %d %s", w.Code, w.Body.String())
	}
	second := newFinalizeFixture(t, "different deadline post")
	*second.clock = second.messageTime().Add(30 * 24 * time.Hour)
	w = finalizeRequest(t, second.service, second.message)
	if w.Code != http.StatusConflict || w.Body.String() != `{"status":"stale_state"}` {
		t.Fatalf("at-deadline finalization = %d %s", w.Code, w.Body.String())
	}
}

func (f *finalizeFixture) messageTime() time.Time {
	at, _ := wire.ParseTime(f.message.SubmissionReceipt.IssuedAt)
	return at
}

func TestFinalizeMessageRejectsTamperedIntentAndReceipt(t *testing.T) {
	f := newFinalizeFixture(t, "original text")
	t.Run("tampered text fails ID recomputation", func(t *testing.T) {
		changed := f.message
		changed.Payload.Text = "tampered text"
		if err := message.Sign(&changed, f.key); err != nil {
			t.Fatal(err)
		}
		w := finalizeRequest(t, f.service, changed)
		if w.Code != http.StatusUnprocessableEntity || w.Body.String() != `{"status":"id_mismatch"}` {
			t.Fatalf("tampered message = %d %s", w.Code, w.Body.String())
		}
	})
	t.Run("valid but altered receipt conflicts with prepared receipt", func(t *testing.T) {
		changed := f.message
		changed.SubmissionReceipt.AmountBaseUnits = "137"
		signedReceipt, err := fee.SignReceipt(changed.SubmissionReceipt, f.service.Key())
		if err != nil {
			t.Fatal(err)
		}
		changed.SubmissionReceipt = signedReceipt
		if err := message.Sign(&changed, f.key); err != nil {
			t.Fatal(err)
		}
		w := finalizeRequest(t, f.service, changed)
		if w.Code != http.StatusUnprocessableEntity || w.Body.String() != `{"status":"invalid_message"}` {
			t.Fatalf("altered receipt = %d %s", w.Code, w.Body.String())
		}
	})
	t.Run("missing prepared admission is rejected", func(t *testing.T) {
		intent := f.intent
		intent.Payload.Text = "no admission"
		id, err := message.ID(intent)
		if err != nil {
			t.Fatal(err)
		}
		receipt := f.message.SubmissionReceipt
		receipt.MessageID = id
		receipt.ReceiptID = keys.EncodeB64URL([]byte(strings.Repeat("n", 32)))
		receipt, err = fee.SignReceipt(receipt, f.service.Key())
		if err != nil {
			t.Fatal(err)
		}
		msg, err := message.Assemble(intent, receipt)
		if err != nil {
			t.Fatal(err)
		}
		if err = message.Sign(&msg, f.key); err != nil {
			t.Fatal(err)
		}
		w := finalizeRequest(t, f.service, msg)
		if w.Code != http.StatusUnprocessableEntity || w.Body.String() != `{"status":"invalid_message"}` {
			t.Fatalf("missing admission = %d %s", w.Code, w.Body.String())
		}
	})
}

func TestFinalizeMessageStorageFailureIsUnavailable(t *testing.T) {
	f := newFinalizeFixture(t, "storage failure")
	before, err := f.service.Ledger.Balance(f.intent.AuthorID, faux.BitcoinMainnet)
	if err != nil {
		t.Fatal(err)
	}
	f.storage.fail = true
	w := finalizeRequest(t, f.service, f.message)
	if w.Code != http.StatusServiceUnavailable || w.Body.String() != `{"retry_after_seconds":5,"status":"unavailable"}` {
		t.Fatalf("storage failure = %d %s", w.Code, w.Body.String())
	}
	after, err := f.service.Ledger.Balance(f.intent.AuthorID, faux.BitcoinMainnet)
	if err != nil || before.Available.Cmp(after.Available) != 0 || before.Revision != after.Revision {
		t.Fatalf("failed finalization changed balance: before=%+v after=%+v err=%v", before, after, err)
	}
	f.storage.fail = false
	w = finalizeRequest(t, f.service, f.message)
	if w.Code != http.StatusCreated || w.Body.String() != `{"message_id":"`+f.message.MessageID+`","status":"accepted"}` {
		t.Fatalf("retry after storage recovery = %d %s", w.Code, w.Body.String())
	}
}

func TestProviderFailureOvercomeByRetryIsNotRestored(t *testing.T) {
	f := newFinalizeFixture(t, "provider failure overcome post")
	afterDebit, err := f.service.Ledger.Balance(f.intent.AuthorID, faux.BitcoinMainnet)
	if err != nil {
		t.Fatal(err)
	}
	f.storage.fail = true
	if w := finalizeRequest(t, f.service, f.message); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("provider-caused failure = %d %s", w.Code, w.Body.String())
	}
	f.storage.fail = false
	t.Run("a retry inside the window is accepted", func(t *testing.T) {
		if w := finalizeRequest(t, f.service, f.message); w.Code != http.StatusCreated {
			t.Fatalf("retry after a recorded provider failure = %d %s", w.Code, w.Body.String())
		}
	})
	t.Run("the accepted message is never refunded", func(t *testing.T) {
		*f.clock = f.messageTime().Add(finalizeWindow)
		if err := finalizeRemedyRun(t, f.service, *f.clock); err != nil {
			t.Fatal(err)
		}
		if err := finalizeRemedyRun(t, f.service, *f.clock); err != nil {
			t.Fatal(err)
		}
		checkFinalizeBalance(t, f.service, f.intent.AuthorID, afterDebit.Available)
		if f.service.Ledger.Restored(f.intent.AuthorID, f.message.MessageID) {
			t.Fatal("Restored = true; want an accepted message kept unrefunded")
		}
	})
}

func TestProviderFailureRestoresFeeOnce(t *testing.T) {
	f := newFinalizeFixture(t, "provider failure post")
	afterDebit, err := f.service.Ledger.Balance(f.intent.AuthorID, faux.BitcoinMainnet)
	if err != nil {
		t.Fatal(err)
	}
	f.storage.fail = true
	if w := finalizeRequest(t, f.service, f.message); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("provider-caused failure = %d %s", w.Code, w.Body.String())
	}
	t.Run("the failure is durable before the reply", func(t *testing.T) {
		if !f.service.Ledger.ProviderFailed(f.intent.AuthorID, f.message.MessageID) {
			t.Fatal("ProviderFailed = false; want the failure recorded")
		}
	})
	t.Run("settlement never claims the failed receipt", func(t *testing.T) {
		f.storage.fail = false
		*f.clock = f.messageTime().Add(3 * time.Minute)
		if err := f.service.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, _, located, err := f.service.Settlement().Locate(f.message.SubmissionReceipt.ReceiptID); err != nil || located {
			t.Fatalf("Locate(provider-failed receipt) = %t, %v; want not located", located, err)
		}
	})
	t.Run("no credit before the window closes", func(t *testing.T) {
		*f.clock = f.messageTime().Add(finalizeWindow - time.Second)
		if err := finalizeRemedyRun(t, f.service, *f.clock); err != nil {
			t.Fatal(err)
		}
		checkFinalizeBalance(t, f.service, f.intent.AuthorID, afterDebit.Available)
	})
	t.Run("exactly one credit after the window", func(t *testing.T) {
		*f.clock = f.messageTime().Add(finalizeWindow)
		if err := finalizeRemedyRun(t, f.service, *f.clock); err != nil {
			t.Fatal(err)
		}
		checkFinalizeBalance(t, f.service, f.intent.AuthorID, new(big.Int).Add(afterDebit.Available, big.NewInt(136)))
	})
	t.Run("rerun does not credit again", func(t *testing.T) {
		if err := finalizeRemedyRun(t, f.service, *f.clock); err != nil {
			t.Fatal(err)
		}
		checkFinalizeBalance(t, f.service, f.intent.AuthorID, new(big.Int).Add(afterDebit.Available, big.NewInt(136)))
	})
	t.Run("finalization after the window is stale", func(t *testing.T) {
		f.storage.fail = false
		if w := finalizeRequest(t, f.service, f.message); w.Code != http.StatusConflict || w.Body.String() != `{"status":"stale_state"}` {
			t.Fatalf("finalization after the window = %d %s", w.Code, w.Body.String())
		}
	})
}

func TestProviderFailureRestoresFeeAfterIDCollision(t *testing.T) {
	f := newFinalizeFixture(t, "collision provider failure post")
	afterDebit, err := f.service.Ledger.Balance(f.intent.AuthorID, faux.BitcoinMainnet)
	if err != nil {
		t.Fatal(err)
	}
	messageBytes, err := canon.Encode(f.message)
	if err != nil {
		t.Fatal(err)
	}
	otherReceipt := keys.EncodeB64URL([]byte(strings.Repeat("o", 32)))
	if _, err = f.service.Records().Accept(Acceptance{MessageID: f.message.MessageID, Bytes: messageBytes, IntentDigest: sha256.Sum256([]byte("another intent")), ReceiptID: otherReceipt, IssuedAt: f.messageTime()}); err != nil {
		t.Fatal(err)
	}
	t.Run("colliding finalization is a provider-caused failure", func(t *testing.T) {
		if w := finalizeRequest(t, f.service, f.message); w.Code != http.StatusConflict || w.Body.String() != `{"status":"id_collision"}` {
			t.Fatalf("colliding finalization = %d %s", w.Code, w.Body.String())
		}
		if !f.service.Ledger.ProviderFailed(f.intent.AuthorID, f.message.MessageID) {
			t.Fatal("ProviderFailed = false; want the failure recorded")
		}
	})
	t.Run("exactly one credit after the window", func(t *testing.T) {
		*f.clock = f.messageTime().Add(finalizeWindow)
		if err := finalizeRemedyRun(t, f.service, *f.clock); err != nil {
			t.Fatal(err)
		}
		checkFinalizeBalance(t, f.service, f.intent.AuthorID, new(big.Int).Add(afterDebit.Available, big.NewInt(136)))
	})
	t.Run("rerun does not credit again", func(t *testing.T) {
		if err := finalizeRemedyRun(t, f.service, *f.clock); err != nil {
			t.Fatal(err)
		}
		checkFinalizeBalance(t, f.service, f.intent.AuthorID, new(big.Int).Add(afterDebit.Available, big.NewInt(136)))
	})
}

func TestProviderFailureSkipsClaimedReceipt(t *testing.T) {
	f := newFinalizeFixture(t, "claimed provider failure post")
	*f.clock = f.messageTime().Add(3 * time.Minute)
	if err := f.service.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, claimed, err := f.service.Settlement().Locate(f.message.SubmissionReceipt.ReceiptID); err != nil || !claimed {
		t.Fatalf("Locate(receipt) = %t, %v; want the receipt claimed before the failure", claimed, err)
	}
	f.storage.fail = true
	if w := finalizeRequest(t, f.service, f.message); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("finalization of a claimed receipt = %d %s", w.Code, w.Body.String())
	}
	if f.service.Ledger.ProviderFailed(f.intent.AuthorID, f.message.MessageID) {
		t.Fatal("ProviderFailed = true; want a claimed receipt left unmarked")
	}
}

func finalizeRemedyRun(t *testing.T, s *Service, now time.Time) error {
	t.Helper()
	for _, l := range s.loops {
		if l.name == "finalize-fee-remedies" {
			return l.fn(context.Background(), now)
		}
	}
	t.Fatal("finalize-fee-remedies loop is not registered")
	return nil
}

func checkFinalizeBalance(t *testing.T, s *Service, authorID string, want *big.Int) {
	t.Helper()
	b, err := s.Ledger.Balance(authorID, faux.BitcoinMainnet)
	if err != nil || b.Available.Cmp(want) != 0 {
		t.Fatalf("available = %v, %v; want %v", b.Available, err, want)
	}
}
