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
	scheduleID := "fyfs2:" + strings.Repeat("0", 64)
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
