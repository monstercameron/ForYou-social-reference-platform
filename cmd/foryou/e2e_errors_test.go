package main

// 429 rate_limited and 503 unavailable are not tested: they depend on disclosed operator limits and outages the devnet does not configure.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/delegation"
	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/fee"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/message"
	"foryou.local/foryou/internal/sdk"
	"foryou.local/foryou/internal/wire"
)

// negStart is one hour after 2026-10-01T00:00:00Z, the quarter start on which dev fee schedule 1 takes effect.
var negStart = time.Date(2026, 10, 1, 1, 0, 0, 0, time.UTC)

// negClock is the goroutine-safe controlled clock shared by every devnet role and the test.
type negClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *negClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *negClock) Set(at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = at
}

func (c *negClock) Advance(d time.Duration) { c.Set(c.Now().Add(d)) }

type negEnv struct {
	d          *devnetRuntime
	clock      *negClock
	client     *sdk.Client
	publisher  *sdk.Publisher
	scheduleID string
	author     keys.PrivateKey
	unfunded   keys.PrivateKey
	delegate   keys.PrivateKey
}

func TestDevnetErrorCatalog(t *testing.T) {
	e := negSetup(t)
	provider, foundation := e.d.URL("provider"), e.d.URL("foundation")
	zeroID := "fy1:" + strings.Repeat("0", 64)

	t.Run("400 invalid_query limit is not a number", func(t *testing.T) {
		negExpectError(t, negGet(t, provider+"/v1/messages?limit=abc"), 400, "invalid_query")
	})
	t.Run("400 invalid_query impossible date", func(t *testing.T) {
		negExpectError(t, negGet(t, provider+"/v1/messages?date=2026-02-30"), 400, "invalid_query")
	})
	t.Run("400 invalid_query repeated parameter", func(t *testing.T) {
		negExpectError(t, negGet(t, provider+"/v1/messages?limit=1&limit=2"), 400, "invalid_query")
	})
	t.Run("400 invalid_cursor empty cursor", func(t *testing.T) {
		negExpectError(t, negGet(t, provider+"/v1/messages?cursor="), 400, "invalid_cursor")
	})
	t.Run("400 invalid_cursor cursor from another listing", func(t *testing.T) {
		negPublish(t, e, "first post for a cursor")
		negPublish(t, e, "second post for a cursor")
		var page wire.DiscoveryPage
		negDecode(t, negGet(t, provider+"/v1/messages?limit=1"), 200, &page)
		if page.NextCursor == nil {
			t.Fatal("GET /v1/messages?limit=1 over two posts returned no next_cursor")
		}
		target := provider + "/v1/author-messages?author_id=" + url.QueryEscape(keys.DIDOf(e.author)) + "&limit=1&cursor=" + *page.NextCursor
		negExpectError(t, negGet(t, target), 400, "invalid_cursor")
	})

	depositBody := func(amount, requestSeed string) []byte {
		return negJSON(t, wire.DepositRequest{
			ProviderID: e.client.ServiceID(), AuthorID: keys.DIDOf(e.author), Network: wire.BitcoinMainnet, AmountChainUnits: amount,
			RequestID: keys.EncodeB64URL([]byte(requestSeed)), NotAfter: wire.FormatTime(e.clock.Now().Add(time.Minute)),
		})
	}
	t.Run("401 unauthenticated without service-auth", func(t *testing.T) {
		body := depositBody("1000", "neg-deposit-no-auth-000000000001")
		negExpectError(t, negSend(t, http.MethodPost, provider+"/v1/deposits", body, nil), 401, "unauthenticated")
	})
	t.Run("401 unauthenticated challenge used twice", func(t *testing.T) {
		body := depositBody("1000", "neg-deposit-replayed-00000000001")
		headers := negServiceAuth(t, e, e.author, http.MethodPost, "/v1/deposits", body)
		first := negSend(t, http.MethodPost, provider+"/v1/deposits", body, headers)
		negStatus(t, first, 201)
		negExpectError(t, negSend(t, http.MethodPost, provider+"/v1/deposits", body, headers), 401, "unauthenticated")
	})
	t.Run("401 unauthenticated challenge for another resource", func(t *testing.T) {
		body := depositBody("1000", "neg-deposit-other-resource-00001")
		headers := negServiceAuth(t, e, e.author, http.MethodPost, "/v1/withdrawals", body)
		negExpectError(t, negSend(t, http.MethodPost, provider+"/v1/deposits", body, headers), 401, "unauthenticated")
	})

	t.Run("402 insufficient_balance for an unfunded author", func(t *testing.T) {
		before := negBalance(t, e, e.unfunded)
		intent := negIntent(e, e.unfunded, nil, "an unfunded author's post")
		quote := negQuote(t, e, e.unfunded, intent)
		request := negAdmission(t, e, e.unfunded, intent, quote.QuoteID, quote.QuoteID)
		negExpectError(t, negSend(t, http.MethodPost, provider+"/v1/admissions", request, nil), 402, "insufficient_balance")
		negBalanceUnchanged(t, e, e.unfunded, before)
	})

	t.Run("403 delegation_revoked after revocation", func(t *testing.T) {
		state, err := e.client.EnrollEpoch(context.Background(), e.author, wire.AuthorizationEpochEnroll{
			RequestNonce: keys.EncodeB64URL([]byte("neg-epoch-enroll-nonce-000000001")), NotAfter: wire.FormatTime(e.clock.Now().Add(time.Minute)),
		})
		if err != nil {
			t.Fatalf("EnrollEpoch: %v", err)
		}
		granted, err := delegation.SignDelegation(e.author, wire.Delegation{
			AuthorID: keys.DIDOf(e.author), SignerID: keys.DIDOf(e.delegate), ProviderID: e.client.ServiceID(), Scope: []string{"post"},
			Epoch: *state.Epoch, ValidFrom: wire.FormatTime(e.clock.Now()), ValidUntil: wire.FormatTime(e.clock.Now().Add(24 * time.Hour)),
		})
		if err != nil {
			t.Fatalf("SignDelegation: %v", err)
		}
		intent := negIntent(e, e.author, &granted, "a delegate's post")
		quote := negQuote(t, e, e.delegate, intent)
		if err := e.client.Revoke(context.Background(), e.author, wire.RevocationRequest{DelegationID: granted.DelegationID, Delegation: granted}); err != nil {
			t.Fatalf("Revoke: %v", err)
		}
		before := negBalance(t, e, e.author)
		request := negAdmission(t, e, e.delegate, intent, quote.QuoteID, quote.QuoteID)
		negExpectError(t, negSend(t, http.MethodPost, provider+"/v1/admissions", request, nil), 403, "delegation_revoked")
		negBalanceUnchanged(t, e, e.author, before)
	})

	t.Run("404 not_found unknown message", func(t *testing.T) {
		negExpectError(t, negGet(t, provider+"/v1/messages/"+zeroID), 404, "not_found")
	})
	t.Run("404 not_found unknown settlement receipt", func(t *testing.T) {
		negExpectError(t, negGet(t, provider+"/v1/settlements/"+keys.EncodeB64URL([]byte("neg-unknown-receipt-id-000000001"))), 404, "not_found")
	})
	t.Run("404 not_found unknown vault receipt", func(t *testing.T) {
		negExpectError(t, negGet(t, foundation+"/v1/vault/receipts/"+zeroID), 404, "not_found")
	})

	t.Run("409 quote_conflict quote of another intent", func(t *testing.T) {
		intentA := negIntent(e, e.author, nil, "intent A")
		intentB := negIntent(e, e.author, nil, "intent B")
		quoteA := negQuote(t, e, e.author, intentA)
		negQuote(t, e, e.author, intentB)
		before := negBalance(t, e, e.author)
		request := negAdmission(t, e, e.author, intentB, quoteA.QuoteID, quoteA.QuoteID)
		negExpectError(t, negSend(t, http.MethodPost, provider+"/v1/admissions", request, nil), 409, "quote_conflict")
		negBalanceUnchanged(t, e, e.author, before)
	})
	t.Run("409 request_conflict deposit retry with another amount", func(t *testing.T) {
		original := depositBody("1000", "neg-deposit-conflict-00000000001")
		negStatus(t, negSend(t, http.MethodPost, provider+"/v1/deposits", original, negServiceAuth(t, e, e.author, http.MethodPost, "/v1/deposits", original)), 201)
		changed := depositBody("2000", "neg-deposit-conflict-00000000001")
		negExpectError(t, negSend(t, http.MethodPost, provider+"/v1/deposits", changed, negServiceAuth(t, e, e.author, http.MethodPost, "/v1/deposits", changed)), 409, "request_conflict")
	})

	pending := negPublish(t, e, "a post that is claimed before the committee witnesses it")
	negStepPastClaimPeriod(t, e)
	e.clock.Advance(2 * time.Minute)
	if err := e.d.Service("provider").RunOnce(context.Background()); err != nil {
		t.Fatalf("provider RunOnce: %v", err)
	}
	var proof wire.SettlementProof
	negDecode(t, negGet(t, provider+"/v1/settlements/"+pending.Receipt.ReceiptID), 200, &proof)
	if len(proof.Witnesses) != 0 {
		t.Fatalf("proof before any monitor pass has %d witnesses; want 0", len(proof.Witnesses))
	}
	pendingBytes := negBody(t, negGet(t, provider+"/v1/messages/"+pending.Receipt.MessageID), 200)

	t.Run("409 not_ready vault delivery before settlement", func(t *testing.T) {
		delivery := negJSON(t, wire.VaultDelivery{Message: pendingBytes, Proof: proof})
		negExpectError(t, negSend(t, http.MethodPut, foundation+"/v1/vault/messages/"+pending.Receipt.MessageID, delivery, nil), 409, "not_ready")
	})

	t.Run("422 id_mismatch message_id is not the intent hash", func(t *testing.T) {
		intent := negIntent(e, e.author, nil, "a post submitted under another ID")
		admitted := negAdmit(t, e, intent)
		before := negBalance(t, e, e.author)
		posted, err := message.Assemble(intent, admitted)
		if err != nil {
			t.Fatal(err)
		}
		posted.MessageID = "fy1:" + strings.Repeat("a", 64)
		if err := message.Sign(&posted, e.author); err != nil {
			t.Fatal(err)
		}
		negExpectError(t, negSend(t, http.MethodPost, provider+"/v1/messages", negJSON(t, posted), nil), 422, "id_mismatch")
		negBalanceUnchanged(t, e, e.author, before)
	})
	t.Run("422 message_too_large final message over MAX_MESSAGE_BYTES", func(t *testing.T) {
		intent := negIntent(e, e.author, nil, "")
		empty := len(negJSON(t, intent))
		// The intent alone stays 400 bytes under MAX_MESSAGE_BYTES (1048576); the receipt, message_id and signature push the final message over it.
		intent.Payload.Text = strings.Repeat("a", 1048576-400-empty)
		quote := negQuote(t, e, e.author, intent)
		before := negBalance(t, e, e.author)
		request := negAdmission(t, e, e.author, intent, quote.QuoteID, quote.QuoteID)
		negExpectError(t, negSend(t, http.MethodPost, provider+"/v1/admissions", request, nil), 422, "message_too_large")
		negBalanceUnchanged(t, e, e.author, before)
	})
	t.Run("422 invalid_message seventeen media slots", func(t *testing.T) {
		intent := negIntent(e, e.author, nil, "seventeen media slots")
		slots := []wire.MediaReference{
			negMedia("m01"), negMedia("m02"), negMedia("m03"), negMedia("m04"), negMedia("m05"), negMedia("m06"),
			negMedia("m07"), negMedia("m08"), negMedia("m09"), negMedia("m10"), negMedia("m11"), negMedia("m12"),
			negMedia("m13"), negMedia("m14"), negMedia("m15"), negMedia("m16"), negMedia("m17"),
		}
		intent.Payload.MediaSlots = &slots
		negExpectError(t, negPreflight(t, e, e.author, intent), 422, "invalid_message")
	})
	t.Run("422 invalid_message media slot over plain http", func(t *testing.T) {
		intent := negIntent(e, e.author, nil, "one media slot over plain http")
		slot := negMedia("m01")
		slot.URL = "http://example.com/a.png"
		intent.Payload.MediaSlots = &[]wire.MediaReference{slot}
		negExpectError(t, negPreflight(t, e, e.author, intent), 422, "invalid_message")
	})
	t.Run("422 invalid_message vault delivery with a changed signature byte", func(t *testing.T) {
		var tampered map[string]any
		if err := json.Unmarshal(pendingBytes, &tampered); err != nil {
			t.Fatal(err)
		}
		signature := []byte(tampered["signature"].(string))
		signature[0] = negOtherB64(signature[0])
		tampered["signature"] = string(signature)
		delivery := negJSON(t, map[string]any{"message": tampered, "proof": proof})
		negExpectError(t, negSend(t, http.MethodPut, foundation+"/v1/vault/messages/"+pending.Receipt.MessageID, delivery, nil), 422, "invalid_message")
	})
	t.Run("422 bad_authorization admission signed for another quote", func(t *testing.T) {
		intent := negIntent(e, e.author, nil, "authorization signed for another quote")
		quote := negQuote(t, e, e.author, intent)
		before := negBalance(t, e, e.author)
		request := negAdmission(t, e, e.author, intent, quote.QuoteID, "fyfq2:"+strings.Repeat("0", 64))
		negExpectError(t, negSend(t, http.MethodPost, provider+"/v1/admissions", request, nil), 422, "bad_authorization")
		negBalanceUnchanged(t, e, e.author, before)
	})
	t.Run("422 quote_expired 3601 seconds after preflight_at", func(t *testing.T) {
		intent := negIntent(e, e.author, nil, "admitted after its quote expired")
		quote := negQuote(t, e, e.author, intent)
		before := negBalance(t, e, e.author)
		e.clock.Set(negTime(t, quote.PreflightAt).Add(3601 * time.Second))
		request := negAdmission(t, e, e.author, intent, quote.QuoteID, quote.QuoteID)
		negExpectError(t, negSend(t, http.MethodPost, provider+"/v1/admissions", request, nil), 422, "quote_expired")
		negBalanceUnchanged(t, e, e.author, before)
	})
	t.Run("committed admission retry after quote expiry returns the original receipt", func(t *testing.T) {
		intent := negIntent(e, e.author, nil, "retried after its quote expired")
		quote := negQuote(t, e, e.author, intent)
		request := negAdmission(t, e, e.author, intent, quote.QuoteID, quote.QuoteID)
		original := negBody(t, negSend(t, http.MethodPost, provider+"/v1/admissions", request, nil), 201)
		before := negBalance(t, e, e.author)
		e.clock.Set(negTime(t, quote.PreflightAt).Add(3601 * time.Second))
		retried := negBody(t, negSend(t, http.MethodPost, provider+"/v1/admissions", request, nil), 200)
		if !bytes.Equal(retried, original) {
			t.Errorf("retried receipt = %s; want the original %s", retried, original)
		}
		negBalanceUnchanged(t, e, e.author, before)
	})

	t.Run("410 retention_expired after retain_until while the vault still serves the post", func(t *testing.T) {
		negStepPastClaimPeriod(t, e)
		var settled wire.SettlementProof
		negDecode(t, negGet(t, provider+"/v1/settlements/"+pending.Receipt.ReceiptID), 200, &settled)
		if len(settled.Witnesses) < 4 {
			t.Fatalf("proof after a full devnet pass has %d witnesses; want a quorum of at least 4 of 5", len(settled.Witnesses))
		}
		negStatus(t, negGet(t, foundation+"/v1/vault/receipts/"+pending.Receipt.MessageID), 200)
		var declarations wire.RetentionDeclarationsPage
		negDecode(t, negGet(t, provider+"/v1/retention"), 200, &declarations)
		if len(declarations.Declarations) != 1 || declarations.Declarations[0].DeclarationID != proof.Claim.DeclarationID {
			t.Fatalf("GET /v1/retention = %+v; want the one declaration %s that the claim names", declarations, proof.Claim.DeclarationID)
		}
		retention := declarations.Declarations[0].RetentionSeconds
		if retention < 31536000 {
			t.Fatalf("retention_seconds = %d; want at least 31536000", retention)
		}
		retainUntil := negTime(t, pending.Receipt.IssuedAt).Add(time.Duration(retention) * time.Second)
		e.clock.Set(retainUntil.Add(-time.Second))
		if err := e.d.Step(context.Background()); err != nil {
			t.Fatalf("Step: %v", err)
		}
		negStatus(t, negGet(t, provider+"/v1/messages/"+pending.Receipt.MessageID), 200)
		e.clock.Set(retainUntil.Add(time.Second))
		if err := e.d.Step(context.Background()); err != nil {
			t.Fatalf("Step: %v", err)
		}
		negExpectError(t, negGet(t, provider+"/v1/messages/"+pending.Receipt.MessageID), 410, "retention_expired")
		got := negBody(t, negGet(t, foundation+"/v1/messages/"+pending.Receipt.MessageID), 200)
		if !bytes.Equal(got, pendingBytes) {
			t.Errorf("vault bytes = %s; want the provider's %s", got, pendingBytes)
		}
	})
}

// negSetup starts one devnet on ephemeral ports and funds one author with 10,000 satoshis through the deposit routes.
func negSetup(t *testing.T) *negEnv {
	t.Helper()
	ctx := context.Background()
	clock := &negClock{now: negStart}
	d, err := startDevnet(ctx, devnetOptions{DataDir: t.TempDir(), Now: clock.Now, ClaimPeriod: time.Minute, Loops: false})
	if err != nil {
		t.Fatalf("startDevnet: %v", err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Errorf("devnet Close: %v", err)
		}
	})
	client, err := sdk.New(d.URL("provider"), sdk.Options{ServiceID: keys.DIDOf(devnet.ProviderKey(0)), AllowLoopbackHTTP: true, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	genesis := devnet.Genesis()
	root, err := sdk.BuildTrustRoot(sdk.Pin{ReleaseKeyIDs: genesis.ReleaseKeyIDs, Threshold: int(genesis.ReleaseThreshold)}, sdk.Publications{Genesis: genesis, FetchedAt: negStart})
	if err != nil {
		t.Fatal(err)
	}
	schedule, ok := root.ScheduleInForce(wire.BitcoinMainnet, negStart)
	if !ok {
		t.Fatal("no Bitcoin fee schedule in force at the devnet start")
	}
	journal, err := sdk.FileJournal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &negEnv{
		d: d, clock: clock, client: client, publisher: sdk.NewPublisher(client, root, journal, clock.Now), scheduleID: schedule.ScheduleID,
		author:   keys.PrivateKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{11}, ed25519.SeedSize))),
		unfunded: keys.PrivateKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{12}, ed25519.SeedSize))),
		delegate: keys.PrivateKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{13}, ed25519.SeedSize))),
	}
	fundingLog, err := sdk.FileFundingLog(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	invoice, _, err := client.RequestDeposit(ctx, e.author, sdk.DepositPlan{Network: wire.BitcoinMainnet, AmountChainUnits: "10000", DisclosureAcknowledged: true, Schedule: schedule, Confirmed: "0", Pending: "0"}, fundingLog)
	if err != nil {
		t.Fatalf("RequestDeposit: %v", err)
	}
	transfer, err := faux.NewClient(d.URL("faux"), nil).Submit(ctx, faux.SubmitRequest{Network: wire.BitcoinMainnet, Recipient: invoice.Recipient, AmountChainUnits: "10000", ClientRef: invoice.DepositID})
	if err != nil {
		t.Fatalf("faux Submit: %v", err)
	}
	observed := wire.DepositTransfer{Network: transfer.Network, TxID: transfer.TxID}
	if transfer.OutputIndex != nil {
		index := int64(*transfer.OutputIndex)
		observed.OutputIndex = &index
	}
	result, err := client.ObserveDeposit(ctx, e.author, invoice, observed, fundingLog)
	if err != nil || result.Credit == nil {
		t.Fatalf("ObserveDeposit = %+v, %v; want a committed credit", result, err)
	}
	return e
}

// negExpectError asserts the status, a JSON content type and exactly the closed PublicReadError body {"status": name}.
func negExpectError(t *testing.T, resp *http.Response, status int, name string) {
	t.Helper()
	body := negBody(t, resp, status)
	if mediaType := strings.TrimSpace(strings.SplitN(resp.Header.Get("Content-Type"), ";", 2)[0]); mediaType != "application/json" {
		t.Errorf("Content-Type = %q; want application/json", resp.Header.Get("Content-Type"))
	}
	var got struct {
		Status            string `json:"status"`
		RetryAfterSeconds *int64 `json:"retry_after_seconds"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&got); err != nil {
		t.Fatalf("error body %s is not a closed PublicReadError: %v", body, err)
	}
	if decoder.More() {
		t.Fatalf("error body %s has trailing data", body)
	}
	if got.Status != name {
		t.Errorf("error status = %q; want %q", got.Status, name)
	}
	retryable := status == 429 || status == 503
	switch {
	case retryable && (got.RetryAfterSeconds == nil || *got.RetryAfterSeconds < 1 || *got.RetryAfterSeconds > 86400):
		t.Errorf("retry_after_seconds = %v; want an integer from 1 to 86400", got.RetryAfterSeconds)
	case !retryable && got.RetryAfterSeconds != nil:
		t.Errorf("error body %s carries retry_after_seconds on a %d", body, status)
	}
}

// negBody reads and closes resp, failing unless it has the wanted status.
func negBody(t *testing.T, resp *http.Response, status int) []byte {
	t.Helper()
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s %s: %v", resp.Request.Method, resp.Request.URL.Path, err)
	}
	if resp.StatusCode != status {
		t.Fatalf("%s %s = %d %s; want %d", resp.Request.Method, resp.Request.URL.Path, resp.StatusCode, body, status)
	}
	return body
}

func negStatus(t *testing.T, resp *http.Response, status int) {
	t.Helper()
	negBody(t, resp, status)
}

func negDecode(t *testing.T, resp *http.Response, status int, into any) {
	t.Helper()
	body := negBody(t, resp, status)
	if err := json.Unmarshal(body, into); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
}

func negSend(t *testing.T, method, target string, body []byte, headers http.Header) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, target, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if headers != nil {
		request.Header = headers.Clone()
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, target, err)
	}
	return resp
}

func negGet(t *testing.T, target string) *http.Response {
	t.Helper()
	return negSend(t, http.MethodGet, target, nil, nil)
}

func negJSON(t *testing.T, v any) []byte {
	t.Helper()
	encoded, err := canon.Encode(v)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func negTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := wire.ParseTime(value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

// negServiceAuth obtains a one-use root challenge for method and resource over body and returns the signed service-auth headers.
func negServiceAuth(t *testing.T, e *negEnv, key keys.PrivateKey, method, resource string, body []byte) http.Header {
	t.Helper()
	hash, err := delegation.BodyHash(delegation.BodyJSON, body)
	if err != nil {
		t.Fatal(err)
	}
	request := negJSON(t, delegation.ChallengeRequest{AuthorID: keys.DIDOf(key), Method: method, Resource: resource, BodyHash: hash})
	var challenge wire.ServiceChallenge
	negDecode(t, negSend(t, http.MethodPost, e.d.URL("provider")+"/v1/service-challenges", request, nil), 200, &challenge)
	header, signature, err := delegation.SignChallenge(key, challenge)
	if err != nil {
		t.Fatal(err)
	}
	headers := http.Header{}
	headers.Set(delegation.HeaderChallenge, header)
	headers.Set(delegation.HeaderSignature, signature)
	return headers
}

func negBalance(t *testing.T, e *negEnv, key keys.PrivateKey) wire.BalanceStatus {
	t.Helper()
	status, err := e.client.Balance(context.Background(), key, wire.BitcoinMainnet)
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	return status
}

// negBalanceUnchanged asserts that the signed BalanceStatus, including its revision and signature, is the one read before the rejected request.
func negBalanceUnchanged(t *testing.T, e *negEnv, key keys.PrivateKey, before wire.BalanceStatus) {
	t.Helper()
	if after := negBalance(t, e, key); after != before {
		t.Errorf("BalanceStatus after the rejected request = %+v; want unchanged %+v", after, before)
	}
}

// negIntent returns a text/plain post intent by author at the devnet clock, signed through granted when it is not nil.
func negIntent(e *negEnv, author keys.PrivateKey, granted *wire.Delegation, text string) wire.Intent {
	intent := wire.Intent{
		AuthorID: keys.DIDOf(author), Timestamp: wire.FormatTime(e.clock.Now()), MessageType: "post",
		Version: wire.Version{Major: 2, Minor: 0}, Payload: wire.PostPayload{ContentFormat: "text/plain", Text: text},
	}
	if granted != nil {
		intent.SignerID, intent.Delegation = granted.SignerID, granted
	}
	return intent
}

func negMedia(id string) wire.MediaReference {
	return wire.MediaReference{ID: id, URL: "https://media.example/" + id + ".png", Kind: "image", SHA256: strings.Repeat("a", 64)}
}

// negPreflight posts a fee preflight for intent signed by signer under schedule 1.
func negPreflight(t *testing.T, e *negEnv, signer keys.PrivateKey, intent wire.Intent) *http.Response {
	t.Helper()
	id, err := message.ID(intent)
	if err != nil {
		t.Fatal(err)
	}
	authorization, err := fee.SignPreflight(fee.PreflightAuthorization(e.client.ServiceID(), intent.AuthorID, id, e.scheduleID), signer)
	if err != nil {
		t.Fatal(err)
	}
	request := negJSON(t, wire.FeePreflightRequest{ProviderID: e.client.ServiceID(), AuthorID: intent.AuthorID, ScheduleID: e.scheduleID, Intent: negJSON(t, intent), Authorization: authorization})
	return negSend(t, http.MethodPost, e.d.URL("provider")+"/v1/fee-quotes", request, nil)
}

func negQuote(t *testing.T, e *negEnv, signer keys.PrivateKey, intent wire.Intent) wire.FeeQuote {
	t.Helper()
	var quote wire.FeeQuote
	negDecode(t, negPreflight(t, e, signer, intent), 200, &quote)
	return quote
}

// negAdmission builds an AdmissionRequest presenting quoteID whose authorization signer signs over signedQuoteID.
func negAdmission(t *testing.T, e *negEnv, signer keys.PrivateKey, intent wire.Intent, quoteID, signedQuoteID string) []byte {
	t.Helper()
	id, err := message.ID(intent)
	if err != nil {
		t.Fatal(err)
	}
	authorization, err := fee.SignAdmission(fee.AdmissionAuthorization(e.client.ServiceID(), intent.AuthorID, id, signedQuoteID), signer)
	if err != nil {
		t.Fatal(err)
	}
	return negJSON(t, wire.AdmissionRequest{ProviderID: e.client.ServiceID(), Intent: intent, Authorization: authorization, QuoteID: quoteID})
}

// negAdmit quotes and admits intent for the funded author and returns its receipt.
func negAdmit(t *testing.T, e *negEnv, intent wire.Intent) wire.SubmissionReceipt {
	t.Helper()
	quote := negQuote(t, e, e.author, intent)
	var receipt wire.SubmissionReceipt
	negDecode(t, negSend(t, http.MethodPost, e.d.URL("provider")+"/v1/admissions", negAdmission(t, e, e.author, intent, quote.QuoteID, quote.QuoteID), nil), 201, &receipt)
	return receipt
}

// negPublish publishes a text post by the funded author through the SDK and returns the admitted result.
func negPublish(t *testing.T, e *negEnv, text string) sdk.Admitted {
	t.Helper()
	ctx := context.Background()
	quoted, err := e.publisher.QuoteDraft(ctx, sdk.Draft{ContentFormat: "text/plain", Text: text}, e.author, wire.BitcoinMainnet, nil)
	if err != nil {
		t.Fatalf("QuoteDraft: %v", err)
	}
	approval := sdk.Approval{MessageID: quoted.Prepared.MessageID, Amount: quoted.Quote.AmountBaseUnits, Network: quoted.Quote.Network, ApprovedAt: e.clock.Now()}
	admitted, err := e.publisher.Admit(ctx, e.author, quoted, approval)
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if _, err := e.publisher.Finalize(ctx, e.author, admitted); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	return admitted
}

// negStepPastClaimPeriod moves the clock past one dev claim period (one minute) and runs one devnet pass.
func negStepPastClaimPeriod(t *testing.T, e *negEnv) {
	t.Helper()
	e.clock.Advance(2 * time.Minute)
	if err := e.d.Step(context.Background()); err != nil {
		t.Fatalf("Step: %v", err)
	}
}

// negOtherB64 returns a different base64url character so a signature stays well-formed but no longer verifies.
func negOtherB64(c byte) byte {
	if c == 'A' {
		return 'B'
	}
	return 'A'
}
