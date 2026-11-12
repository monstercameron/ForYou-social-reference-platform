package server

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/retention"
	"foryou.local/foryou/internal/sdk"
	"foryou.local/foryou/internal/wire"
)

var netTestNow = time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)

var netTestProfile = base64.RawURLEncoding.EncodeToString(make([]byte, 32))

type netTestProvider struct {
	key       keys.PrivateKey
	label     string
	mu        sync.Mutex
	manifest  []byte
	retention []byte
	server    *httptest.Server
}

func netFixtureProvider(t *testing.T, seed byte, label string) *netTestProvider {
	t.Helper()
	key, err := keys.NewKeyFromSeed(bytes.Repeat([]byte{seed}, 32))
	if err != nil {
		t.Fatal(err)
	}
	p := &netTestProvider{key: key, label: label}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/foryou-operator.json", func(w http.ResponseWriter, r *http.Request) {
		p.serve(w, func() []byte { return p.manifest })
	})
	mux.HandleFunc("GET /v1/retention", func(w http.ResponseWriter, r *http.Request) {
		p.serve(w, func() []byte { return p.retention })
	})
	p.server = httptest.NewServer(mux)
	t.Cleanup(p.server.Close)
	return p
}

func (p *netTestProvider) serve(w http.ResponseWriter, body func() []byte) {
	p.mu.Lock()
	raw := body()
	p.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

func (p *netTestProvider) publish(manifest []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.manifest = manifest
}

func (p *netTestProvider) operatorKey() string { return keys.DIDOf(p.key) }

func netSignedManifest(t *testing.T, key keys.PrivateKey, version int64, digest string) []byte {
	t.Helper()
	m := wire.OperatorManifest{OperatorID: keys.DIDOf(key), ManifestVersion: version, IssuedAt: "2026-01-01T00:00:00Z", ExpiresAt: "2026-01-31T00:00:00Z", ServiceRoles: []string{"storage"}, ServiceEndpoints: []string{"https://operator.example"}, SupportedProfiles: []string{"ForYouExportSet-1"}, PolicyURL: "https://operator.example/policy", PolicyDigest: digest, LegalContact: "legal@example.org", AbuseContact: "abuse@example.org", PrivacyContact: "privacy@example.org", AppealURL: "https://operator.example/appeal", MarketsAssessed: []string{}}
	body, err := canon.EncodeWithout(m, "signature")
	if err != nil {
		t.Fatal(err)
	}
	m.Signature, err = keys.Sign(key, "operator-manifest", body)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := canon.Encode(m)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func netFixtureClient(t *testing.T, defaults ...*netTestProvider) *Client {
	t.Helper()
	endpoints := make([]Endpoint, 0, len(defaults))
	for _, p := range defaults {
		endpoints = append(endpoints, Endpoint{URL: p.server.URL, ServiceID: p.operatorKey(), Label: p.label})
	}
	client, err := NewClient(Config{DataDir: t.TempDir(), Dev: true, Now: func() time.Time { return netTestNow }, Providers: endpoints, DefaultList: ListDisclosure{Controller: "Example Foundation", Funding: "donations", Version: "1", Criteria: "signed manifests", Monitors: "two monitors", Conflicts: "none known"}})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func netGet(h http.Handler, path string, headers ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.AddCookie(&http.Cookie{Name: profileCookie, Value: netTestProfile})
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func netPost(h http.Handler, path string, form url.Values, headers ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: profileCookie, Value: netTestProfile})
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestNetworkProviders(t *testing.T) {
	t.Run("defaults are listed with the disclosure and removing one hides it until restored", func(t *testing.T) {
		provider := netFixtureProvider(t, 1, "Default operator")
		client := netFixtureClient(t, provider)
		h := client.Handler()
		page := netGet(h, "/network").Body.String()
		if !strings.Contains(page, "Default list by Example Foundation, funded by donations, version 1") || !strings.Contains(page, "Two endpoints or keys do not prove independent operators.") || !strings.Contains(page, "Default operator") {
			t.Fatal("default list disclosure or default provider missing")
		}
		removed := netPost(h, "/network/providers/remove", url.Values{"endpoint": {provider.server.URL}})
		if removed.Code != http.StatusSeeOther || !strings.Contains(netGet(h, "/network").Body.String(), "Removed defaults") {
			t.Fatalf("removing the default returned %d without a restore control", removed.Code)
		}
		restored := netPost(h, "/network/defaults/restore", url.Values{})
		if restored.Code != http.StatusSeeOther || !strings.Contains(netGet(h, "/network").Body.String(), "Default operator") {
			t.Fatalf("restore returned %d; default provider not back", restored.Code)
		}
	})

	t.Run("trust switch changes the publishing provider", func(t *testing.T) {
		provider := netFixtureProvider(t, 2, "Default operator")
		client := netFixtureClient(t, provider)
		h := client.Handler()
		if _, ok := client.app.publishingProvider(netTestProfile); !ok {
			t.Fatal("a default provider is trusted by default and should publish")
		}
		netPost(h, "/network/providers/trust", url.Values{"endpoint": {provider.server.URL}, "trusted": {"0"}})
		if _, ok := client.app.publishingProvider(netTestProfile); ok {
			t.Error("untrusted provider still publishes")
		}
		netPost(h, "/network/providers/trust", url.Values{"endpoint": {provider.server.URL}, "trusted": {"1"}})
		if _, ok := client.app.publishingProvider(netTestProfile); !ok {
			t.Error("trusted provider does not publish")
		}
	})

	t.Run("use for publishing moves the chosen provider to the front", func(t *testing.T) {
		first := netFixtureProvider(t, 9, "First operator")
		second := netFixtureProvider(t, 10, "Second operator")
		client := netFixtureClient(t, first, second)
		h := client.Handler()
		if provider, _ := client.app.publishingProvider(netTestProfile); provider.Endpoint != first.server.URL {
			t.Fatal("the first configured provider should publish by default")
		}
		netPost(h, "/network/providers/primary", url.Values{"endpoint": {second.server.URL}})
		if provider, _ := client.app.publishingProvider(netTestProfile); provider.Endpoint != second.server.URL {
			t.Error("chosen provider does not publish after Use for publishing")
		}
	})

	t.Run("adding a provider verifies its manifest against the entered key and leaves it untrusted", func(t *testing.T) {
		manual := netFixtureProvider(t, 3, "")
		manual.publish(netSignedManifest(t, manual.key, 1, strings.Repeat("a", 64)))
		client := netFixtureClient(t)
		h := client.Handler()
		added := netPost(h, "/network/providers", url.Values{"endpoint": {manual.server.URL}, "operator_key": {manual.operatorKey()}})
		page := netGet(h, "/network").Body.String()
		if added.Code != http.StatusSeeOther || !strings.Contains(page, "Added by you") || !strings.Contains(page, "Not trusted") || strings.Contains(page, "Manifest not verified") {
			t.Fatalf("verified add returned %d; provider not listed as untrusted and verified", added.Code)
		}
	})

	t.Run("a manifest signed by another key is refused", func(t *testing.T) {
		manual := netFixtureProvider(t, 4, "")
		other := netFixtureProvider(t, 5, "")
		manual.publish(netSignedManifest(t, other.key, 1, strings.Repeat("a", 64)))
		client := netFixtureClient(t)
		h := client.Handler()
		refused := netPost(h, "/network/providers", url.Values{"endpoint": {manual.server.URL}, "operator_key": {manual.operatorKey()}})
		if refused.Code != http.StatusOK || !strings.Contains(refused.Body.String(), "does not verify against this operator key") {
			t.Fatalf("mismatched key returned %d without the refusal reason", refused.Code)
		}
		if strings.Contains(netGet(h, "/network").Body.String(), "Added by you") {
			t.Error("refused provider was added")
		}
	})

	t.Run("plain http to a remote host is refused", func(t *testing.T) {
		client := netFixtureClient(t)
		refused := netPost(client.Handler(), "/network/providers", url.Values{"endpoint": {"http://p.example"}, "operator_key": {"did:key:z6MkExample"}})
		if refused.Code != http.StatusOK || !strings.Contains(refused.Body.String(), "Remote endpoints must use https://") {
			t.Fatalf("remote http returned %d without the refusal", refused.Code)
		}
	})

	t.Run("an unreachable manifest needs the explicit checkbox and is marked unverified", func(t *testing.T) {
		gone := netFixtureProvider(t, 6, "")
		endpoint := gone.server.URL
		key := gone.operatorKey()
		gone.server.Close()
		client := netFixtureClient(t)
		h := client.Handler()
		blocked := netPost(h, "/network/providers", url.Values{"endpoint": {endpoint}, "operator_key": {key}})
		if !strings.Contains(blocked.Body.String(), "could not be fetched") || strings.Contains(netGet(h, "/network").Body.String(), "Added by you") {
			t.Fatal("unreachable manifest was added without the explicit checkbox")
		}
		added := netPost(h, "/network/providers", url.Values{"endpoint": {endpoint}, "operator_key": {key}, "unverified": {"1"}})
		if added.Code != http.StatusSeeOther || !strings.Contains(netGet(h, "/network").Body.String(), "Manifest not verified") {
			t.Fatalf("explicit unverified add returned %d without the warning", added.Code)
		}
	})
}

func TestNetworkManifestPins(t *testing.T) {
	provider := netFixtureProvider(t, 7, "Default operator")
	client := netFixtureClient(t, provider)
	h := client.Handler()
	digestA := strings.Repeat("a", 64)
	digestB := strings.Repeat("b", 64)

	t.Run("a newer manifest pins and an older one is rejected", func(t *testing.T) {
		provider.publish(netSignedManifest(t, provider.key, 2, digestA))
		if body := netGet(h, "/network/provider?endpoint="+url.QueryEscape(provider.server.URL)).Body.String(); !strings.Contains(body, "Pinned version 2") {
			t.Fatal("version 2 was not pinned")
		}
		provider.publish(netSignedManifest(t, provider.key, 1, digestA))
		if body := netGet(h, "/network/provider?endpoint="+url.QueryEscape(provider.server.URL)).Body.String(); !strings.Contains(body, "Older version offered; rejected") {
			t.Fatal("version 1 after version 2 was not rejected")
		}
	})

	t.Run("two different bodies at one version mark equivocation until reset", func(t *testing.T) {
		provider.publish(netSignedManifest(t, provider.key, 2, digestB))
		body := netGet(h, "/network/provider?endpoint="+url.QueryEscape(provider.server.URL)).Body.String()
		if !strings.Contains(body, "Equivocation: two different manifests claim version 2. Review the operator key and policy.") || !strings.Contains(body, "Reset trust in this manifest") {
			t.Fatal("equivocation text or reset control missing")
		}
		reset := netPost(h, "/network/manifest/reset", url.Values{"endpoint": {provider.server.URL}, "confirm": {"1"}})
		if reset.Code != http.StatusOK || !strings.Contains(reset.Body.String(), "Trust in this manifest was reset") {
			t.Fatalf("reset returned %d", reset.Code)
		}
		if after := netGet(h, "/network/provider?endpoint="+url.QueryEscape(provider.server.URL)).Body.String(); !strings.Contains(after, "Pinned version 2") {
			t.Fatal("reset did not clear the equivocation mark")
		}
	})

	t.Run("the detail fragment has no page frame and the page fallback has one", func(t *testing.T) {
		endpoint := "/network/provider?endpoint=" + url.QueryEscape(provider.server.URL)
		fragment := netGet(h, endpoint, "Sec-Fetch-Dest", "empty").Body.String()
		page := netGet(h, endpoint).Body.String()
		if strings.Contains(fragment, "<h1>") || !strings.Contains(page, "<h1>Network</h1>") || !strings.Contains(fragment, "Pinned version 2") {
			t.Error("detail fragment or full-page fallback does not match its role")
		}
	})

	t.Run("a manifest signed by another key is not shown as manifest fields", func(t *testing.T) {
		other, err := keys.NewKeyFromSeed(bytes.Repeat([]byte{15}, 32))
		if err != nil {
			t.Fatal(err)
		}
		provider.publish(netSignedManifest(t, other, 3, digestA))
		body := netGet(h, "/network/provider?endpoint="+url.QueryEscape(provider.server.URL), "Sec-Fetch-Dest", "empty").Body.String()
		if !strings.Contains(body, "Manifest signature does not verify; not pinned") || strings.Contains(body, "Operator ID") {
			t.Error("a manifest that does not verify was shown with its fields")
		}
	})
}

func TestNetworkRetention(t *testing.T) {
	provider := netFixtureProvider(t, 8, "Default operator")
	client := netFixtureClient(t, provider)
	h := client.Handler()
	provider.publish(netSignedManifest(t, provider.key, 1, strings.Repeat("a", 64)))
	detailPath := "/network/provider?endpoint=" + url.QueryEscape(provider.server.URL)
	declaration, err := retention.Declare(provider.key, retention.MinSeconds, wire.PayoutRecipients{}, netTestNow)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("a verified declaration at the minimum is shown in days", func(t *testing.T) {
		raw, err := canon.Encode(wire.RetentionDeclarationsPage{Declarations: []wire.RetentionDeclaration{declaration}})
		if err != nil {
			t.Fatal(err)
		}
		provider.mu.Lock()
		provider.retention = raw
		provider.mu.Unlock()
		body := netGet(h, detailPath, "Sec-Fetch-Dest", "empty").Body.String()
		if !strings.Contains(body, "Keeps records at least 365 days (declaration "+declaration.DeclarationID) {
			t.Error("valid declaration not shown")
		}
	})

	t.Run("a declaration below one year is not shown as valid", func(t *testing.T) {
		below := declaration
		below.RetentionSeconds = retention.MinSeconds - 1
		raw, err := canon.Encode(wire.RetentionDeclarationsPage{Declarations: []wire.RetentionDeclaration{below}})
		if err != nil {
			t.Fatal(err)
		}
		provider.mu.Lock()
		provider.retention = raw
		provider.mu.Unlock()
		body := netGet(h, detailPath, "Sec-Fetch-Dest", "empty").Body.String()
		if strings.Contains(body, "Keeps records at least") || !strings.Contains(body, "No verified retention declaration.") {
			t.Error("declaration below the minimum was shown as valid")
		}
	})
}

func TestNetworkTrustRoot(t *testing.T) {
	client := netFixtureClient(t)
	body := netGet(client.Handler(), "/network").Body.String()

	t.Run("without a verified trust root the page says so and shows no schedule", func(t *testing.T) {
		if !strings.Contains(body, "The trust root has not been verified") || strings.Contains(body, "Fee schedules in force") {
			t.Error("unverified trust root rendered as verified")
		}
	})

	t.Run("a verified root shows the release keys, fee schedules and committee of the dev genesis", func(t *testing.T) {
		now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
		genesis := devnet.Genesis()
		root, err := sdk.BuildTrustRoot(sdk.Pin{ReleaseKeyIDs: genesis.ReleaseKeyIDs, Threshold: int(genesis.ReleaseThreshold)}, sdk.Publications{Genesis: genesis, FetchedAt: now, Endpoint: "https://foundation.example"})
		if err != nil {
			t.Fatal(err)
		}
		verified, err := NewClient(Config{DataDir: t.TempDir(), Dev: true, Now: func() time.Time { return now }})
		if err != nil {
			t.Fatal(err)
		}
		verified.app.trustMu.Lock()
		verified.app.root = root
		verified.app.trustStatus = TrustStatus{State: TrustReady, FetchedAt: now, Endpoint: "https://foundation.example"}
		verified.app.trustMu.Unlock()
		page := netGet(verified.Handler(), "/network").Body.String()
		if !strings.Contains(page, "Verified against the release key set pinned") {
			t.Error("verified trust root does not name the pinned release key set")
		}
		if !strings.Contains(page, "136 millisatoshi") || !strings.Contains(page, "48900000000 wei") {
			t.Error("fee schedule 1 amounts of the dev genesis missing")
		}
		if !strings.Contains(page, "decisions need more than two-thirds, so 4 of 5") {
			t.Error("committee quorum of the dev genesis missing")
		}
	})
}

func TestNetworkFragments(t *testing.T) {
	provider := netFixtureProvider(t, 12, "Default operator")
	manual := netFixtureProvider(t, 11, "")
	manual.publish(netSignedManifest(t, manual.key, 1, strings.Repeat("a", 64)))
	client := netFixtureClient(t, provider)
	h := client.Handler()

	t.Run("adding a provider answers with the providers fragment and no page frame", func(t *testing.T) {
		added := netPost(h, "/network/providers", url.Values{"endpoint": {manual.server.URL}, "operator_key": {manual.operatorKey()}}, "Sec-Fetch-Dest", "empty")
		body := added.Body.String()
		if added.Code != http.StatusOK || strings.Contains(body, "<h1>") || !strings.Contains(body, `id="network-providers"`) || !strings.Contains(body, "Its manifest is verified. It is not trusted until you turn trust on.") {
			t.Fatalf("fragment add returned %d with a page frame or without the providers section", added.Code)
		}
	})

	t.Run("changing trust answers with the providers fragment and no page frame", func(t *testing.T) {
		toggled := netPost(h, "/network/providers/trust", url.Values{"endpoint": {provider.server.URL}, "trusted": {"0"}}, "Sec-Fetch-Dest", "empty")
		body := toggled.Body.String()
		if toggled.Code != http.StatusOK || strings.Contains(body, "<h1>") || !strings.Contains(body, `id="network-providers"`) {
			t.Fatalf("fragment trust returned %d with a page frame or without the providers section", toggled.Code)
		}
	})

	t.Run("a failed add without script returns the full page with the reason", func(t *testing.T) {
		failed := netPost(h, "/network/providers", url.Values{"endpoint": {"https://p.example"}, "operator_key": {"not-a-key"}})
		body := failed.Body.String()
		if failed.Code != http.StatusOK || !strings.Contains(body, "<h1>Network</h1>") || !strings.Contains(body, "The operator key must be a did:key") {
			t.Fatalf("full-page add failure returned %d without the page or the reason", failed.Code)
		}
	})
}

func netUnverifiedProofs(t *testing.T, providerID string) string {
	t.Helper()
	claim := func(id string) wire.SettlementClaim {
		return wire.SettlementClaim{ClaimID: "fysc2:" + id, ProviderID: providerID, Network: "bitcoin-mainnet", Sequence: 1, PeriodStart: "2027-01-12T00:00:00Z", PeriodEnd: "2027-01-13T00:00:00Z", DeclarationID: "fyrd2:" + strings.Repeat("b", 64), ReceiptCount: 1, TotalBaseUnits: "136", CumulativeBaseUnits: "136", ReceiptsRoot: strings.Repeat("c", 64), ProviderSignature: strings.Repeat("A", 86)}
	}
	first, second := claim(strings.Repeat("a", 64)), claim(strings.Repeat("d", 64))
	raw, err := canon.Encode(wire.EquivocationsPage{Proofs: []wire.EquivocationProof{{Claims: []wire.SettlementClaim{first, second}, Successions: []wire.KeySuccession{}}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestNetworkMonitor(t *testing.T) {
	provider := netFixtureProvider(t, 13, "Default operator")
	var proofs atomic.Pointer[string]
	empty := `{"proofs":[]}`
	proofs.Store(&empty)
	monitor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/equivocations" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(*proofs.Load()))
	}))
	t.Cleanup(monitor.Close)
	monitorKey, err := keys.NewKeyFromSeed(bytes.Repeat([]byte{14}, 32))
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(Config{DataDir: t.TempDir(), Dev: true, Now: func() time.Time { return netTestNow }, Providers: []Endpoint{{URL: provider.server.URL, ServiceID: provider.operatorKey(), Label: provider.label}}, Monitors: []Endpoint{{URL: monitor.URL, ServiceID: keys.DIDOf(monitorKey), Label: "Test monitor"}}, DefaultList: ListDisclosure{Controller: "Example Foundation", Funding: "donations", Version: "1", Criteria: "signed manifests", Monitors: "one monitor", Conflicts: "none known"}})
	if err != nil {
		t.Fatal(err)
	}
	h := client.Handler()
	check := "/network/monitor?endpoint=" + url.QueryEscape(monitor.URL)

	t.Run("a monitor with no proofs says so for the provider", func(t *testing.T) {
		if body := netGet(h, check).Body.String(); !strings.Contains(body, "No equivocation proofs for Default operator.") {
			t.Fatal("empty proofs page not reported")
		}
	})

	t.Run("a proof that does not verify is reported as an unverified claim", func(t *testing.T) {
		unverified := netUnverifiedProofs(t, provider.operatorKey())
		proofs.Store(&unverified)
		if body := netGet(h, check).Body.String(); !strings.Contains(body, "Claim about Default operator is an unverified claim.") {
			t.Fatal("unverifiable proof not reported as an unverified claim")
		}
	})
}
