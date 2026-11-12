package sdk

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"foryou.local/foryou/internal/apierr"
	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/delegation"
	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/fee"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/message"
	"foryou.local/foryou/internal/wire"
)

var pbAuthorSeed = []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31}

var pbProviderSeed = []byte{32, 33, 34, 35, 36, 37, 38, 39, 40, 41, 42, 43, 44, 45, 46, 47, 48, 49, 50, 51, 52, 53, 54, 55, 56, 57, 58, 59, 60, 61, 62, 63}

// pbFakeProvider answers the quote, admission and message routes as the protocol describes, built from internal/fee and internal/message.
type pbFakeProvider struct {
	t             *testing.T
	now           time.Time
	root          *TrustRoot
	provider      ed25519.PrivateKey
	quoteSigner   ed25519.PrivateKey
	quoteChange   func(wire.FeeQuote) wire.FeeQuote
	rejectWith    apierr.Status
	hangAdmission bool
	hangMessage   bool
	available     string
	auth          *delegation.ServiceAuth
	mu            sync.Mutex
	quotes        map[string]wire.FeeQuote
	receipts      map[string]wire.SubmissionReceipt
	debits        map[string]int
	admissions    [][]byte
	messages      [][]byte
	server        *httptest.Server
}

func pbSetup(t *testing.T, now time.Time, timeout time.Duration) (*Publisher, ed25519.PrivateKey, *pbFakeProvider) {
	t.Helper()
	author, err := keys.NewKeyFromSeed(pbAuthorSeed)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := keys.NewKeyFromSeed(pbProviderSeed)
	if err != nil {
		t.Fatal(err)
	}
	genesis := devnet.Genesis()
	root, err := BuildTrustRoot(Pin{ReleaseKeyIDs: genesis.ReleaseKeyIDs, Threshold: int(genesis.ReleaseThreshold)}, Publications{Genesis: genesis, FetchedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return now }
	auth, err := delegation.NewServiceAuth(delegation.ServiceAuthConfig{ServiceID: keys.DIDOf(provider), Store: delegation.NewMemoryChallengeStore(), Now: clock, Limits: delegation.Limits{MaxOutstanding: 64, MaxPerSource: 64}})
	if err != nil {
		t.Fatal(err)
	}
	fake := &pbFakeProvider{t: t, now: now, provider: provider, root: root, available: "10000000", auth: auth, quotes: map[string]wire.FeeQuote{}, receipts: map[string]wire.SubmissionReceipt{}, debits: map[string]int{}}
	fake.server = httptest.NewServer(http.HandlerFunc(fake.serveHTTP))
	t.Cleanup(fake.server.Close)
	client, err := New(fake.server.URL, Options{ServiceID: keys.DIDOf(provider), AllowLoopbackHTTP: true, Timeout: timeout, Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	journal, err := FileJournal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return NewPublisher(client, root, journal, func() time.Time { return now }), author, fake
}

func (f *pbFakeProvider) serveHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	body := pbReadBody(f.t, r)
	switch {
	case r.URL.Path == "/v1/fee-quotes":
		f.serveQuote(w, body)
	case r.URL.Path == "/v1/admissions":
		f.serveAdmission(w, r, body)
	case r.URL.Path == "/v1/messages":
		f.serveMessage(w, r, body)
	case r.URL.Path == "/v1/service-challenges":
		f.serveChallenge(w, body)
	case strings.HasPrefix(r.URL.Path, "/v1/balances/"):
		f.serveBalance(w, r)
	default:
		f.t.Errorf("unexpected path %s", r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *pbFakeProvider) serveChallenge(w http.ResponseWriter, body []byte) {
	request, err := delegation.ParseChallengeRequest(body)
	if err != nil {
		f.t.Errorf("decode challenge request: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	challenge, err := f.auth.Issue("test", request)
	if err != nil {
		f.t.Error(err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	pbWriteJSON(f.t, w, http.StatusOK, challenge)
}

func (f *pbFakeProvider) serveBalance(w http.ResponseWriter, r *http.Request) {
	author, err := f.auth.Verify(delegation.NewAuthRequest(r.Method, r.URL.EscapedPath(), r.URL.RawQuery, r.Header.Get, delegation.BodyNone, nil))
	if err != nil {
		f.t.Errorf("balance request did not authenticate: %v", err)
		apierr.Write(w, apierr.PublicReadError{Status: apierr.Unauthenticated})
		return
	}
	f.mu.Lock()
	available := f.available
	f.mu.Unlock()
	status := wire.BalanceStatus{ProviderID: keys.DIDOf(f.provider), AuthorID: author, Network: strings.TrimPrefix(r.URL.Path, "/v1/balances/"), Denomination: "millisatoshi", Available: available, Frozen: "0", Reserved: "0", Revision: 1, UpdatedAt: wire.FormatTime(f.now)}
	encoded, err := canon.EncodeWithout(status, "provider_signature")
	if err == nil {
		status.ProviderSignature, err = keys.Sign(f.provider, "balance-status", encoded)
	}
	if err != nil {
		f.t.Error(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	pbWriteJSON(f.t, w, http.StatusOK, status)
}

func (f *pbFakeProvider) serveQuote(w http.ResponseWriter, body []byte) {
	var request wire.FeePreflightRequest
	if err := wire.Decode(body, "FeePreflightRequest", &request); err != nil {
		f.t.Errorf("decode preflight: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var intent wire.Intent
	if err := wire.Decode(request.Intent, "AdmissionRequest/properties/intent", &intent); err != nil {
		f.t.Errorf("decode preflight intent: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	id, err := message.ID(intent)
	if err != nil || fee.VerifyPreflight(fee.PreflightAuthorization(request.ProviderID, request.AuthorID, id, request.ScheduleID), request.AuthorID, request.Authorization) != nil {
		f.t.Errorf("preflight authorization did not verify: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	canonical, err := canon.Encode(intent)
	if err != nil {
		f.t.Error(err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	schedule, ok := f.root.ScheduleInForce(wire.BitcoinMainnet, f.now)
	if !ok {
		f.t.Error("dev trust root has no bitcoin-mainnet schedule")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	quote, err := fee.IssueQuote(fee.QuoteRequest{ProviderID: request.ProviderID, AuthorID: request.AuthorID, MessageID: id, BillableBytes: len(canonical), PreflightAt: f.now, ExpiresAt: f.now.Add(10 * time.Minute)}, schedule, f.root.schedules, f.provider)
	if err != nil {
		f.t.Error(err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if f.quoteChange != nil {
		quote = pbResignQuote(f.t, f.quoteChange(quote), f.quoteSigner)
	}
	f.mu.Lock()
	f.quotes[quote.QuoteID] = quote
	f.mu.Unlock()
	pbWriteJSON(f.t, w, http.StatusOK, quote)
}

func (f *pbFakeProvider) serveAdmission(w http.ResponseWriter, r *http.Request, body []byte) {
	f.mu.Lock()
	f.admissions = append(f.admissions, body)
	first := len(f.admissions) == 1
	f.mu.Unlock()
	var request wire.AdmissionRequest
	if err := wire.Decode(body, "AdmissionRequest", &request); err != nil {
		f.t.Errorf("decode admission: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	id, err := message.ID(request.Intent)
	if err != nil || fee.VerifyAdmission(fee.AdmissionAuthorization(request.ProviderID, request.Intent.AuthorID, id, request.QuoteID), request.Intent.AuthorID, request.Authorization) != nil {
		f.t.Errorf("admission authorization did not verify: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if f.rejectWith != "" {
		apierr.Write(w, apierr.PublicReadError{Status: f.rejectWith})
		return
	}
	f.mu.Lock()
	if prior, ok := f.receipts[id]; ok {
		f.mu.Unlock()
		pbWriteJSON(f.t, w, http.StatusOK, prior)
		return
	}
	quote, ok := f.quotes[request.QuoteID]
	if !ok || quote.MessageID != id {
		f.mu.Unlock()
		apierr.Write(w, apierr.PublicReadError{Status: "quote_conflict"})
		return
	}
	receipt, err := fee.SignReceipt(wire.SubmissionReceipt{ProviderID: quote.ProviderID, AuthorID: quote.AuthorID, MessageID: id, ReceiptID: keys.EncodeB64URL(make([]byte, 32)), QuoteID: quote.QuoteID, ScheduleID: quote.ScheduleID, Network: quote.Network, AmountBaseUnits: quote.AmountBaseUnits, AuthorizationAt: wire.FormatTime(f.now), IssuedAt: wire.FormatTime(f.now)}, f.provider)
	if err == nil {
		f.receipts[id] = receipt
		f.debits[id]++
	}
	f.mu.Unlock()
	if err != nil {
		f.t.Error(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if f.hangAdmission && first {
		pbDropConnection(f.t, w)
		return
	}
	pbWriteJSON(f.t, w, http.StatusCreated, receipt)
}

func (f *pbFakeProvider) serveMessage(w http.ResponseWriter, r *http.Request, body []byte) {
	f.mu.Lock()
	f.messages = append(f.messages, body)
	first := len(f.messages) == 1
	f.mu.Unlock()
	var record wire.PostMessage
	if err := wire.Decode(body, "PostMessage", &record); err != nil || message.Verify(record, message.VerifyOptions{Now: f.now}) != nil {
		f.t.Errorf("final record did not verify: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if f.hangMessage && first {
		pbDropConnection(f.t, w)
		return
	}
	status := http.StatusCreated
	if !first {
		status = http.StatusOK
	}
	pbWriteJSON(f.t, w, status, wire.MessageAcceptance{MessageID: record.MessageID, Status: "accepted"})
}

// pbDropConnection loses the response the way a broken connection does, so the
// client's outcome is unknown without the test depending on how fast the machine is.
func pbDropConnection(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		t.Errorf("hijack: %v", err)
		return
	}
	_ = conn.Close()
}

func (f *pbFakeProvider) admissionBodies() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte(nil), f.admissions...)
}

func (f *pbFakeProvider) messageBodies() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte(nil), f.messages...)
}

func (f *pbFakeProvider) debitCount(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.debits[id]
}

func pbResignQuote(t *testing.T, quote wire.FeeQuote, key ed25519.PrivateKey) wire.FeeQuote {
	t.Helper()
	id, err := fee.QuoteID(quote)
	if err != nil {
		t.Fatal(err)
	}
	quote.QuoteID = id
	unsigned, err := canon.EncodeWithout(quote, "provider_signature")
	if err != nil {
		t.Fatal(err)
	}
	quote.ProviderSignature, err = keys.Sign(key, "fee-quote", unsigned)
	if err != nil {
		t.Fatal(err)
	}
	return quote
}

func pbReadBody(t *testing.T, r *http.Request) []byte {
	t.Helper()
	var body bytes.Buffer
	if _, err := body.ReadFrom(r.Body); err != nil {
		t.Error(err)
	}
	return body.Bytes()
}

func pbWriteJSON(t *testing.T, w http.ResponseWriter, status int, value any) {
	t.Helper()
	data, err := canon.Encode(value)
	if err != nil {
		t.Error(err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(status)
	if _, err := w.Write(data); err != nil {
		t.Error(err)
	}
}

func pbApproval(q Quoted, at time.Time) Approval {
	return Approval{MessageID: q.Prepared.MessageID, Amount: q.Quote.AmountBaseUnits, Network: q.Quote.Network, ApprovedAt: at}
}

func pbQuote(t *testing.T, p *Publisher, author ed25519.PrivateKey, text string) Quoted {
	t.Helper()
	q, err := p.QuoteDraft(t.Context(), Draft{ContentFormat: "text/plain", Text: text}, author, wire.BitcoinMainnet, nil)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func pbCheckQuoteMismatch(t *testing.T, setup func(*pbFakeProvider)) {
	t.Helper()
	publisher, author, fake := pbSetup(t, time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC), time.Second)
	fake.quoteSigner = fake.provider
	setup(fake)
	_, err := publisher.QuoteDraft(t.Context(), Draft{ContentFormat: "text/plain", Text: "quote mismatch"}, author, wire.BitcoinMainnet, nil)
	if !errors.Is(err, ErrQuoteMismatch) {
		t.Fatalf("QuoteDraft error = %v; want ErrQuoteMismatch", err)
	}
	if n := len(fake.admissionBodies()); n != 0 {
		t.Fatalf("admission requests = %d; want 0", n)
	}
}

func pbCheckAdmitRefused(t *testing.T, p *Publisher, fake *pbFakeProvider, author ed25519.PrivateKey, q Quoted, a Approval, want error) {
	t.Helper()
	if _, err := p.Admit(t.Context(), author, q, a); !errors.Is(err, want) {
		t.Fatalf("Admit error = %v; want %v", err, want)
	}
	if n := len(fake.admissionBodies()); n != 0 {
		t.Fatalf("admission requests = %d; want 0", n)
	}
}

func pbCheckAdmissionRejected(t *testing.T, status int, name apierr.Status) {
	t.Helper()
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	publisher, author, fake := pbSetup(t, now, time.Second)
	fake.rejectWith = name
	q := pbQuote(t, publisher, author, "rejected")
	_, err := publisher.Admit(t.Context(), author, q, pbApproval(q, now))
	var serviceError *Error
	if !errors.As(err, &serviceError) || serviceError.HTTPStatus != status || serviceError.Status != string(name) {
		t.Fatalf("Admit error = %v; want HTTP %d %s", err, status, name)
	}
	entry, ok, err := publisher.journal.Get(q.Prepared.MessageID)
	if err != nil || !ok || entry.State != "rejected" || entry.Rejection != string(name) || len(entry.AdmissionRequest) == 0 {
		t.Fatalf("journal = %+v, %v, %v; want rejected %s with the admission request kept", entry, ok, err, name)
	}
}

func TestPublishHappyPath(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	publisher, author, fake := pbSetup(t, now, time.Second)
	quoted := pbQuote(t, publisher, author, "hello")
	if quoted.Prepared.Fee != "136" || quoted.Prepared.Denomination != "millisatoshi" || quoted.Quote.AmountBaseUnits != "136" || quoted.Quote.ExpiresAt != "2026-11-11T12:10:00Z" {
		t.Fatalf("quoted = %+v; want 136 millisatoshi expiring 12:10:00Z", quoted)
	}
	published, err := publisher.Publish(t.Context(), author, quoted, pbApproval(quoted, now))
	if err != nil {
		t.Fatal(err)
	}
	if published.MessageID != quoted.Prepared.MessageID || published.Retry || len(fake.admissionBodies()) != 1 {
		t.Fatalf("published = %+v with %d admissions", published, len(fake.admissionBodies()))
	}
	entry, ok, err := publisher.journal.Get(published.MessageID)
	if err != nil || !ok || entry.State != "accepted" {
		t.Fatalf("journal = %+v, %v, %v; want accepted", entry, ok, err)
	}
	var final wire.PostMessage
	if err := wire.Decode(entry.FinalMessage, "PostMessage", &final); err != nil {
		t.Fatal(err)
	}
	if err := message.Verify(final, message.VerifyOptions{Now: now}); err != nil {
		t.Fatalf("stored final message does not verify: %v", err)
	}
	if final.SubmissionReceipt != entry.Receipt || final.SubmissionReceipt.QuoteID != quoted.Quote.QuoteID {
		t.Fatalf("stored final message receipt = %+v; want the admitted receipt", final.SubmissionReceipt)
	}
}

func TestPrepare(t *testing.T) {
	t.Run("specification media slot example recomputes its ID and billable bytes", func(t *testing.T) {
		publisher, author, _ := pbSetup(t, time.Date(2026, 11, 11, 0, 0, 4, 0, time.UTC), time.Second)
		data, err := os.ReadFile("testdata/pb/post-payload.json")
		if err != nil {
			t.Fatal(err)
		}
		var payload wire.PostPayload
		if err := wire.Decode(data, "PostPayload", &payload); err != nil {
			t.Fatal(err)
		}
		prepared, err := publisher.Prepare(Draft{ContentFormat: payload.ContentFormat, Text: payload.Text, MediaSlots: *payload.MediaSlots}, author, wire.BitcoinMainnet, nil)
		if err != nil {
			t.Fatal(err)
		}
		if prepared.MessageID != "fy1:8ce9fcf3b498398bcd9a5c62345257c421037e8cfbb58418c32dc56b496dc15f" || prepared.BillableBytes != 602 || prepared.Fee != "136" {
			t.Fatalf("prepared = %s, %d bytes, fee %s; want the example's ID, 602 bytes, 136", prepared.MessageID, prepared.BillableBytes, prepared.Fee)
		}
	})
	t.Run("1025 billable bytes cost two started KiB", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
		publisher, author, _ := pbSetup(t, now, time.Second)
		short, err := canon.Encode(wire.Intent{AuthorID: keys.DIDOf(author), Timestamp: wire.FormatTime(now), MessageType: "post", Version: wire.Version{Major: 2}, Payload: wire.PostPayload{ContentFormat: "text/plain", Text: "x"}})
		if err != nil {
			t.Fatal(err)
		}
		prepared, err := publisher.Prepare(Draft{ContentFormat: "text/plain", Text: strings.Repeat("x", 1025-len(short)+1)}, author, wire.BitcoinMainnet, nil)
		if err != nil {
			t.Fatal(err)
		}
		if prepared.BillableBytes != 1025 || prepared.Fee != "272" {
			t.Fatalf("billable bytes / fee = %d / %s; want 1025 / 272", prepared.BillableBytes, prepared.Fee)
		}
	})
	t.Run("final message budget reserves the 8192 byte receipt bound", func(t *testing.T) {
		publisher, author, fake := pbSetup(t, time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC), time.Second)
		_, err := publisher.QuoteDraft(t.Context(), Draft{ContentFormat: "text/plain", Text: strings.Repeat("x", 1048576-4000)}, author, wire.BitcoinMainnet, nil)
		if !errors.Is(err, ErrTooLarge) {
			t.Fatalf("QuoteDraft error = %v; want ErrTooLarge", err)
		}
		if n := len(fake.admissionBodies()); n != 0 {
			t.Fatalf("admission requests = %d; want 0", n)
		}
	})
	t.Run("update of another author's record", func(t *testing.T) {
		publisher, author, _ := pbSetup(t, time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC), time.Second)
		target := "fy1:" + strings.Repeat("a", 64)
		_, err := publisher.Prepare(Draft{ContentFormat: "text/plain", Text: "update", Supersedes: target}, author, wire.BitcoinMainnet, &TargetEvidence{Message: wire.PostMessage{MessageID: target, AuthorID: keys.DIDOf(devnet.ProviderKey(0))}})
		if !errors.Is(err, ErrUpdateAuthor) {
			t.Fatalf("Prepare error = %v; want ErrUpdateAuthor", err)
		}
	})
	t.Run("update whose target has a reply_to the draft lacks", func(t *testing.T) {
		publisher, author, _ := pbSetup(t, time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC), time.Second)
		target := "fy1:" + strings.Repeat("a", 64)
		evidence := &TargetEvidence{Message: wire.PostMessage{MessageID: target, AuthorID: keys.DIDOf(author), Payload: wire.PostPayload{ReplyTo: "fy1:" + strings.Repeat("b", 64)}}}
		_, err := publisher.Prepare(Draft{ContentFormat: "text/plain", Text: "update", Supersedes: target}, author, wire.BitcoinMainnet, evidence)
		if !errors.Is(err, ErrUpdateThread) {
			t.Fatalf("Prepare error = %v; want ErrUpdateThread", err)
		}
	})
	t.Run("evidence for another record than the superseded one", func(t *testing.T) {
		publisher, author, _ := pbSetup(t, time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC), time.Second)
		_, err := publisher.Prepare(Draft{ContentFormat: "text/plain", Text: "update", Supersedes: "fy1:" + strings.Repeat("a", 64)}, author, wire.BitcoinMainnet, &TargetEvidence{Message: wire.PostMessage{MessageID: "fy1:" + strings.Repeat("b", 64), AuthorID: keys.DIDOf(author)}})
		if err == nil {
			t.Fatal("Prepare accepted evidence for another record")
		}
	})
	t.Run("seventeen media slots", func(t *testing.T) {
		publisher, author, _ := pbSetup(t, time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC), time.Second)
		s := wire.MediaReference{ID: "a", URL: "https://media.example/a.jpg", Kind: "image", SHA256: strings.Repeat("a", 64)}
		slots := []wire.MediaReference{s, s, s, s, s, s, s, s, s, s, s, s, s, s, s, s, s}
		if _, err := publisher.Prepare(Draft{ContentFormat: "text/plain", Text: "slots", MediaSlots: slots}, author, wire.BitcoinMainnet, nil); err == nil {
			t.Fatal("Prepare accepted seventeen media slots")
		}
	})
	t.Run("duplicate reference target", func(t *testing.T) {
		publisher, author, _ := pbSetup(t, time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC), time.Second)
		target := "fy1:" + strings.Repeat("a", 64)
		refs := []wire.PostReference{{ID: "one", Target: target}, {ID: "two", Target: target}}
		if _, err := publisher.Prepare(Draft{ContentFormat: "text/plain", Text: "refs", Refs: refs}, author, wire.BitcoinMainnet, nil); err == nil {
			t.Fatal("Prepare accepted a duplicate reference target")
		}
	})
	t.Run("empty text without a media slot", func(t *testing.T) {
		publisher, author, _ := pbSetup(t, time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC), time.Second)
		if _, err := publisher.Prepare(Draft{ContentFormat: "text/plain"}, author, wire.BitcoinMainnet, nil); err == nil {
			t.Fatal("Prepare accepted empty text without a media slot")
		}
	})
}

func TestQuoteMismatch(t *testing.T) {
	t.Run("amount 137", func(t *testing.T) {
		pbCheckQuoteMismatch(t, func(f *pbFakeProvider) {
			f.quoteChange = func(q wire.FeeQuote) wire.FeeQuote { q.AmountBaseUnits = "137"; return q }
		})
	})
	t.Run("another schedule_id", func(t *testing.T) {
		pbCheckQuoteMismatch(t, func(f *pbFakeProvider) {
			f.quoteChange = func(q wire.FeeQuote) wire.FeeQuote { q.ScheduleID = "fyfs2:" + strings.Repeat("0", 64); return q }
		})
	})
	t.Run("another message_id", func(t *testing.T) {
		pbCheckQuoteMismatch(t, func(f *pbFakeProvider) {
			f.quoteChange = func(q wire.FeeQuote) wire.FeeQuote { q.MessageID = "fy1:" + strings.Repeat("0", 64); return q }
		})
	})
	t.Run("expires_at 3601 s after preflight_at", func(t *testing.T) {
		pbCheckQuoteMismatch(t, func(f *pbFakeProvider) {
			f.quoteChange = func(q wire.FeeQuote) wire.FeeQuote { q.ExpiresAt = "2026-11-11T13:00:01Z"; return q }
		})
	})
	t.Run("signed by another key", func(t *testing.T) {
		pbCheckQuoteMismatch(t, func(f *pbFakeProvider) {
			f.quoteChange = func(q wire.FeeQuote) wire.FeeQuote { return q }
			f.quoteSigner = devnet.ProviderKey(0)
		})
	})
}

func TestAdmitRefusesBeforeSending(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	t.Run("approval amount differs from the quote", func(t *testing.T) {
		publisher, author, fake := pbSetup(t, now, time.Second)
		q := pbQuote(t, publisher, author, "approval")
		a := pbApproval(q, now)
		a.Amount = "137"
		pbCheckAdmitRefused(t, publisher, fake, author, q, a, ErrNotApproved)
	})
	t.Run("timestamp 241 s old", func(t *testing.T) {
		publisher, author, fake := pbSetup(t, now, time.Second)
		q := pbQuote(t, publisher, author, "stale")
		publisher.now = func() time.Time { return now.Add(241 * time.Second) }
		pbCheckAdmitRefused(t, publisher, fake, author, q, pbApproval(q, now), ErrStale)
	})
	t.Run("quote expired while the reader confirmed", func(t *testing.T) {
		publisher, author, fake := pbSetup(t, now, time.Second)
		q := pbQuote(t, publisher, author, "expired")
		publisher.now = func() time.Time { return now.Add(11 * time.Minute) }
		pbCheckAdmitRefused(t, publisher, fake, author, q, pbApproval(q, now), ErrStale)
	})
	t.Run("quote expires within 30 s", func(t *testing.T) {
		publisher, author, fake := pbSetup(t, now, time.Second)
		q := pbQuote(t, publisher, author, "expiring")
		publisher.now = func() time.Time { return now.Add(9*time.Minute + 31*time.Second) }
		pbCheckAdmitRefused(t, publisher, fake, author, q, pbApproval(q, now), ErrStale)
	})
	t.Run("intent changed after quoting", func(t *testing.T) {
		publisher, author, fake := pbSetup(t, now, time.Second)
		q := pbQuote(t, publisher, author, "quoted intent")
		q.Prepared.Intent.Payload.Text = "changed"
		pbCheckAdmitRefused(t, publisher, fake, author, q, pbApproval(q, now), ErrQuoteMismatch)
	})
}

func TestAdmitChecksConfirmedBalance(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	t.Run("balance below the quoted fee refuses before authorizing", func(t *testing.T) {
		publisher, author, fake := pbSetup(t, now, time.Second)
		q := pbQuote(t, publisher, author, "underfunded")
		fake.available = "135"
		pbCheckAdmitRefused(t, publisher, fake, author, q, pbApproval(q, now), ErrInsufficientBalance)
		if entry, ok, err := publisher.journal.Get(q.Prepared.MessageID); err != nil || !ok || entry.State != "quoted" {
			t.Fatalf("journal = %+v, %v, %v; want quoted", entry, ok, err)
		}
	})
	t.Run("balance equal to the quoted fee proceeds", func(t *testing.T) {
		publisher, author, fake := pbSetup(t, now, time.Second)
		q := pbQuote(t, publisher, author, "funded")
		fake.available = "136"
		if _, err := publisher.Admit(t.Context(), author, q, pbApproval(q, now)); err != nil {
			t.Fatal(err)
		}
		if n := len(fake.admissionBodies()); n != 1 {
			t.Fatalf("admission requests = %d; want 1", n)
		}
	})
}

func TestAdmissionRejected(t *testing.T) {
	t.Run("402 insufficient_balance", func(t *testing.T) { pbCheckAdmissionRejected(t, http.StatusPaymentRequired, "insufficient_balance") })
	t.Run("422 quote_expired", func(t *testing.T) { pbCheckAdmissionRejected(t, http.StatusUnprocessableEntity, "quote_expired") })
	t.Run("409 quote_conflict", func(t *testing.T) { pbCheckAdmissionRejected(t, http.StatusConflict, "quote_conflict") })
}

func TestAmbiguousAdmission(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	publisher, author, fake := pbSetup(t, now, 10*time.Second)
	fake.hangAdmission = true
	q := pbQuote(t, publisher, author, "ambiguous admission")
	if _, err := publisher.Admit(t.Context(), author, q, pbApproval(q, now)); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("Admit error = %v; want ErrOutcomeUnknown", err)
	}
	if entry, ok, err := publisher.journal.Get(q.Prepared.MessageID); err != nil || !ok || entry.State != "authorized" {
		t.Fatalf("journal = %+v, %v, %v; want authorized", entry, ok, err)
	}
	schedule, _ := publisher.root.ScheduleInForce(wire.BitcoinMainnet, now)
	second := q
	var err error
	second.Quote, err = fee.IssueQuote(fee.QuoteRequest{ProviderID: q.Quote.ProviderID, AuthorID: q.Quote.AuthorID, MessageID: q.Prepared.MessageID, BillableBytes: q.Prepared.BillableBytes, PreflightAt: now, ExpiresAt: now.Add(9 * time.Minute)}, schedule, publisher.root.schedules, fake.provider)
	if err != nil {
		t.Fatal(err)
	}
	if second.Raw, err = canon.Encode(second.Quote); err != nil {
		t.Fatal(err)
	}
	if _, err := publisher.Admit(t.Context(), author, second, pbApproval(second, now)); !errors.Is(err, ErrPendingAuthorization) {
		t.Fatalf("second Admit error = %v; want ErrPendingAuthorization", err)
	}
	entry, err := publisher.Resume(t.Context(), author, q.Prepared.MessageID)
	if err != nil || entry.State != "admitted" {
		t.Fatalf("Resume = %+v, %v; want admitted", entry, err)
	}
	bodies := fake.admissionBodies()
	if len(bodies) != 2 || !bytes.Equal(bodies[0], bodies[1]) {
		t.Fatalf("admission bodies = %d; want 2 byte-identical", len(bodies))
	}
	if n := fake.debitCount(q.Prepared.MessageID); n != 1 {
		t.Fatalf("debits = %d; want 1", n)
	}
}

func TestAmbiguousFinalization(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	t.Run("Resume resubmits the identical signed bytes", func(t *testing.T) {
		publisher, author, fake := pbSetup(t, now, 10*time.Second)
		fake.hangMessage = true
		q := pbQuote(t, publisher, author, "ambiguous finalization")
		if _, err := publisher.Publish(t.Context(), author, q, pbApproval(q, now)); !errors.Is(err, ErrOutcomeUnknown) {
			t.Fatalf("Publish error = %v; want ErrOutcomeUnknown", err)
		}
		if entry, ok, err := publisher.journal.Get(q.Prepared.MessageID); err != nil || !ok || entry.State != "signed" {
			t.Fatalf("journal = %+v, %v, %v; want signed", entry, ok, err)
		}
		entry, err := publisher.Resume(t.Context(), author, q.Prepared.MessageID)
		if err != nil || entry.State != "accepted" {
			t.Fatalf("Resume = %+v, %v; want accepted", entry, err)
		}
		bodies := fake.messageBodies()
		if len(bodies) != 2 || !bytes.Equal(bodies[0], bodies[1]) || !bytes.Equal(bodies[1], entry.FinalMessage) {
			t.Fatalf("message bodies = %d; want 2 byte-identical to the journal", len(bodies))
		}
	})
	t.Run("a second Finalize reports the 200 identical retry", func(t *testing.T) {
		publisher, author, fake := pbSetup(t, now, 10*time.Second)
		fake.hangMessage = true
		q := pbQuote(t, publisher, author, "finalize retry")
		admitted, err := publisher.Admit(t.Context(), author, q, pbApproval(q, now))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := publisher.Finalize(t.Context(), author, admitted); !errors.Is(err, ErrOutcomeUnknown) {
			t.Fatalf("Finalize error = %v; want ErrOutcomeUnknown", err)
		}
		published, err := publisher.Finalize(t.Context(), author, admitted)
		if err != nil || !published.Retry {
			t.Fatalf("second Finalize = %+v, %v; want Retry", published, err)
		}
		bodies := fake.messageBodies()
		if len(bodies) != 2 || !bytes.Equal(bodies[0], bodies[1]) {
			t.Fatalf("message bodies = %d; want 2 byte-identical", len(bodies))
		}
	})
}

func TestFinalizeAfterDeadline(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	publisher, author, fake := pbSetup(t, now, time.Second)
	q := pbQuote(t, publisher, author, "deadline")
	admitted, err := publisher.Admit(t.Context(), author, q, pbApproval(q, now))
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 12, 11, 12, 0, 0, 0, time.UTC); !admitted.Deadline.Equal(want) {
		t.Fatalf("deadline = %v; want %v", admitted.Deadline, want)
	}
	publisher.now = func() time.Time { return admitted.Deadline }
	if _, err := publisher.Finalize(t.Context(), author, admitted); !errors.Is(err, ErrDeadlinePassed) {
		t.Fatalf("Finalize error = %v; want ErrDeadlinePassed", err)
	}
	if n := len(fake.messageBodies()); n != 0 {
		t.Fatalf("message submissions = %d; want 0", n)
	}
}

func TestScanDraft(t *testing.T) {
	t.Run("private key header and credential URL are masked", func(t *testing.T) {
		findings := ScanDraft(Draft{ContentFormat: "text/plain", Text: "-----BEGIN PRIVATE KEY-----", MediaSlots: []wire.MediaReference{{URL: "https://x.example/a?token=abcd"}}})
		want := []Finding{{Field: "text", Kind: "private_key_pem", Excerpt: "----" + strings.Repeat("*", 23)}, {Field: "media_slots[0].url", Kind: "credential_url", Excerpt: "http" + strings.Repeat("*", 26)}}
		if len(findings) != 2 || findings[0] != want[0] || findings[1] != want[1] {
			t.Fatalf("findings = %+v; want %+v", findings, want)
		}
	})
	t.Run("ordinary text and dotted words yield nothing", func(t *testing.T) {
		if findings := ScanDraft(Draft{ContentFormat: "text/plain", Text: "ordinary post about something.longwords.otherthing"}); len(findings) != 0 {
			t.Fatalf("findings = %+v; want none", findings)
		}
	})
}
