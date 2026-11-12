package server

import (
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/distribution"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/sdk"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/wire"
)

// ldgFake serves one provider, five monitors and a Foundation for the Ledger tests.
type ldgFake struct {
	claims       []wire.SettlementClaim
	monitors     [5]wire.MonitorWitnessesPage
	messages     map[string][]byte
	settlements  map[string]wire.SettlementProof
	vault        map[string][]byte
	distribution []byte
	messageHits  atomic.Int32
}

func (f *ldgFake) provider(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/v1/settlement-claims":
		page := wire.SettlementClaimsPage{Claims: []wire.SettlementClaim{}}
		if r.URL.Query().Get("after_sequence") == "0" && f.claims != nil {
			page.Claims = f.claims
		}
		writeJSON(w, http.StatusOK, page)
	case strings.HasPrefix(r.URL.Path, "/v1/messages/"):
		f.messageHits.Add(1)
		raw, ok := f.messages[strings.TrimPrefix(r.URL.Path, "/v1/messages/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	case strings.HasPrefix(r.URL.Path, "/v1/settlements/"):
		proof, ok := f.settlements[strings.TrimPrefix(r.URL.Path, "/v1/settlements/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, proof)
	default:
		http.NotFound(w, r)
	}
}

func (f *ldgFake) monitor(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/m0/v1/witnesses":
		f.writeMonitor(w, 0)
	case "/m1/v1/witnesses":
		f.writeMonitor(w, 1)
	case "/m2/v1/witnesses":
		f.writeMonitor(w, 2)
	case "/m3/v1/witnesses":
		f.writeMonitor(w, 3)
	case "/m4/v1/witnesses":
		f.writeMonitor(w, 4)
	default:
		http.NotFound(w, r)
	}
}

// writeMonitor answers with one monitor's page, with empty lists where the fake holds none.
func (f *ldgFake) writeMonitor(w http.ResponseWriter, n int) {
	page := f.monitors[n]
	if page.Witnesses == nil {
		page.Witnesses = []wire.SettlementWitness{}
	}
	if page.Refusals == nil {
		page.Refusals = []wire.SettlementRefusal{}
	}
	if page.Holds == nil {
		page.Holds = []wire.SettlementHold{}
	}
	if page.Claims == nil {
		page.Claims = []wire.SettlementClaim{}
	}
	writeJSON(w, http.StatusOK, page)
}

func (f *ldgFake) foundation(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/v1/foundation/distribution":
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(f.distribution)
	case r.URL.Path == "/v1/foundation/vault-status":
		writeJSON(w, http.StatusOK, wire.VaultStatusPage{Statuses: []wire.VaultStatus{}})
	case strings.HasPrefix(r.URL.Path, "/v1/vault/receipts/"):
		raw, ok := f.vault[strings.TrimPrefix(r.URL.Path, "/v1/vault/receipts/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	default:
		http.NotFound(w, r)
	}
}

// ldgTrustRoot builds the dev trust root that the Ledger reads committees and schedules from.
func ldgTrustRoot(t *testing.T, now time.Time) *sdk.TrustRoot {
	t.Helper()
	genesis := devnet.Genesis()
	root, err := sdk.BuildTrustRoot(sdk.Pin{ReleaseKeyIDs: genesis.ReleaseKeyIDs, Threshold: int(genesis.ReleaseThreshold)}, sdk.Publications{
		Genesis:      genesis,
		Certificates: wire.FoundationCertificatesPage{Certificates: devnet.Certificates(now), Closures: []wire.FoundationKeyClosure{}, KeySetUpdates: []wire.ReleaseKeySetUpdate{}},
		FeeSchedules: wire.FeeSchedulesPage{Schedules: devnet.FeeSchedules()},
		Distribution: wire.DistributionPage{Policies: devnet.Policies(), Statements: []wire.DistributionStatement{}, Disclosures: devnet.Disclosures()},
		FetchedAt:    now,
		Endpoint:     "https://foundation.example",
	})
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// ldgClient starts the fakes and a client that reads from them.
func ldgClient(t *testing.T, now time.Time, fake *ldgFake, dev bool) (*Client, *httptest.Server) {
	t.Helper()
	provider := httptest.NewTLSServer(http.HandlerFunc(fake.provider))
	t.Cleanup(provider.Close)
	monitors := httptest.NewTLSServer(http.HandlerFunc(fake.monitor))
	t.Cleanup(monitors.Close)
	foundation := httptest.NewTLSServer(http.HandlerFunc(fake.foundation))
	t.Cleanup(foundation.Close)
	client, err := NewClient(Config{
		Dev:        dev,
		DataDir:    t.TempDir(),
		Now:        func() time.Time { return now },
		HTTPClient: provider.Client(),
		Providers:  []Endpoint{{URL: provider.URL, ServiceID: keys.DIDOf(devnet.ProviderKey(0)), Label: "Fixture provider"}},
		Monitors: []Endpoint{
			{URL: monitors.URL + "/m0", ServiceID: keys.DIDOf(devnet.MonitorKey(0)), Label: "Monitor 0"},
			{URL: monitors.URL + "/m1", ServiceID: keys.DIDOf(devnet.MonitorKey(1)), Label: "Monitor 1"},
			{URL: monitors.URL + "/m2", ServiceID: keys.DIDOf(devnet.MonitorKey(2)), Label: "Monitor 2"},
			{URL: monitors.URL + "/m3", ServiceID: keys.DIDOf(devnet.MonitorKey(3)), Label: "Monitor 3"},
			{URL: monitors.URL + "/m4", ServiceID: keys.DIDOf(devnet.MonitorKey(4)), Label: "Monitor 4"},
		},
		Foundation: Endpoint{URL: foundation.URL, ServiceID: keys.DIDOf(devnet.FoundationKey()), Label: "Fixture Foundation"},
	})
	if err != nil {
		t.Fatal(err)
	}
	client.app.trustMu.Lock()
	client.app.root = ldgTrustRoot(t, now)
	client.app.trustStatus = TrustStatus{State: TrustReady, FetchedAt: now, Endpoint: foundation.URL}
	client.app.trustMu.Unlock()
	return client, provider
}

// ldgClaimOf builds and signs a claim by the dev provider, linked to the previous claim when given.
func ldgClaimOf(t *testing.T, previous *wire.SettlementClaim, total int64, receipts int, start, end time.Time) wire.SettlementClaim {
	t.Helper()
	claim, err := settlement.BuildClaim(devnet.ProviderKey(0), settlement.ClaimInput{
		Network: wire.BitcoinMainnet, Previous: previous, PeriodStart: start, PeriodEnd: end,
		DeclarationID: ldgDeclarationID, ReceiptCount: receipts, Total: big.NewInt(total),
		ReceiptsRoot: strings.Repeat("ab", 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	return claim
}

func ldgWitnessOf(t *testing.T, monitor int, claim wire.SettlementClaim, at time.Time) wire.SettlementWitness {
	t.Helper()
	witness, err := settlement.SignWitness(devnet.MonitorKey(monitor), claim, big.NewInt(100), at)
	if err != nil {
		t.Fatal(err)
	}
	return witness
}

// ldgTwoClaims serves claim 1 (136 msat, witnessed by monitors 0 and 1) and claim 2 (272 msat, witnessed by monitors 0 to 3).
func ldgTwoClaims(t *testing.T, now time.Time) (*ldgFake, wire.SettlementClaim, wire.SettlementClaim) {
	t.Helper()
	start := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Minute)
	at := now.Add(-time.Minute)
	first := ldgClaimOf(t, nil, 136, 1, start, end)
	second := ldgClaimOf(t, &first, 272, 2, end, end.Add(time.Minute))
	fake := &ldgFake{claims: []wire.SettlementClaim{first, second}}
	fake.monitors[0].Witnesses = []wire.SettlementWitness{ldgWitnessOf(t, 0, first, at), ldgWitnessOf(t, 0, second, at)}
	fake.monitors[1].Witnesses = []wire.SettlementWitness{ldgWitnessOf(t, 1, first, at), ldgWitnessOf(t, 1, second, at)}
	fake.monitors[2].Witnesses = []wire.SettlementWitness{ldgWitnessOf(t, 2, second, at)}
	fake.monitors[3].Witnesses = []wire.SettlementWitness{ldgWitnessOf(t, 3, second, at)}
	return fake, first, second
}

// ldgFresh starts a client over its own copy of the two-claim fixture, so a subtest may change what it serves.
func ldgFresh(t *testing.T, now time.Time) (*ldgFake, *Client) {
	t.Helper()
	fake, _, _ := ldgTwoClaims(t, now)
	client, _ := ldgClient(t, now, fake, false)
	return fake, client
}

func ldgGet(h http.Handler, path string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	h.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	return response
}

func ldgWant(t *testing.T, body, want string) {
	t.Helper()
	if !strings.Contains(body, want) {
		t.Fatalf("page does not contain %q", want)
	}
}

func ldgLacks(t *testing.T, body, unwanted string) {
	t.Helper()
	if strings.Contains(body, unwanted) {
		t.Fatalf("page contains %q; want it absent", unwanted)
	}
}

// ldgSignedStatement is a Foundation distribution statement for one provider, signed with the dev Foundation key.
func ldgSignedStatement(t *testing.T) wire.DistributionStatement {
	t.Helper()
	statement, err := distribution.SignStatement(wire.DistributionStatement{
		FoundationID:         keys.DIDOf(devnet.FoundationKey()),
		Network:              wire.BitcoinMainnet,
		PeriodStart:          "2026-07-01T00:00:00Z",
		PeriodEnd:            "2026-10-01T00:00:00Z",
		PolicyID:             devnet.Policies()[0].PolicyID,
		ReceivedBaseUnits:    "1000",
		DistributedBaseUnits: "500",
		MonitorPayments:      []wire.MonitorPayment{},
		Entries: []wire.DistributionEntry{{
			ProviderID:           keys.DIDOf(devnet.ProviderKey(0)),
			SettledFeesBaseUnits: "1000",
			QualityBP:            6250,
			ReportIDs:            []string{},
			RetentionYears:       1,
			ProvenQuarters:       1,
			AmountBaseUnits:      "500",
			AccruedBaseUnits:     "500",
		}},
	}, devnet.FoundationKey())
	if err != nil {
		t.Fatal(err)
	}
	return statement
}

func TestLedgerPage(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	t.Run("no provider shows the empty state", func(t *testing.T) {
		response := ldgGet(testClientHandler(t), "/ledger")
		body := response.Body.String()
		if response.Code != http.StatusOK || !strings.Contains(body, "No provider is selected") || !strings.Contains(body, `href="/network"`) || strings.Contains(body, "faux chain") {
			t.Fatalf("ledger without provider = %d; want the empty state and no dev notice", response.Code)
		}
	})
	t.Run("dev mode states the faux chain", func(t *testing.T) {
		response := ldgGet(newTestClient(t, true).Handler(), "/ledger")
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "the faux chain stands in for Bitcoin and Ethereum") {
			t.Fatalf("dev ledger = %d; want the faux chain notice", response.Code)
		}
	})
	t.Run("quorum is counted from monitor pages", func(t *testing.T) {
		_, client := ldgFresh(t, now)
		body := ldgGet(client.Handler(), "/ledger").Body.String()
		ldgWant(t, body, "Claim 2 · Bitcoin")
		ldgWant(t, body, "272 msat")
		ldgWant(t, body, "4 of 5 committee monitors witnessed")
		ldgWant(t, body, "Quorum reached")
		ldgWant(t, body, "2 of 5 committee monitors witnessed")
		ldgWant(t, body, "No quorum on this claim")
		ldgWant(t, body, "Quorum is more than two-thirds: 4 of 5")
	})
	t.Run("a refusal is shown with its reason", func(t *testing.T) {
		fake, client := ldgFresh(t, now)
		refusal, err := settlement.SignRefusal(devnet.MonitorKey(4), fake.claims[1], settlement.ReasonUnfunded, now.Add(-time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		fake.monitors[4].Refusals = []wire.SettlementRefusal{refusal}
		ldgWant(t, ldgGet(client.Handler(), "/ledger").Body.String(), "Refused: unfunded")
	})
	t.Run("a witness from the wrong page does not count", func(t *testing.T) {
		fake, client := ldgFresh(t, now)
		first := fake.claims[0]
		fake.monitors[0].Witnesses = append(fake.monitors[0].Witnesses, ldgWitnessOf(t, 4, first, now.Add(-time.Minute)))
		body := ldgGet(client.Handler(), "/ledger?network=bitcoin-mainnet").Body.String()
		ldgWant(t, body, "2 of 5 committee monitors witnessed")
		ldgLacks(t, body, "3 of 5 committee monitors witnessed")
	})
	t.Run("a tampered claim is not verified", func(t *testing.T) {
		fake, client := ldgFresh(t, now)
		tampered := fake.claims[1]
		tampered.TotalBaseUnits = "999"
		fake.claims[1] = tampered
		ldgWant(t, ldgGet(client.Handler(), "/ledger").Body.String(), "Not verified: the claim ID or provider signature does not match")
	})
	t.Run("bad parameters are refused", func(t *testing.T) {
		_, client := ldgFresh(t, now)
		if response := ldgGet(client.Handler(), "/ledger?network=dogecoin"); response.Code != http.StatusBadRequest {
			t.Fatalf("network=dogecoin = %d; want 400", response.Code)
		}
		if response := ldgGet(client.Handler(), "/ledger?provider=7"); response.Code != http.StatusBadRequest {
			t.Fatalf("provider=7 = %d; want 400", response.Code)
		}
		if response := ldgGet(client.Handler(), "/ledger?after=-1"); response.Code != http.StatusBadRequest {
			t.Fatalf("after=-1 = %d; want 400", response.Code)
		}
	})
	t.Run("claims fragment and full page", func(t *testing.T) {
		_, client := ldgFresh(t, now)
		fragment := ldgGet(client.Handler(), "/ledger/claims?network=bitcoin-mainnet")
		if fragment.Code != http.StatusOK || !strings.HasPrefix(fragment.Body.String(), `<section id="claims"`) || strings.Contains(fragment.Body.String(), "<html") {
			t.Fatalf("claims fragment = %d; want the claims section alone", fragment.Code)
		}
		page := ldgGet(client.Handler(), "/ledger?network=bitcoin-mainnet")
		ldgWant(t, page.Body.String(), "<h1>Ledger</h1>")
		ldgWant(t, page.Body.String(), "Claim 2 · Bitcoin")
	})
	t.Run("fee schedules in force", func(t *testing.T) {
		_, client := ldgFresh(t, now)
		body := ldgGet(client.Handler(), "/ledger").Body.String()
		ldgWant(t, body, "Signed by the release keys (")
		ldgWant(t, body, "A post up to 1 KiB costs</dt><dd>136 msat")
		ldgWant(t, body, "48,900,000,000 wei")
	})
	t.Run("no distribution statement yet", func(t *testing.T) {
		fake, _, _ := ldgTwoClaims(t, now)
		fake.distribution = []byte(`{"disclosures":[],"policies":[],"statements":[]}`)
		client, _ := ldgClient(t, now, fake, false)
		ldgWant(t, ldgGet(client.Handler(), "/ledger").Body.String(), "has not published a distribution statement yet")
	})
	t.Run("monitor disclosures state each tie and count the monitors that have one", func(t *testing.T) {
		fake, _, _ := ldgTwoClaims(t, now)
		tied, free := keys.DIDOf(devnet.MonitorKey(0)), keys.DIDOf(devnet.MonitorKey(1))
		policy := "fydp2:" + strings.Repeat("a", 64)
		raw, err := json.Marshal(wire.DistributionPage{Policies: []wire.DistributionPolicy{}, Statements: []wire.DistributionStatement{}, Disclosures: []wire.MonitorDisclosure{
			{PolicyID: policy, MonitorID: tied, SelectionCriteria: "Chosen by lot from the applicants.", Relationships: []wire.MonitorRelationship{{Party: "foundation", Kind: "funding", Description: "Receives an annual grant."}}},
			{PolicyID: policy, MonitorID: free, SelectionCriteria: "Chosen by lot from the applicants.", Relationships: []wire.MonitorRelationship{}},
		}})
		if err != nil {
			t.Fatal(err)
		}
		fake.distribution = raw
		client, _ := ldgClient(t, now, fake, false)
		body := ldgGet(client.Handler(), "/ledger").Body.String()
		ldgWant(t, body, "1 of 2 monitors have a declared funding or ownership tie")
		ldgWant(t, body, "The Foundation: Receives an annual grant.")
		ldgWant(t, body, "No funding or ownership tie declared.")
	})
	t.Run("a signed distribution statement verifies", func(t *testing.T) {
		fake, _, _ := ldgTwoClaims(t, now)
		raw, err := json.Marshal(wire.DistributionPage{Policies: []wire.DistributionPolicy{}, Statements: []wire.DistributionStatement{ldgSignedStatement(t)}, Disclosures: []wire.MonitorDisclosure{}})
		if err != nil {
			t.Fatal(err)
		}
		fake.distribution = raw
		client, _ := ldgClient(t, now, fake, false)
		body := ldgGet(client.Handler(), "/ledger").Body.String()
		ldgWant(t, body, "62.50%")
		ldgWant(t, body, "Signed by Fixture Foundation")
		ldgLacks(t, body, "Not verified")
	})
	t.Run("a changed distribution statement is not verified", func(t *testing.T) {
		fake, _, _ := ldgTwoClaims(t, now)
		statement := ldgSignedStatement(t)
		statement.DistributedBaseUnits = "501"
		raw, err := json.Marshal(wire.DistributionPage{Policies: []wire.DistributionPolicy{}, Statements: []wire.DistributionStatement{statement}, Disclosures: []wire.MonitorDisclosure{}})
		if err != nil {
			t.Fatal(err)
		}
		fake.distribution = raw
		client, _ := ldgClient(t, now, fake, false)
		body := ldgGet(client.Handler(), "/ledger").Body.String()
		ldgWant(t, body, "Not verified")
	})
}

// ldgSettledFixture serves one network post whose receipt is settled: a two-leaf claim, witnessed by monitors 0 to 3, with the receipt as leaf 1.
func ldgSettledFixture(t *testing.T, now time.Time) (*ldgFake, wire.TransportMessage, time.Time) {
	t.Helper()
	issuedAt := now.Add(-24 * time.Hour)
	recordRaw, _ := signedNetworkRecord(t, issuedAt)
	var message wire.TransportMessage
	if err := wire.Decode(recordRaw, "TransportMessage", &message); err != nil {
		t.Fatal(err)
	}
	receipt := message.SubmissionReceipt
	sibling := receipt
	sibling.AmountBaseUnits = "1"
	leaf, err := settlement.LeafOf(receipt)
	if err != nil {
		t.Fatal(err)
	}
	siblingLeaf, err := settlement.LeafOf(sibling)
	if err != nil {
		t.Fatal(err)
	}
	leaves := []wire.SettlementLeaf{leaf, siblingLeaf}
	root, err := settlement.TreeRoot(leaves)
	if err != nil {
		t.Fatal(err)
	}
	claimStart := issuedAt.Add(-time.Hour)
	claim, err := settlement.BuildClaim(devnet.ProviderKey(0), settlement.ClaimInput{
		Network: wire.BitcoinMainnet, PeriodStart: claimStart, PeriodEnd: claimStart.Add(2 * time.Hour),
		DeclarationID: ldgDeclarationID, ReceiptCount: 2, Total: root.Sum,
		ReceiptsRoot: hex.EncodeToString(root.Hash[:]),
	})
	if err != nil {
		t.Fatal(err)
	}
	witnesses := []wire.SettlementWitness{ldgWitnessOf(t, 0, claim, now), ldgWitnessOf(t, 1, claim, now), ldgWitnessOf(t, 2, claim, now), ldgWitnessOf(t, 3, claim, now)}
	proof, err := settlement.BuildProof(claim, leaves, 0, witnesses, nil)
	if err != nil {
		t.Fatal(err)
	}
	fake := &ldgFake{
		messages:    map[string][]byte{message.MessageID: recordRaw},
		settlements: map[string]wire.SettlementProof{receipt.ReceiptID: proof},
		vault:       map[string][]byte{},
	}
	return fake, message, issuedAt
}

func TestLedgerFeeWalk(t *testing.T) {
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	t.Run("a non-canonical id is not looked up", func(t *testing.T) {
		fake, _, _ := ldgSettledFixture(t, now)
		client, _ := ldgClient(t, now, fake, false)
		before := fake.messageHits.Load()
		response := ldgGet(client.Handler(), "/ledger/fee?id=garden")
		if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), "Only network posts have a fee to follow.") || fake.messageHits.Load() != before {
			t.Fatalf("non-canonical id = %d; want a 404 without any source contacted", response.Code)
		}
	})
	t.Run("a settled fee walks to quorum", func(t *testing.T) {
		fake, message, _ := ldgSettledFixture(t, now)
		client, _ := ldgClient(t, now, fake, false)
		body := ldgGet(client.Handler(), "/ledger/fee?id="+message.MessageID).Body.String()
		ldgWant(t, body, "Matches the receipt")
		ldgWant(t, body, "Signed by Fixture provider")
		ldgWant(t, body, "This receipt is leaf 1 of 2. A path of 1 step links it to the claim")
		ldgWant(t, body, "4 of 5 committee monitors witnessed")
		ldgWant(t, body, "Settled: the fee is proven paid to the Foundation")
	})
	t.Run("a proof for another receipt is a problem", func(t *testing.T) {
		fake, message, _ := ldgSettledFixture(t, now)
		receipt := message.SubmissionReceipt
		other := receipt
		other.AmountBaseUnits = "2"
		otherLeaf, err := settlement.LeafOf(other)
		if err != nil {
			t.Fatal(err)
		}
		root, err := settlement.TreeRoot([]wire.SettlementLeaf{otherLeaf})
		if err != nil {
			t.Fatal(err)
		}
		claim, err := settlement.BuildClaim(devnet.ProviderKey(0), settlement.ClaimInput{
			Network: wire.BitcoinMainnet, PeriodStart: now.Add(-3 * time.Hour), PeriodEnd: now.Add(-time.Hour),
			DeclarationID: ldgDeclarationID, ReceiptCount: 1, Total: root.Sum,
			ReceiptsRoot: hex.EncodeToString(root.Hash[:]),
		})
		if err != nil {
			t.Fatal(err)
		}
		proof, err := settlement.BuildProof(claim, []wire.SettlementLeaf{otherLeaf}, 0, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		fake.settlements[receipt.ReceiptID] = proof
		client, _ := ldgClient(t, now, fake, false)
		body := ldgGet(client.Handler(), "/ledger/fee?id="+message.MessageID).Body.String()
		ldgWant(t, body, "The proof does not link this receipt to the claim: the audit path ends at a different receipts root.")
		ldgLacks(t, body, "This receipt is leaf")
	})
	t.Run("no proof yet is pending", func(t *testing.T) {
		fake, message, _ := ldgSettledFixture(t, now)
		fake.settlements = map[string]wire.SettlementProof{}
		client, _ := ldgClient(t, now, fake, false)
		body := ldgGet(client.Handler(), "/ledger/fee?id="+message.MessageID).Body.String()
		ldgWant(t, body, "No settlement proof yet")
		ldgWant(t, body, "Pending settlement")
		ldgWant(t, body, "not proof of payment")
	})
	t.Run("not found names the sources", func(t *testing.T) {
		fake, _, _ := ldgSettledFixture(t, now)
		client, _ := ldgClient(t, now, fake, false)
		missing := "fy1:" + strings.Repeat("0", 64)
		response := ldgGet(client.Handler(), "/ledger/fee?id="+missing)
		if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), "This post was not returned by your sources.") || !strings.Contains(response.Body.String(), "This does not mean it was deleted or never existed.") {
			t.Fatalf("missing post = %d; want the sources named", response.Code)
		}
	})
	t.Run("the vault receipt verifies", func(t *testing.T) {
		fake, message, _ := ldgSettledFixture(t, now)
		fake.vault[message.MessageID] = ldgVaultReceiptJSON(t, message.MessageID, time.Date(2026, 11, 12, 1, 0, 0, 0, time.UTC), "")
		client, _ := ldgClient(t, now, fake, false)
		response := ldgGet(client.Handler(), "/ledger/fee/vault?id="+message.MessageID)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Stored in the Lifetime Vault on") || !strings.Contains(response.Body.String(), "Verified by this client") || strings.Contains(response.Body.String(), "<html") {
			t.Fatalf("vault fragment = %d %q; want the verified stored line", response.Code, response.Body.String())
		}
		full := ldgGet(client.Handler(), "/ledger/fee?id="+message.MessageID+"&vault=1")
		ldgWant(t, full.Body.String(), "<h1>Follow the fee</h1>")
		ldgWant(t, full.Body.String(), "Stored in the Lifetime Vault on")
	})
	t.Run("a vault receipt changed after signing is not verified", func(t *testing.T) {
		fake, message, _ := ldgSettledFixture(t, now)
		fake.vault[message.MessageID] = ldgVaultReceiptJSON(t, message.MessageID, time.Date(2026, 11, 12, 1, 0, 0, 0, time.UTC), "2026-11-12T02:00:00Z")
		client, _ := ldgClient(t, now, fake, false)
		body := ldgGet(client.Handler(), "/ledger/fee/vault?id="+message.MessageID).Body.String()
		ldgWant(t, body, "could not be verified")
		ldgLacks(t, body, "Stored in the Lifetime Vault on")
	})
}

// ldgVaultReceiptJSON signs a Lifetime Vault receipt for one message with the dev Foundation key.
// When storedAt is set, the receipt is altered to that time after signing.
func ldgVaultReceiptJSON(t *testing.T, id string, stored time.Time, storedAt string) []byte {
	t.Helper()
	receipt := wire.VaultReceipt{FoundationID: keys.DIDOf(devnet.FoundationKey()), MessageID: id, StoredAt: wire.FormatTime(stored)}
	canonical, err := canon.EncodeWithout(receipt, "foundation_signature")
	if err != nil {
		t.Fatal(err)
	}
	receipt.FoundationSignature, err = keys.Sign(devnet.FoundationKey(), "vault-receipt", canonical)
	if err != nil {
		t.Fatal(err)
	}
	if storedAt != "" {
		receipt.StoredAt = storedAt
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestLedgerHooks(t *testing.T) {
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	t.Run("the sidebar links the ledger", func(t *testing.T) {
		body := ldgGet(testClientHandler(t), "/").Body.String()
		if !strings.Contains(body, `href="/ledger"`) || !strings.Contains(body, `aria-label="Ledger"`) {
			t.Fatal("sidebar does not link the Ledger")
		}
	})
	t.Run("a demo post offers no follow the fee action", func(t *testing.T) {
		body := ldgGet(testClientHandler(t), "/posts/garden").Body.String()
		if strings.Contains(body, "/ledger/fee") {
			t.Fatal("demo post offers a fee walk")
		}
	})
	t.Run("a network post offers follow the fee", func(t *testing.T) {
		fake, message, _ := ldgSettledFixture(t, now)
		client, _ := ldgClient(t, now, fake, false)
		body := ldgGet(client.Handler(), "/posts/"+message.MessageID).Body.String()
		ldgWant(t, body, `href="/ledger/fee?id=`+strings.Replace(message.MessageID, ":", "%3a", 1)+`"`)
	})
}

// ldgDeclarationID is a well-formed retention declaration ID for synthetic claims.
const ldgDeclarationID = "fyrd2:" + "cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd"
