package provider

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/delegation"
	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/fee"
	"foryou.local/foryou/internal/funding"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/message"
	"foryou.local/foryou/internal/wire"
)

type admissionTestFixture struct {
	service  *Service
	chain    *faux.Ledger
	key      keys.PrivateKey
	delegate keys.PrivateKey
	author   string
	now      time.Time
	setTime  func(time.Time)
}

func admissionNewFixture(t *testing.T) *admissionTestFixture {
	t.Helper()
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	setTime := func(t time.Time) { now = t }
	clock := func() time.Time { return now }
	cfg, err := TestConfig(clock)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	seed := sha256.Sum256([]byte("provider admission test author"))
	key := ed25519.NewKeyFromSeed(seed[:])
	f := &admissionTestFixture{service: s, chain: cfg.Faux.(*faux.Ledger), key: key, author: keys.DIDOf(key), now: now, setTime: setTime}
	f.admissionFund(t)
	return f
}

func (f *admissionTestFixture) admissionFund(t *testing.T) {
	t.Helper()
	request := wire.DepositRequest{ProviderID: f.service.ProviderID(), AuthorID: f.author, Network: faux.BitcoinMainnet, AmountChainUnits: "50000", RequestID: keys.EncodeB64URL([]byte("12345678901234567890123456789012")), NotAfter: wire.FormatTime(f.now.Add(time.Minute))}
	invoice, _, err := f.service.Ledger.IssueInvoice(request, big.NewInt(136_000_000), funding.Signer{Key: f.service.Key()})
	if err != nil {
		t.Fatal(err)
	}
	transfer, err := f.chain.Submit(context.Background(), faux.SubmitRequest{Network: faux.BitcoinMainnet, Recipient: invoice.Recipient, AmountChainUnits: "50000"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.chain.Mine(context.Background(), faux.BitcoinMainnet, 5); err != nil {
		t.Fatal(err)
	}
	index := int64(*transfer.OutputIndex)
	proof := wire.DepositProof{ProviderID: f.service.ProviderID(), AuthorID: f.author, DepositID: invoice.DepositID, Transfer: wire.DepositTransfer{Network: transfer.Network, TxID: transfer.TxID, OutputIndex: &index}}
	if _, _, err := f.service.Ledger.Observe(context.Background(), proof, f.chain, funding.Signer{Key: f.service.Key()}); err != nil {
		t.Fatal(err)
	}
}

func (f *admissionTestFixture) admissionIntent() wire.Intent {
	return wire.Intent{AuthorID: f.author, Timestamp: wire.FormatTime(f.now), MessageType: "post", Version: wire.Version{Major: 2, Minor: 0}, Payload: wire.PostPayload{ContentFormat: "text/plain", Text: "a paid post"}}
}

func (f *admissionTestFixture) admissionQuote(t *testing.T, intent wire.Intent) wire.FeeQuote {
	t.Helper()
	schedule, err := fee.InForce(f.service.FeeSchedules(), faux.BitcoinMainnet, f.now)
	if err != nil {
		t.Fatal(err)
	}
	id, err := message.ID(intent)
	if err != nil {
		t.Fatal(err)
	}
	authorization := fee.PreflightAuthorization(f.service.ProviderID(), f.author, id, schedule.ScheduleID)
	signature, err := fee.SignPreflight(authorization, f.admissionSigner(intent))
	if err != nil {
		t.Fatal(err)
	}
	intentBytes, err := canon.Encode(intent)
	if err != nil {
		t.Fatal(err)
	}
	body, err := canon.Encode(wire.FeePreflightRequest{ProviderID: f.service.ProviderID(), AuthorID: f.author, ScheduleID: schedule.ScheduleID, Intent: intentBytes, Authorization: signature})
	if err != nil {
		t.Fatal(err)
	}
	response := admissionServe(f.service, "POST", "/v1/fee-quotes", body)
	if response.Code != http.StatusOK {
		t.Fatalf("fee quote = %d %s", response.Code, response.Body.String())
	}
	var quote wire.FeeQuote
	if err := json.Unmarshal(response.Body.Bytes(), &quote); err != nil {
		t.Fatal(err)
	}
	return quote
}

func (f *admissionTestFixture) admissionRequest(t *testing.T, intent wire.Intent, quoteID string) []byte {
	t.Helper()
	id, err := message.ID(intent)
	if err != nil {
		t.Fatal(err)
	}
	authorization := fee.AdmissionAuthorization(f.service.ProviderID(), f.author, id, quoteID)
	signature, err := fee.SignAdmission(authorization, f.admissionSigner(intent))
	if err != nil {
		t.Fatal(err)
	}
	body, err := canon.Encode(wire.AdmissionRequest{ProviderID: f.service.ProviderID(), Intent: intent, Authorization: signature, QuoteID: quoteID})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func (f *admissionTestFixture) admissionSigner(intent wire.Intent) keys.PrivateKey {
	if intent.Delegation != nil {
		return f.delegate
	}
	return f.key
}

func (f *admissionTestFixture) delegatedAdmissionIntent(t *testing.T, validUntil time.Time) wire.Intent {
	t.Helper()
	delegateSeed := sha256.Sum256([]byte("provider admission delegate"))
	f.delegate = ed25519.NewKeyFromSeed(delegateSeed[:])
	enrollment, err := delegation.SignEnroll(f.key, wire.AuthorizationEpochEnroll{ProviderID: f.service.ProviderID(), RequestNonce: keys.EncodeB64URL([]byte(strings.Repeat("n", 32))), NotAfter: wire.FormatTime(f.now.Add(time.Minute))})
	if err != nil {
		t.Fatal(err)
	}
	state, _, err := f.service.Authority.Enroll(f.author, enrollment)
	if err != nil {
		t.Fatal(err)
	}
	delegated, err := delegation.SignDelegation(f.key, wire.Delegation{AuthorID: f.author, SignerID: keys.DIDOf(f.delegate), ProviderID: f.service.ProviderID(), Scope: []string{"post"}, Epoch: *state.Epoch, ValidFrom: wire.FormatTime(f.now), ValidUntil: wire.FormatTime(validUntil)})
	if err != nil {
		t.Fatal(err)
	}
	intent := f.admissionIntent()
	intent.SignerID = delegated.SignerID
	intent.Delegation = &delegated
	return intent
}

func admissionServe(s *Service, method, path string, body []byte) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(string(body)))
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, request)
	return response
}

func TestAdmissionFeeQuote(t *testing.T) {
	t.Run("valid preflight returns verified quote", func(t *testing.T) {
		f := admissionNewFixture(t)
		intent := f.admissionIntent()
		quote := f.admissionQuote(t, intent)
		id, _ := message.ID(intent)
		canonical, _ := canon.Encode(intent)
		if err := fee.CheckQuote(quote, f.service.FeeSchedules(), fee.Expectation{ProviderID: f.service.ProviderID(), AuthorID: f.author, MessageID: id, BillableBytes: len(canonical)}, f.now); err != nil {
			t.Fatalf("CheckQuote: %v", err)
		}
		if quote.BillableBytes != int64(len(canonical)) {
			t.Errorf("billable_bytes = %d; want %d", quote.BillableBytes, len(canonical))
		}
		if quote.AmountBaseUnits != "136" {
			t.Errorf("amount = %s; want 136 msat", quote.AmountBaseUnits)
		}
	})
	t.Run("wrong schedule is stale", func(t *testing.T) {
		f := admissionNewFixture(t)
		intent := f.admissionIntent()
		id, _ := message.ID(intent)
		badSchedule := "fyfs2:" + strings.Repeat("0", 64)
		authorization, _ := fee.SignPreflight(fee.PreflightAuthorization(f.service.ProviderID(), f.author, id, badSchedule), f.key)
		intentBytes, _ := canon.Encode(intent)
		req := wire.FeePreflightRequest{ProviderID: f.service.ProviderID(), AuthorID: f.author, ScheduleID: badSchedule, Intent: intentBytes, Authorization: authorization}
		body, _ := canon.Encode(req)
		response := admissionServe(f.service, "POST", "/v1/fee-quotes", body)
		if response.Code != http.StatusConflict || response.Body.String() != `{"status":"stale_state"}` {
			t.Fatalf("response = %d %s", response.Code, response.Body.String())
		}
	})
	t.Run("timestamp outside the admission window is invalid", func(t *testing.T) {
		f := admissionNewFixture(t)
		intent := f.admissionIntent()
		intent.Timestamp = wire.FormatTime(f.now.Add(-301 * time.Second))
		body := admissionQuoteBody(t, f, intent)
		response := admissionServe(f.service, "POST", "/v1/fee-quotes", body)
		if response.Code != http.StatusUnprocessableEntity || response.Body.String() != `{"status":"invalid_message"}` {
			t.Fatalf("response = %d %s", response.Code, response.Body.String())
		}
	})
	t.Run("bad preflight signature is rejected", func(t *testing.T) {
		f := admissionNewFixture(t)
		intent := f.admissionIntent()
		schedule, _ := fee.InForce(f.service.FeeSchedules(), faux.BitcoinMainnet, f.now)
		intentBytes, _ := canon.Encode(intent)
		body, _ := canon.Encode(wire.FeePreflightRequest{ProviderID: f.service.ProviderID(), AuthorID: f.author, ScheduleID: schedule.ScheduleID, Intent: intentBytes, Authorization: strings.Repeat("A", 86)})
		response := admissionServe(f.service, "POST", "/v1/fee-quotes", body)
		if response.Code != http.StatusUnprocessableEntity || response.Body.String() != `{"status":"bad_authorization"}` {
			t.Fatalf("response = %d %s", response.Code, response.Body.String())
		}
	})
	t.Run("intent with an unknown field is invalid", func(t *testing.T) {
		f := admissionNewFixture(t)
		body := strings.Replace(string(admissionQuoteBody(t, f, f.admissionIntent())), `"intent":{`, `"intent":{"extra":1,`, 1)
		response := admissionServe(f.service, "POST", "/v1/fee-quotes", []byte(body))
		if response.Code != http.StatusUnprocessableEntity || response.Body.String() != `{"status":"invalid_message"}` {
			t.Fatalf("response = %d %s", response.Code, response.Body.String())
		}
	})
	t.Run("userinfo media URL is rejected", func(t *testing.T) {
		f := admissionNewFixture(t)
		intent := f.admissionIntent()
		intent.Payload.MediaSlots = &[]wire.MediaReference{{ID: "photo", Kind: "image", URL: "https://user@example.com/a.png", SHA256: strings.Repeat("a", 64)}}
		response := admissionServe(f.service, "POST", "/v1/fee-quotes", admissionQuoteBody(t, f, intent))
		if response.Code != http.StatusUnprocessableEntity || response.Body.String() != `{"status":"invalid_message"}` {
			t.Fatalf("response = %d %s", response.Code, response.Body.String())
		}
	})
}

func TestAdmissionDelegationGate(t *testing.T) {
	t.Run("delegated admission uses current authorization epoch", func(t *testing.T) {
		f := admissionNewFixture(t)
		intent := f.delegatedAdmissionIntent(t, f.now.Add(time.Hour))
		quote := f.admissionQuote(t, intent)
		before, err := f.service.Ledger.Balance(f.author, faux.BitcoinMainnet)
		if err != nil {
			t.Fatal(err)
		}
		response := admissionServe(f.service, "POST", "/v1/admissions", f.admissionRequest(t, intent, quote.QuoteID))
		if response.Code != http.StatusCreated {
			t.Fatalf("delegated admission = %d %s", response.Code, response.Body.String())
		}
		after, err := f.service.Ledger.Balance(f.author, faux.BitcoinMainnet)
		if err != nil {
			t.Fatal(err)
		}
		if quote.AmountBaseUnits != "272" || new(big.Int).Sub(before.Available, after.Available).Cmp(big.NewInt(272)) != 0 {
			t.Fatalf("delegated quote/debit = %s/%s for %d billable bytes; want 272 msat for two started KiB", quote.AmountBaseUnits, new(big.Int).Sub(before.Available, after.Available), quote.BillableBytes)
		}
	})
	t.Run("revoked delegation is rejected without debit", func(t *testing.T) {
		f := admissionNewFixture(t)
		intent := f.delegatedAdmissionIntent(t, f.now.Add(time.Hour))
		quote := f.admissionQuote(t, intent)
		revocation, err := delegation.SignRevocation(f.key, wire.RevocationRequest{ProviderID: f.service.ProviderID(), DelegationID: intent.Delegation.DelegationID, Delegation: *intent.Delegation})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.service.Authority.Revoke(f.author, revocation); err != nil {
			t.Fatal(err)
		}
		before, err := f.service.Ledger.Balance(f.author, faux.BitcoinMainnet)
		if err != nil {
			t.Fatal(err)
		}
		response := admissionServe(f.service, "POST", "/v1/admissions", f.admissionRequest(t, intent, quote.QuoteID))
		if response.Code != http.StatusForbidden || response.Body.String() != `{"status":"delegation_revoked"}` {
			t.Fatalf("revoked delegation = %d %s", response.Code, response.Body.String())
		}
		after, err := f.service.Ledger.Balance(f.author, faux.BitcoinMainnet)
		if err != nil {
			t.Fatal(err)
		}
		if before.Available.Cmp(after.Available) != 0 {
			t.Fatalf("balance changed from %s to %s", before.Available, after.Available)
		}
	})
	t.Run("expired delegation is rejected without debit", func(t *testing.T) {
		f := admissionNewFixture(t)
		intent := f.delegatedAdmissionIntent(t, f.now.Add(5*time.Second))
		quote := f.admissionQuote(t, intent)
		before, err := f.service.Ledger.Balance(f.author, faux.BitcoinMainnet)
		if err != nil {
			t.Fatal(err)
		}
		f.setTime(f.now.Add(10 * time.Second))
		response := admissionServe(f.service, "POST", "/v1/admissions", f.admissionRequest(t, intent, quote.QuoteID))
		if response.Code != http.StatusUnprocessableEntity || response.Body.String() != `{"status":"bad_authorization"}` {
			t.Fatalf("expired delegation = %d %s", response.Code, response.Body.String())
		}
		after, err := f.service.Ledger.Balance(f.author, faux.BitcoinMainnet)
		if err != nil {
			t.Fatal(err)
		}
		if before.Available.Cmp(after.Available) != 0 {
			t.Fatalf("balance changed from %s to %s", before.Available, after.Available)
		}
	})
}

func TestAdmissionQuoteEviction(t *testing.T) {
	t.Run("expired quote is evicted by the registered task", func(t *testing.T) {
		f := admissionNewFixture(t)
		intent := f.admissionIntent()
		quote := f.admissionQuote(t, intent)
		before, err := f.service.Ledger.Balance(f.author, faux.BitcoinMainnet)
		if err != nil {
			t.Fatal(err)
		}
		expires, _ := wire.ParseTime(quote.ExpiresAt)
		f.setTime(expires.Add(time.Second))
		if err := f.service.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		response := admissionServe(f.service, "POST", "/v1/admissions", f.admissionRequest(t, intent, quote.QuoteID))
		if response.Code != http.StatusConflict || response.Body.String() != `{"status":"quote_conflict"}` {
			t.Fatalf("evicted quote response = %d %s", response.Code, response.Body.String())
		}
		after, err := f.service.Ledger.Balance(f.author, faux.BitcoinMainnet)
		if err != nil {
			t.Fatal(err)
		}
		if before.Available.Cmp(after.Available) != 0 {
			t.Fatalf("balance changed from %s to %s", before.Available, after.Available)
		}
	})
}

func TestAdmissionKnownCrossAuthorTarget(t *testing.T) {
	t.Run("held cross-author target blocks a quote", func(t *testing.T) {
		f := admissionNewFixture(t)
		targetBytes := loadExample(t, 0)
		var target wire.PostMessage
		if err := json.Unmarshal(targetBytes, &target); err != nil {
			t.Fatal(err)
		}
		if _, err := f.service.Records().Accept(Acceptance{MessageID: target.MessageID, Bytes: targetBytes, IntentDigest: sha256.Sum256(targetBytes), ReceiptID: "admission-test-target", IssuedAt: f.now}); err != nil {
			t.Fatal(err)
		}
		intent := f.admissionIntent()
		intent.Payload.Supersedes = target.MessageID
		response := admissionServe(f.service, "POST", "/v1/fee-quotes", admissionQuoteBody(t, f, intent))
		if response.Code != http.StatusUnprocessableEntity || response.Body.String() != `{"status":"invalid_message"}` {
			t.Fatalf("cross-author target = %d %s", response.Code, response.Body.String())
		}
	})
}

func admissionQuoteBody(t *testing.T, f *admissionTestFixture, intent wire.Intent) []byte {
	t.Helper()
	schedule, _ := fee.InForce(f.service.FeeSchedules(), faux.BitcoinMainnet, f.now)
	id, _ := message.ID(intent)
	authorization, err := fee.SignPreflight(fee.PreflightAuthorization(f.service.ProviderID(), f.author, id, schedule.ScheduleID), f.key)
	if err != nil {
		t.Fatal(err)
	}
	intentBytes, err := canon.Encode(intent)
	if err != nil {
		t.Fatal(err)
	}
	body, err := canon.Encode(wire.FeePreflightRequest{ProviderID: f.service.ProviderID(), AuthorID: f.author, ScheduleID: schedule.ScheduleID, Intent: intentBytes, Authorization: authorization})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestAdmissionCommitAndRetry(t *testing.T) {
	t.Run("new admission debits once and retry recovers original receipt", func(t *testing.T) {
		f := admissionNewFixture(t)
		intent := f.admissionIntent()
		quote := f.admissionQuote(t, intent)
		before, _ := f.service.Ledger.Balance(f.author, faux.BitcoinMainnet)
		body := f.admissionRequest(t, intent, quote.QuoteID)
		first := admissionServe(f.service, "POST", "/v1/admissions", body)
		if first.Code != http.StatusCreated {
			t.Fatalf("first admission = %d %s", first.Code, first.Body.String())
		}
		var receipt wire.SubmissionReceipt
		if err := json.Unmarshal(first.Body.Bytes(), &receipt); err != nil {
			t.Fatal(err)
		}
		if err := fee.VerifyReceipt(receipt); err != nil {
			t.Fatalf("VerifyReceipt: %v", err)
		}
		if err := fee.CheckReceipt(receipt, quote); err != nil {
			t.Fatalf("CheckReceipt: %v", err)
		}
		after, _ := f.service.Ledger.Balance(f.author, faux.BitcoinMainnet)
		if new(big.Int).Sub(before.Available, after.Available).Cmp(big.NewInt(136)) != 0 {
			t.Fatalf("balance delta = %s; want 136", new(big.Int).Sub(before.Available, after.Available))
		}
		retry := admissionServe(f.service, "POST", "/v1/admissions", body)
		if retry.Code != http.StatusOK || retry.Body.String() != first.Body.String() {
			t.Fatalf("retry = %d %s; want original %s", retry.Code, retry.Body.String(), first.Body.String())
		}
		afterRetry, _ := f.service.Ledger.Balance(f.author, faux.BitcoinMainnet)
		if afterRetry.Available.Cmp(after.Available) != 0 {
			t.Fatalf("retry balance = %s; want %s", afterRetry.Available, after.Available)
		}
		f.setTime(f.now.Add(time.Second))
		secondQuote := f.admissionQuote(t, intent)
		secondQuoteRetry := admissionServe(f.service, "POST", "/v1/admissions", f.admissionRequest(t, intent, secondQuote.QuoteID))
		if secondQuote.QuoteID == quote.QuoteID || secondQuoteRetry.Code != http.StatusOK || secondQuoteRetry.Body.String() != first.Body.String() {
			t.Fatalf("different-quote retry = %s, %d %s", secondQuote.QuoteID, secondQuoteRetry.Code, secondQuoteRetry.Body.String())
		}
	})
	t.Run("racing same message with different quotes only debits once", func(t *testing.T) {
		f := admissionNewFixture(t)
		intent := f.admissionIntent()
		firstQuote := f.admissionQuote(t, intent)
		f.setTime(f.now.Add(time.Second))
		secondQuote := f.admissionQuote(t, intent)
		if firstQuote.QuoteID == secondQuote.QuoteID {
			t.Fatal("expected quote IDs to differ")
		}
		before, _ := f.service.Ledger.Balance(f.author, faux.BitcoinMainnet)
		firstBody := f.admissionRequest(t, intent, firstQuote.QuoteID)
		secondBody := f.admissionRequest(t, intent, secondQuote.QuoteID)
		start := make(chan struct{})
		responses := make(chan *httptest.ResponseRecorder, 2)
		var wait sync.WaitGroup
		wait.Add(2)
		go func() {
			defer wait.Done()
			<-start
			responses <- admissionServe(f.service, "POST", "/v1/admissions", firstBody)
		}()
		go func() {
			defer wait.Done()
			<-start
			responses <- admissionServe(f.service, "POST", "/v1/admissions", secondBody)
		}()
		close(start)
		wait.Wait()
		firstResponse := <-responses
		secondResponse := <-responses
		createdCount := 0
		if firstResponse.Code == http.StatusCreated {
			createdCount++
		}
		if secondResponse.Code == http.StatusCreated {
			createdCount++
		}
		if createdCount != 1 || firstResponse.Code != http.StatusCreated && firstResponse.Code != http.StatusOK || secondResponse.Code != http.StatusCreated && secondResponse.Code != http.StatusOK || firstResponse.Body.String() != secondResponse.Body.String() {
			t.Fatalf("racing responses = %d %s; %d %s", firstResponse.Code, firstResponse.Body.String(), secondResponse.Code, secondResponse.Body.String())
		}
		after, _ := f.service.Ledger.Balance(f.author, faux.BitcoinMainnet)
		if new(big.Int).Sub(before.Available, after.Available).Cmp(big.NewInt(136)) != 0 {
			t.Fatalf("balance delta = %s; want 136", new(big.Int).Sub(before.Available, after.Available))
		}
	})
	t.Run("capacity refusal leaves balance unchanged", func(t *testing.T) {
		f := admissionNewFixture(t)
		intent := f.admissionIntent()
		quote := f.admissionQuote(t, intent)
		before, _ := f.service.Ledger.Balance(f.author, faux.BitcoinMainnet)
		previous := admissionCapacity
		admissionCapacity = func(*Service, int) (int, bool) { return 17, false }
		t.Cleanup(func() { admissionCapacity = previous })
		response := admissionServe(f.service, "POST", "/v1/admissions", f.admissionRequest(t, intent, quote.QuoteID))
		if response.Code != http.StatusServiceUnavailable || response.Body.String() != `{"retry_after_seconds":17,"status":"unavailable"}` {
			t.Fatalf("response = %d %s", response.Code, response.Body.String())
		}
		after, _ := f.service.Ledger.Balance(f.author, faux.BitcoinMainnet)
		if before.Available.Cmp(after.Available) != 0 {
			t.Fatalf("balance changed from %s to %s", before.Available, after.Available)
		}
	})
	t.Run("mismatched quote leaves balance unchanged", func(t *testing.T) {
		f := admissionNewFixture(t)
		intent := f.admissionIntent()
		quote := f.admissionQuote(t, intent)
		other := intent
		other.Payload.Text = "different intent"
		before, _ := f.service.Ledger.Balance(f.author, faux.BitcoinMainnet)
		response := admissionServe(f.service, "POST", "/v1/admissions", f.admissionRequest(t, other, quote.QuoteID))
		if response.Code != http.StatusConflict || response.Body.String() != `{"status":"quote_conflict"}` {
			t.Fatalf("response = %d %s", response.Code, response.Body.String())
		}
		after, _ := f.service.Ledger.Balance(f.author, faux.BitcoinMainnet)
		if before.Available.Cmp(after.Available) != 0 {
			t.Fatalf("balance changed from %s to %s", before.Available, after.Available)
		}
	})
	t.Run("expired quote is rejected and committed retry recovers", func(t *testing.T) {
		f := admissionNewFixture(t)
		intent := f.admissionIntent()
		quote := f.admissionQuote(t, intent)
		body := f.admissionRequest(t, intent, quote.QuoteID)
		first := admissionServe(f.service, "POST", "/v1/admissions", body)
		if first.Code != http.StatusCreated {
			t.Fatalf("first admission = %d %s", first.Code, first.Body.String())
		}
		before, _ := f.service.Ledger.Balance(f.author, faux.BitcoinMainnet)
		expires, _ := wire.ParseTime(quote.ExpiresAt)
		f.setTime(expires.Add(time.Second))
		retry := admissionServe(f.service, "POST", "/v1/admissions", body)
		if retry.Code != http.StatusOK || retry.Body.String() != first.Body.String() {
			t.Fatalf("retry after expiry = %d %s", retry.Code, retry.Body.String())
		}
		after, _ := f.service.Ledger.Balance(f.author, faux.BitcoinMainnet)
		if before.Available.Cmp(after.Available) != 0 {
			t.Fatalf("balance changed from %s to %s", before.Available, after.Available)
		}
	})
	t.Run("expired quote for new work leaves balance unchanged", func(t *testing.T) {
		f := admissionNewFixture(t)
		intent := f.admissionIntent()
		quote := f.admissionQuote(t, intent)
		before, _ := f.service.Ledger.Balance(f.author, faux.BitcoinMainnet)
		expires, _ := wire.ParseTime(quote.ExpiresAt)
		f.setTime(expires.Add(time.Second))
		response := admissionServe(f.service, "POST", "/v1/admissions", f.admissionRequest(t, intent, quote.QuoteID))
		if response.Code != http.StatusUnprocessableEntity || response.Body.String() != `{"status":"quote_expired"}` {
			t.Fatalf("response = %d %s", response.Code, response.Body.String())
		}
		after, _ := f.service.Ledger.Balance(f.author, faux.BitcoinMainnet)
		if before.Available.Cmp(after.Available) != 0 {
			t.Fatalf("balance changed from %s to %s", before.Available, after.Available)
		}
	})
	t.Run("unfunded author receives payment required", func(t *testing.T) {
		f := admissionNewFixture(t)
		seed := sha256.Sum256([]byte("provider admission unfunded author"))
		f.key = ed25519.NewKeyFromSeed(seed[:])
		f.author = keys.DIDOf(f.key)
		intent := f.admissionIntent()
		quote := f.admissionQuote(t, intent)
		before, _ := f.service.Ledger.Balance(f.author, faux.BitcoinMainnet)
		response := admissionServe(f.service, "POST", "/v1/admissions", f.admissionRequest(t, intent, quote.QuoteID))
		if response.Code != http.StatusPaymentRequired || response.Body.String() != `{"status":"insufficient_balance"}` {
			t.Fatalf("response = %d %s", response.Code, response.Body.String())
		}
		after, _ := f.service.Ledger.Balance(f.author, faux.BitcoinMainnet)
		if before.Available.Cmp(after.Available) != 0 || after.Available.Sign() != 0 {
			t.Fatalf("unfunded balance changed from %s to %s", before.Available, after.Available)
		}
	})
	t.Run("userinfo media URL is rejected without debit", func(t *testing.T) {
		f := admissionNewFixture(t)
		intent := f.admissionIntent()
		intent.Payload.MediaSlots = &[]wire.MediaReference{{ID: "photo", Kind: "image", URL: "https://user@example.com/a.png", SHA256: strings.Repeat("a", 64)}}
		before, _ := f.service.Ledger.Balance(f.author, faux.BitcoinMainnet)
		response := admissionServe(f.service, "POST", "/v1/admissions", f.admissionRequest(t, intent, "fyfq2:"+strings.Repeat("b", 64)))
		if response.Code != http.StatusUnprocessableEntity || response.Body.String() != `{"status":"invalid_message"}` {
			t.Fatalf("response = %d %s", response.Code, response.Body.String())
		}
		after, _ := f.service.Ledger.Balance(f.author, faux.BitcoinMainnet)
		if before.Available.Cmp(after.Available) != 0 {
			t.Fatalf("balance changed from %s to %s", before.Available, after.Available)
		}
	})
}

func TestAdmissionLookupRequiresServiceAuth(t *testing.T) {
	t.Run("missing auth is unauthorized", func(t *testing.T) {
		f := admissionNewFixture(t)
		response := admissionServe(f.service, "GET", "/v1/admissions/fy1:"+strings.Repeat("a", 64), nil)
		if response.Code != http.StatusUnauthorized || response.Body.String() != `{"status":"unauthenticated"}` {
			t.Fatalf("response = %d %s", response.Code, response.Body.String())
		}
	})
	t.Run("authenticated author sees own receipt and not another authors", func(t *testing.T) {
		f := admissionNewFixture(t)
		intent := f.admissionIntent()
		quote := f.admissionQuote(t, intent)
		admitted := admissionServe(f.service, "POST", "/v1/admissions", f.admissionRequest(t, intent, quote.QuoteID))
		if admitted.Code != http.StatusCreated {
			t.Fatalf("admission = %d %s", admitted.Code, admitted.Body.String())
		}
		messageID, _ := message.ID(intent)
		lookup := func(authorID string, key keys.PrivateKey, id string) *httptest.ResponseRecorder {
			resource := "/v1/admissions/" + id
			challenge, err := f.service.ServiceAuth.Issue("127.0.0.1", delegation.ChallengeRequest{AuthorID: authorID, Method: "GET", Resource: resource, BodyHash: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"})
			if err != nil {
				t.Fatal(err)
			}
			challengeHeader, signature, err := delegation.SignChallenge(key, challenge)
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest("GET", resource, nil)
			request.Header.Set(delegation.HeaderChallenge, challengeHeader)
			request.Header.Set(delegation.HeaderSignature, signature)
			response := httptest.NewRecorder()
			f.service.Handler().ServeHTTP(response, request)
			return response
		}
		own := lookup(f.author, f.key, messageID)
		if own.Code != http.StatusOK || own.Body.String() != admitted.Body.String() {
			t.Fatalf("own lookup = %d %s", own.Code, own.Body.String())
		}
		otherSeed := sha256.Sum256([]byte("provider admission other author"))
		otherKey := ed25519.NewKeyFromSeed(otherSeed[:])
		otherAuthor := keys.DIDOf(otherKey)
		other := lookup(otherAuthor, otherKey, messageID)
		if other.Code != http.StatusNotFound || other.Body.String() != `{"status":"not_found"}` {
			t.Fatalf("other-author lookup = %d %s", other.Code, other.Body.String())
		}
	})
}
