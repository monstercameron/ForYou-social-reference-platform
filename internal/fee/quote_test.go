package fee

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

type feeExample struct {
	Schedule wire.FeeSchedule `json:"schedule"`
	Quotes   []wire.FeeQuote  `json:"quotes"`
	Message  struct {
		SubmissionReceipt wire.SubmissionReceipt `json:"submission_receipt"`
	} `json:"message"`
}

func readFeeExample(t *testing.T) feeExample {
	t.Helper()
	b, err := os.ReadFile("testdata/post-with-media-slots.json")
	if err != nil {
		t.Fatal(err)
	}
	var v feeExample
	if err = json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func exampleProviderKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(i + 32)
	}
	key, err := keys.NewKeyFromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestQuoteVector(t *testing.T) {
	v := readFeeExample(t)
	q := v.Quotes[0]
	t.Run("example ID and signature verify", func(t *testing.T) {
		id, err := QuoteID(q)
		if err != nil || id != "fyfq2:8c82b88a34442ea0d0c45ff83c52852bcfb55b3f85d5cc94cb895ba4afa4bec2" {
			t.Fatalf("QuoteID = %s, %v", id, err)
		}
		if err := VerifyQuote(q); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("issue reproduces vector", func(t *testing.T) {
		r := QuoteRequest{ProviderID: q.ProviderID, AuthorID: q.AuthorID, MessageID: q.MessageID, BillableBytes: 602, PreflightAt: time.Date(2026, 11, 11, 0, 0, 4, 0, time.UTC), ExpiresAt: time.Date(2026, 11, 11, 0, 10, 0, 0, time.UTC)}
		got, err := IssueQuote(r, v.Schedule, []wire.FeeSchedule{v.Schedule}, exampleProviderKey(t))
		if err != nil {
			t.Fatal(err)
		}
		if got != q {
			t.Fatalf("IssueQuote = %#v; want %#v", got, q)
		}
	})
	t.Run("client rejects changed amount", func(t *testing.T) {
		bad := q
		bad.AmountBaseUnits = "3"
		bad.QuoteID, _ = QuoteID(bad)
		canonical, err := canon.EncodeWithout(bad, "provider_signature")
		if err != nil {
			t.Fatal(err)
		}
		bad.ProviderSignature, err = keys.Sign(exampleProviderKey(t), "fee-quote", canonical)
		if err != nil {
			t.Fatal(err)
		}
		if err := CheckQuote(bad, []wire.FeeSchedule{v.Schedule}, Expectation{q.ProviderID, q.AuthorID, q.MessageID, 602}, time.Date(2026, 11, 11, 0, 0, 5, 0, time.UTC)); !errors.Is(err, ErrQuoteMismatch) {
			t.Fatalf("CheckQuote error = %v; want ErrQuoteMismatch", err)
		}
	})
	t.Run("expiry beyond one hour rejected", func(t *testing.T) {
		r := QuoteRequest{ProviderID: q.ProviderID, AuthorID: q.AuthorID, MessageID: q.MessageID, BillableBytes: 602, PreflightAt: time.Date(2026, 11, 11, 0, 0, 4, 0, time.UTC), ExpiresAt: time.Date(2026, 11, 11, 1, 0, 5, 0, time.UTC)}
		if _, err := IssueQuote(r, v.Schedule, []wire.FeeSchedule{v.Schedule}, exampleProviderKey(t)); err == nil {
			t.Fatal("overlong quote accepted")
		}
	})
	t.Run("client accepts unmodified quote before expiry", func(t *testing.T) {
		if err := CheckQuote(q, []wire.FeeSchedule{v.Schedule}, Expectation{q.ProviderID, q.AuthorID, q.MessageID, 602}, time.Date(2026, 11, 11, 0, 9, 59, 0, time.UTC)); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("client rejects quote at expiry", func(t *testing.T) {
		err := CheckQuote(q, []wire.FeeSchedule{v.Schedule}, Expectation{q.ProviderID, q.AuthorID, q.MessageID, 602}, time.Date(2026, 11, 11, 0, 10, 0, 0, time.UTC))
		if !errors.Is(err, ErrQuoteExpired) {
			t.Fatalf("CheckQuote error = %v; want ErrQuoteExpired", err)
		}
	})
	t.Run("client rejects different message ID", func(t *testing.T) {
		err := CheckQuote(q, []wire.FeeSchedule{v.Schedule}, Expectation{q.ProviderID, q.AuthorID, "fy1:different", 602}, time.Date(2026, 11, 11, 0, 0, 5, 0, time.UTC))
		if !errors.Is(err, ErrQuoteMismatch) {
			t.Fatalf("CheckQuote error = %v; want ErrQuoteMismatch", err)
		}
	})
	t.Run("client rejects different billable bytes", func(t *testing.T) {
		err := CheckQuote(q, []wire.FeeSchedule{v.Schedule}, Expectation{q.ProviderID, q.AuthorID, q.MessageID, 603}, time.Date(2026, 11, 11, 0, 0, 5, 0, time.UTC))
		if !errors.Is(err, ErrQuoteMismatch) {
			t.Fatalf("CheckQuote error = %v; want ErrQuoteMismatch", err)
		}
	})
	t.Run("subsecond timestamps are checked after truncation", func(t *testing.T) {
		r := QuoteRequest{ProviderID: q.ProviderID, AuthorID: q.AuthorID, MessageID: q.MessageID, BillableBytes: 602, PreflightAt: time.Date(2026, 11, 11, 0, 0, 4, 500000000, time.UTC), ExpiresAt: time.Date(2026, 11, 11, 0, 0, 4, 900000000, time.UTC)}
		if _, err := IssueQuote(r, v.Schedule, []wire.FeeSchedule{v.Schedule}, exampleProviderKey(t)); err == nil {
			t.Fatal("quote with equal serialized timestamps accepted")
		}
	})
	t.Run("missing schedule list rejected", func(t *testing.T) {
		r := QuoteRequest{ProviderID: q.ProviderID, AuthorID: q.AuthorID, MessageID: q.MessageID, BillableBytes: 602, PreflightAt: time.Date(2026, 11, 11, 0, 0, 4, 0, time.UTC), ExpiresAt: time.Date(2026, 11, 11, 0, 10, 0, 0, time.UTC)}
		if _, err := IssueQuote(r, v.Schedule, nil, exampleProviderKey(t)); err == nil {
			t.Fatal("quote issued without an in-force schedule")
		}
	})
}
