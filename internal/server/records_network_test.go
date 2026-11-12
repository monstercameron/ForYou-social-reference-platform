package server

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/fee"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/message"
	"foryou.local/foryou/internal/sdk"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/wire"
)

var nrTestNow = time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)

// nrTestRecord signs a post by the fixture author, admitted by devnet provider 0.
func nrTestRecord(t *testing.T, text string, issuedAt time.Time, replyTo string) ([]byte, wire.TransportMessage) {
	t.Helper()
	author := ed25519.NewKeyFromSeed([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31})
	schedule := devnet.Genesis().FeeSchedules[0]
	intent := wire.Intent{AuthorID: keys.DIDOf(author), Timestamp: wire.FormatTime(issuedAt), MessageType: "post", Version: wire.Version{Major: 2, Minor: 0}, Payload: wire.PostPayload{ContentFormat: "text/plain", Text: text, ReplyTo: replyTo}}
	id, err := message.ID(intent)
	if err != nil {
		t.Fatal(err)
	}
	intentBytes, err := canon.Encode(intent)
	if err != nil {
		t.Fatal(err)
	}
	amount, err := fee.Amount(schedule, len(intentBytes))
	if err != nil {
		t.Fatal(err)
	}
	receiptID := sha256.Sum256([]byte(text))
	provider := devnet.ProviderKey(0)
	receipt := wire.SubmissionReceipt{ProviderID: keys.DIDOf(provider), AuthorID: intent.AuthorID, MessageID: id, ReceiptID: keys.EncodeB64URL(receiptID[:]), QuoteID: "fyfq2:0000000000000000000000000000000000000000000000000000000000000000", ScheduleID: schedule.ScheduleID, Network: schedule.Network, AmountBaseUnits: amount.String(), AuthorizationAt: wire.FormatTime(issuedAt), IssuedAt: wire.FormatTime(issuedAt)}
	if receipt, err = fee.SignReceipt(receipt, provider); err != nil {
		t.Fatal(err)
	}
	signed, err := message.Assemble(intent, receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := message.Sign(&signed, author); err != nil {
		t.Fatal(err)
	}
	raw, err := canon.Encode(signed)
	if err != nil {
		t.Fatal(err)
	}
	var decoded wire.TransportMessage
	if err := wire.Decode(raw, "TransportMessage", &decoded); err != nil {
		t.Fatal(err)
	}
	return raw, decoded
}

// nrTestProof is a settlement proof of one receipt witnessed by four of the five devnet monitors.
func nrTestProof(t *testing.T, receipt wire.SubmissionReceipt) []byte {
	t.Helper()
	leaf, err := settlement.LeafOf(receipt)
	if err != nil {
		t.Fatal(err)
	}
	root, err := settlement.TreeRoot([]wire.SettlementLeaf{leaf})
	if err != nil {
		t.Fatal(err)
	}
	end := nrTestNow.Add(-time.Hour)
	claim, err := settlement.BuildClaim(devnet.ProviderKey(0), settlement.ClaimInput{Network: receipt.Network, PeriodStart: end.Add(-24 * time.Hour), PeriodEnd: end, DeclarationID: "fyrd2:" + strings.Repeat("0", 64), ReceiptCount: 1, Total: root.Sum, ReceiptsRoot: hex.EncodeToString(root.Hash[:])})
	if err != nil {
		t.Fatal(err)
	}
	witness := func(n int) wire.SettlementWitness {
		w, err := settlement.SignWitness(devnet.MonitorKey(n), claim, big.NewInt(100), end)
		if err != nil {
			t.Fatal(err)
		}
		return w
	}
	proof, err := canon.Encode(wire.SettlementProof{Claim: claim, Leaf: leaf, AuditPath: []wire.AuditNode{}, Witnesses: []wire.SettlementWitness{witness(0), witness(1), witness(2), witness(3)}})
	if err != nil {
		t.Fatal(err)
	}
	return proof
}

func nrTestTamper(t *testing.T, raw []byte) []byte {
	t.Helper()
	var m wire.TransportMessage
	if err := wire.Decode(raw, "TransportMessage", &m); err != nil {
		t.Fatal(err)
	}
	m.Payload.Text = "tampered text"
	tampered, err := canon.Encode(m)
	if err != nil {
		t.Fatal(err)
	}
	return tampered
}

func nrTestJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

const nrNotFound = `{"status":"not_found"}`

func nrTestPage(messages string) string {
	return `{"expires_at":"` + wire.FormatTime(nrTestNow.Add(30*time.Minute)) + `","generated_at":"` + wire.FormatTime(nrTestNow) + `","messages":[` + messages + `],"next_cursor":null}`
}

// nrTestClient builds a client over the given sources with the devnet trust root in place.
func nrTestClient(t *testing.T, dir string, clock *time.Time, providers []Endpoint, vault Endpoint) *Client {
	t.Helper()
	client, err := NewClient(Config{Dev: true, DataDir: dir, Now: func() time.Time { return *clock }, Providers: providers, Foundation: vault})
	if err != nil {
		t.Fatal(err)
	}
	genesis := devnet.Genesis()
	root, err := sdk.BuildTrustRoot(sdk.Pin{ReleaseKeyIDs: genesis.ReleaseKeyIDs, Threshold: int(genesis.ReleaseThreshold)}, sdk.Publications{Genesis: genesis, FetchedAt: nrTestNow, Endpoint: "https://foundation.example"})
	if err != nil {
		t.Fatal(err)
	}
	client.app.trustMu.Lock()
	client.app.root = root
	client.app.trustStatus = TrustStatus{State: TrustReady, FetchedAt: nrTestNow}
	client.app.trustMu.Unlock()
	return client
}

func nrTestRequest() *http.Request { return httptest.NewRequest(http.MethodGet, "/", nil) }

func TestNetworkRecordGet(t *testing.T) {
	raw, m := nrTestRecord(t, "A record read through the network.", nrTestNow.Add(-time.Hour), "")
	providerID := keys.DIDOf(devnet.ProviderKey(0))
	restricting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nrTestJSON(w, http.StatusForbidden, `{"status":"policy_restricted"}`)
	}))
	defer restricting.Close()
	absent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { nrTestJSON(w, http.StatusNotFound, nrNotFound) }))
	defer absent.Close()
	var proofReady atomic.Bool
	serving := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/messages/"+m.MessageID:
			nrTestJSON(w, http.StatusOK, string(raw))
		case r.URL.Path == "/v1/settlements/"+m.SubmissionReceipt.ReceiptID && proofReady.Load():
			nrTestJSON(w, http.StatusOK, string(nrTestProof(t, m.SubmissionReceipt)))
		default:
			nrTestJSON(w, http.StatusNotFound, nrNotFound)
		}
	}))
	defer serving.Close()
	clock := nrTestNow
	t.Run("found at the second provider after the first restricts it", func(t *testing.T) {
		client := nrTestClient(t, t.TempDir(), &clock, []Endpoint{{URL: restricting.URL, ServiceID: keys.DIDOf(devnet.ProviderKey(1)), Label: "First provider"}, {URL: serving.URL, ServiceID: providerID, Label: "Second provider"}}, Endpoint{})
		p, coverage, found, err := client.app.records().Get(t.Context(), nrTestRequest(), m.MessageID)
		if err != nil || !found || p.Text != "A record read through the network." {
			t.Fatalf("Get = %q, found %v, error %v; want the record from the second provider", p.Text, found, err)
		}
		if got := coverage.String(); got != "First provider: restricted by policy here · Second provider: returned the record" {
			t.Fatalf("coverage = %q", got)
		}
	})
	t.Run("absent from every source is not reported as deleted", func(t *testing.T) {
		client := nrTestClient(t, t.TempDir(), &clock, []Endpoint{{URL: restricting.URL, ServiceID: keys.DIDOf(devnet.ProviderKey(1)), Label: "First provider"}, {URL: absent.URL, ServiceID: providerID, Label: "Second provider"}}, Endpoint{URL: absent.URL, ServiceID: keys.DIDOf(devnet.FoundationKey()), Label: "Vault"})
		_, coverage, found, err := client.app.records().Get(t.Context(), nrTestRequest(), m.MessageID)
		if err != nil || found {
			t.Fatalf("Get found %v, error %v; want not found", found, err)
		}
		if got := coverage.String(); got != "No selected source returned this record. That is not proof that it does not exist. · First provider: restricted by policy here · Second provider: not found here · Vault: not found here" {
			t.Fatalf("coverage = %q", got)
		}
	})
	t.Run("pending when read from the admitting provider's own routes", func(t *testing.T) {
		client := nrTestClient(t, t.TempDir(), &clock, []Endpoint{{URL: serving.URL, ServiceID: providerID, Label: "Provider"}}, Endpoint{})
		p, _, found, err := client.app.records().Get(t.Context(), nrTestRequest(), m.MessageID)
		if err != nil || !found || p.Settlement != "pending_settlement" || p.Class != "locally_admitted" {
			t.Fatalf("Get = settlement %q class %q, found %v, error %v; want pending and admitted", p.Settlement, p.Class, found, err)
		}
	})
	t.Run("unsettled when only the vault returns it", func(t *testing.T) {
		client := nrTestClient(t, t.TempDir(), &clock, []Endpoint{{URL: absent.URL, ServiceID: providerID, Label: "Provider"}}, Endpoint{URL: serving.URL, ServiceID: keys.DIDOf(devnet.FoundationKey()), Label: "Vault"})
		p, _, found, err := client.app.records().Get(t.Context(), nrTestRequest(), m.MessageID)
		if err != nil || !found || p.Settlement != "unsettled" {
			t.Fatalf("Get = settlement %q, found %v, error %v; want unsettled from the vault", p.Settlement, found, err)
		}
	})
	t.Run("settled when the provider serves a proof", func(t *testing.T) {
		proofReady.Store(true)
		defer proofReady.Store(false)
		client := nrTestClient(t, t.TempDir(), &clock, []Endpoint{{URL: serving.URL, ServiceID: providerID, Label: "Provider"}}, Endpoint{})
		p, _, found, err := client.app.records().Get(t.Context(), nrTestRequest(), m.MessageID)
		if err != nil || !found || p.Settlement != "settled" {
			t.Fatalf("Get = settlement %q, found %v, error %v; want settled", p.Settlement, found, err)
		}
	})
}

func TestNetworkRecordSync(t *testing.T) {
	raw, m := nrTestRecord(t, "A record from the change stream.", nrTestNow.Add(-time.Hour), "")
	providerID := keys.DIDOf(devnet.ProviderKey(0))
	t.Run("a second run resumes after the stored next_after", func(t *testing.T) {
		var mu sync.Mutex
		var afters []string
		provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/changes" {
				nrTestJSON(w, http.StatusNotFound, nrNotFound)
				return
			}
			mu.Lock()
			afters = append(afters, r.URL.Query().Get("after"))
			mu.Unlock()
			if r.URL.Query().Get("after") == "0" {
				nrTestJSON(w, http.StatusOK, `{"items":[{"message":`+string(raw)+`,"sequence":1},{"message":`+string(nrTestTamper(t, raw))+`,"sequence":2}],"more":false,"next_after":2,"provider_id":"`+providerID+`"}`)
				return
			}
			nrTestJSON(w, http.StatusOK, `{"items":[],"more":false,"next_after":2,"provider_id":"`+providerID+`"}`)
		}))
		defer provider.Close()
		clock := nrTestNow
		client := nrTestClient(t, t.TempDir(), &clock, []Endpoint{{URL: provider.URL, ServiceID: providerID, Label: "Provider"}}, Endpoint{})
		if err := client.RunOnce(t.Context()); err != nil {
			t.Fatal(err)
		}
		page, err := client.app.records().Recent(t.Context(), nrTestRequest(), "", 10)
		if err != nil || len(page.Posts) != 1 || page.Posts[0].ID != m.MessageID {
			t.Fatalf("Recent after the first run = %d posts, error %v; want only the verified record", len(page.Posts), err)
		}
		if got := page.Coverage.Sources[0].Text; got != "synced 0 min ago: 1 records kept, 1 failed verification" {
			t.Fatalf("sync coverage = %q", got)
		}
		if err := client.RunOnce(t.Context()); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		defer mu.Unlock()
		if strings.Join(afters, ",") != "0,2" {
			t.Fatalf("requested after = %v; want 0 then the stored 2", afters)
		}
	})
	t.Run("a rate limited source is skipped until its retry time", func(t *testing.T) {
		var requests atomic.Int32
		provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/changes" {
				nrTestJSON(w, http.StatusNotFound, nrNotFound)
				return
			}
			requests.Add(1)
			nrTestJSON(w, http.StatusTooManyRequests, `{"retry_after_seconds":30,"status":"rate_limited"}`)
		}))
		defer provider.Close()
		clock := nrTestNow
		client := nrTestClient(t, t.TempDir(), &clock, []Endpoint{{URL: provider.URL, ServiceID: providerID, Label: "Provider"}}, Endpoint{})
		_ = client.RunOnce(t.Context())
		clock = nrTestNow.Add(20 * time.Second)
		_ = client.RunOnce(t.Context())
		if got := requests.Load(); got != 1 {
			t.Fatalf("change requests inside the retry delay = %d; want 1", got)
		}
		clock = nrTestNow.Add(31 * time.Second)
		_ = client.RunOnce(t.Context())
		if got := requests.Load(); got != 2 {
			t.Fatalf("change requests after the retry delay = %d; want 2", got)
		}
	})
}

func TestNetworkRecordRecheck(t *testing.T) {
	providerID := keys.DIDOf(devnet.ProviderKey(0))
	oldRaw, old := nrTestRecord(t, "A receipt waiting for settlement.", nrTestNow.Add(-6*24*time.Hour), "")
	freshRaw, fresh := nrTestRecord(t, "A fresh receipt from the same provider.", nrTestNow.Add(-time.Hour), "")
	var proofReady atomic.Bool
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/messages/"+old.MessageID:
			nrTestJSON(w, http.StatusOK, string(oldRaw))
		case r.URL.Path == "/v1/messages/"+fresh.MessageID:
			nrTestJSON(w, http.StatusOK, string(freshRaw))
		case r.URL.Path == "/v1/settlements/"+old.SubmissionReceipt.ReceiptID && proofReady.Load():
			nrTestJSON(w, http.StatusOK, string(nrTestProof(t, old.SubmissionReceipt)))
		case r.URL.Path == "/v1/changes":
			nrTestJSON(w, http.StatusOK, `{"items":[],"more":false,"next_after":0,"provider_id":"`+providerID+`"}`)
		default:
			nrTestJSON(w, http.StatusNotFound, nrNotFound)
		}
	}))
	defer provider.Close()
	held := func(t *testing.T, client *Client) (freshState, oldState string) {
		t.Helper()
		page, err := client.app.records().Recent(t.Context(), nrTestRequest(), "", 10)
		if err != nil || len(page.Posts) != 2 || page.Posts[0].ID != fresh.MessageID || page.Posts[1].ID != old.MessageID {
			t.Fatalf("Recent = %d posts, error %v; want the fresh record, then the old one", len(page.Posts), err)
		}
		return page.Posts[0].Settlement, page.Posts[1].Settlement
	}
	setup := func(t *testing.T, clock *time.Time) *Client {
		t.Helper()
		client := nrTestClient(t, t.TempDir(), clock, []Endpoint{{URL: provider.URL, ServiceID: providerID, Label: "Provider"}}, Endpoint{})
		_, _, oldFound, oldErr := client.app.records().Get(t.Context(), nrTestRequest(), old.MessageID)
		_, _, freshFound, freshErr := client.app.records().Get(t.Context(), nrTestRequest(), fresh.MessageID)
		if !oldFound || !freshFound || oldErr != nil || freshErr != nil {
			t.Fatalf("setup reads found %v %v, errors %v %v", oldFound, freshFound, oldErr, freshErr)
		}
		return client
	}
	t.Run("a pending record whose proof appears is settled", func(t *testing.T) {
		clock := nrTestNow.Add(-time.Hour)
		client := setup(t, &clock)
		proofReady.Store(true)
		defer proofReady.Store(false)
		clock = nrTestNow
		if err := client.RunOnce(t.Context()); err != nil {
			t.Fatal(err)
		}
		if freshState, oldState := held(t, client); oldState != "settled" || freshState != "pending_settlement" {
			t.Fatalf("settlement after recheck = old %q, fresh %q; want settled and still pending", oldState, freshState)
		}
	})
	t.Run("seven days without a proof is delinquent", func(t *testing.T) {
		clock := nrTestNow
		client := setup(t, &clock)
		clock = nrTestNow.Add(24 * time.Hour)
		if err := client.RunOnce(t.Context()); err != nil {
			t.Fatal(err)
		}
		if freshState, oldState := held(t, client); oldState != "unsettled" || freshState != "unsettled" {
			t.Fatalf("settlement after seven days = old %q, fresh %q; want both unsettled", oldState, freshState)
		}
		if got := client.app.ledger().Records(); len(got) != 1 || got[0].ReceiptID != old.SubmissionReceipt.ReceiptID {
			t.Fatalf("delinquency records = %+v; want the old receipt", got)
		}
	})
}

func TestNetworkRecordReplies(t *testing.T) {
	_, parent := nrTestRecord(t, "A parent post.", nrTestNow.Add(-2*time.Hour), "")
	replyRaw, reply := nrTestRecord(t, "A reply both providers hold.", nrTestNow.Add(-time.Hour), parent.MessageID)
	serve := func(hits *atomic.Int32, messages string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			if r.URL.Path != "/v1/replies/"+parent.MessageID {
				nrTestJSON(w, http.StatusNotFound, nrNotFound)
				return
			}
			nrTestJSON(w, http.StatusOK, nrTestPage(messages))
		}))
	}
	var first, second, third, fourth, fifth atomic.Int32
	servers := []*httptest.Server{serve(&first, string(replyRaw)), serve(&second, string(replyRaw)), serve(&third, ""), serve(&fourth, ""), serve(&fifth, string(replyRaw))}
	defer servers[0].Close()
	defer servers[1].Close()
	defer servers[2].Close()
	defer servers[3].Close()
	defer servers[4].Close()
	clock := nrTestNow
	client := nrTestClient(t, t.TempDir(), &clock, []Endpoint{
		{URL: servers[0].URL, ServiceID: keys.DIDOf(devnet.ProviderKey(0)), Label: "One"},
		{URL: servers[1].URL, ServiceID: keys.DIDOf(devnet.ProviderKey(1)), Label: "Two"},
		{URL: servers[2].URL, ServiceID: keys.DIDOf(devnet.ProviderKey(2)), Label: "Three"},
		{URL: servers[3].URL, ServiceID: keys.DIDOf(devnet.ProviderKey(3)), Label: "Four"},
		{URL: servers[4].URL, ServiceID: keys.DIDOf(devnet.ProviderKey(4)), Label: "Five"},
	}, Endpoint{})
	t.Run("at most four sources are asked and a shared reply appears once", func(t *testing.T) {
		page, err := client.app.records().Replies(t.Context(), nrTestRequest(), parent.MessageID)
		if err != nil || len(page.Posts) != 1 || page.Posts[0].ID != reply.MessageID || page.Posts[0].ReplyTo != parent.MessageID {
			t.Fatalf("Replies = %d posts, error %v; want the one reply", len(page.Posts), err)
		}
		if first.Load() != 1 || second.Load() != 1 || third.Load() != 1 || fourth.Load() != 1 || fifth.Load() != 0 {
			t.Fatalf("requests per source = %d %d %d %d %d; want 1 1 1 1 0", first.Load(), second.Load(), third.Load(), fourth.Load(), fifth.Load())
		}
		if page.Coverage.Note != "Asked the first 4 of your 5 sources" {
			t.Fatalf("coverage note = %q", page.Coverage.Note)
		}
	})
}

func TestNetworkRecordStoreReload(t *testing.T) {
	raw, m := nrTestRecord(t, "A record kept across restarts.", nrTestNow.Add(-time.Hour), "")
	var gone atomic.Bool
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/messages/"+m.MessageID && !gone.Load() {
			nrTestJSON(w, http.StatusOK, string(raw))
			return
		}
		nrTestJSON(w, http.StatusNotFound, nrNotFound)
	}))
	defer provider.Close()
	dir := t.TempDir()
	clock := nrTestNow
	endpoints := []Endpoint{{URL: provider.URL, ServiceID: keys.DIDOf(devnet.ProviderKey(0)), Label: "Provider"}}
	if _, _, found, err := nrTestClient(t, dir, &clock, endpoints, Endpoint{}).app.records().Get(t.Context(), nrTestRequest(), m.MessageID); !found || err != nil {
		t.Fatalf("first Get found %v, error %v", found, err)
	}
	gone.Store(true)
	restarted := nrTestClient(t, dir, &clock, endpoints, Endpoint{})
	t.Run("a new client on the same data directory reads the held record", func(t *testing.T) {
		p, coverage, found, err := restarted.app.records().Get(t.Context(), nrTestRequest(), m.MessageID)
		if err != nil || !found || p.Text != "A record kept across restarts." || !strings.HasPrefix(coverage.String(), "Local copy: held by this server since 2026-11-12") {
			t.Fatalf("Get after restart = %q, found %v, coverage %q, error %v", p.Text, found, coverage.String(), err)
		}
	})
	t.Run("search covers only held records and says so", func(t *testing.T) {
		page, err := restarted.app.records().Search(t.Context(), nrTestRequest(), "KEPT ACROSS", 10)
		if err != nil || len(page.Posts) != 1 || page.Posts[0].ID != m.MessageID || page.Coverage.Note != "Searching records this server has fetched" {
			t.Fatalf("Search = %d posts, note %q, error %v", len(page.Posts), page.Coverage.Note, err)
		}
	})
}
