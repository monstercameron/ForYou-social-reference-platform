package monitor

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/distribution"
	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/governance"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/wire"
)

type testChain struct {
	transfers []faux.Transfer
	query     faux.Query
}

func (c *testChain) Submit(context.Context, faux.SubmitRequest) (faux.Transfer, error) {
	return faux.Transfer{}, errors.New("unused")
}
func (c *testChain) Transfer(context.Context, string, string) (faux.Transfer, error) {
	return faux.Transfer{}, errors.New("unused")
}
func (c *testChain) Transfers(_ context.Context, query faux.Query) ([]faux.Transfer, error) {
	c.query = query
	return append([]faux.Transfer(nil), c.transfers...), nil
}

func testService(t *testing.T, root string, chain faux.Chain) *Service {
	t.Helper()
	svc, err := New(Config{Genesis: devnet.Genesis(), DataDir: root, Now: func() time.Time { return time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC) }, Peers: map[string]string{"monitor": "http://127.0.0.1:8447", "foundation": "http://127.0.0.1:8444"}, Faux: chain})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestProviderConfiguration(t *testing.T) {
	t.Run("dev default maps provider peer to its identity", func(t *testing.T) {
		svc, err := New(Config{DataDir: t.TempDir(), Peers: map[string]string{"provider": "http://127.0.0.1:8445"}, Faux: &testChain{}})
		if err != nil {
			t.Fatal(err)
		}
		defer svc.Close()
		want := []Provider{{ID: keys.DIDOf(devnet.ProviderKey(0)), URL: "http://127.0.0.1:8445"}}
		if got := svc.ProviderList(); !reflect.DeepEqual(got, want) {
			t.Errorf("ProviderList() = %#v; want %#v", got, want)
		}
	})
	t.Run("explicit providers are sorted and looked up by identity", func(t *testing.T) {
		firstID := keys.DIDOf(devnet.ProviderKey(0))
		secondID := keys.DIDOf(devnet.ProviderKey(1))
		providers := map[string]string{secondID: "https://provider-b.example", firstID: "https://provider-a.example"}
		svc, err := New(Config{DataDir: t.TempDir(), Providers: providers, Faux: &testChain{}})
		if err != nil {
			t.Fatal(err)
		}
		defer svc.Close()
		providers[secondID] = "https://changed.example"
		want := []Provider{{ID: firstID, URL: "https://provider-a.example"}, {ID: secondID, URL: "https://provider-b.example"}}
		if got := svc.ProviderList(); !reflect.DeepEqual(got, want) {
			t.Errorf("ProviderList() = %#v; want %#v", got, want)
		}
		if got, ok := svc.ProviderURL(secondID); !ok || got != "https://provider-b.example" {
			t.Errorf("ProviderURL(%q) = %q, %t", secondID, got, ok)
		}
		if got, ok := svc.ProviderURL(keys.DIDOf(devnet.ProviderKey(2))); ok || got != "" {
			t.Errorf("ProviderURL(unknown) = %q, %t; want empty, false", got, ok)
		}
	})
	checkRejected := func(t *testing.T, providers map[string]string, peers map[string]string, wantError string) {
		t.Helper()
		_, err := New(Config{DataDir: t.TempDir(), Providers: providers, Peers: peers, Faux: &testChain{}})
		if err == nil || !strings.Contains(err.Error(), wantError) {
			t.Fatalf("New() error = %v; want error containing %q", err, wantError)
		}
	}
	t.Run("rejects an identity that is not did:key", func(t *testing.T) {
		checkRejected(t, map[string]string{"did:web:provider.example": "https://provider.example"}, nil, "identity")
	})
	t.Run("rejects a relative URL", func(t *testing.T) {
		checkRejected(t, map[string]string{keys.DIDOf(devnet.ProviderKey(0)): "/provider"}, nil, "URL")
	})
	t.Run("rejects a URL with a query", func(t *testing.T) {
		checkRejected(t, map[string]string{keys.DIDOf(devnet.ProviderKey(0)): "https://provider.example?token=x"}, nil, "query")
	})
	t.Run("rejects a URL with an empty query marker", func(t *testing.T) {
		checkRejected(t, map[string]string{keys.DIDOf(devnet.ProviderKey(0)): "https://provider.example?"}, nil, "query")
	})
	t.Run("rejects a URL with an empty fragment marker", func(t *testing.T) {
		checkRejected(t, map[string]string{keys.DIDOf(devnet.ProviderKey(0)): "https://provider.example#"}, nil, "fragment")
	})
	t.Run("rejects two identities sharing one URL", func(t *testing.T) {
		checkRejected(t, map[string]string{keys.DIDOf(devnet.ProviderKey(0)): "https://provider.example", keys.DIDOf(devnet.ProviderKey(1)): "https://provider.example"}, nil, "share")
	})
	t.Run("rejects an unmappable provider peer name", func(t *testing.T) {
		checkRejected(t, nil, map[string]string{"providerx": "http://127.0.0.1:8445"}, "providerx")
	})
	t.Run("rejects duplicate dev identities from provider peers", func(t *testing.T) {
		checkRejected(t, nil, map[string]string{"provider": "http://127.0.0.1:8445", "provider0": "http://127.0.0.1:8446"}, "duplicate dev identity")
	})
}

func TestHandlerRoutesMembersAndHealth(t *testing.T) {
	previous := routeGroups
	routeGroups = append(routeGroups, func(_ *Service, mux *http.ServeMux) {
		mux.HandleFunc("GET /identity", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, MemberFrom(r).DID()) })
	})
	defer func() { routeGroups = previous }()
	svc := testService(t, t.TempDir(), &testChain{})
	defer svc.Close()
	checkMember := func(t *testing.T, path string, index int) {
		t.Helper()
		response := httptest.NewRecorder()
		svc.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK || response.Body.String() != keys.DIDOf(devnet.MonitorKey(index)) {
			t.Errorf("%s response = %d %q", path, response.Code, response.Body.String())
		}
	}
	t.Run("member zero", func(t *testing.T) { checkMember(t, "/monitors/0/identity", 0) })
	t.Run("member four", func(t *testing.T) { checkMember(t, "/monitors/4/identity", 4) })
	checkMissing := func(t *testing.T, path string) {
		t.Helper()
		response := httptest.NewRecorder()
		svc.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		var body struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != http.StatusNotFound || body.Status != "not_found" {
			t.Errorf("%s response = %d %q, decode error %v", path, response.Code, response.Body.String(), err)
		}
	}
	t.Run("unknown member", func(t *testing.T) { checkMissing(t, "/monitors/5/identity") })
	t.Run("leading zero", func(t *testing.T) { checkMissing(t, "/monitors/00/identity") })
	t.Run("not a number", func(t *testing.T) { checkMissing(t, "/monitors/x/identity") })
	t.Run("health", func(t *testing.T) {
		response := httptest.NewRecorder()
		svc.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		if response.Code != http.StatusOK || response.Body.String() != `{"status":"ok"}` {
			t.Errorf("health = %d %q", response.Code, response.Body.String())
		}
	})
}

func TestRunOnceOrdersStepsAndStopsOnError(t *testing.T) {
	previous := loopSteps
	defer func() { loopSteps = previous }()
	var order []string
	failure := errors.New("stop")
	loopSteps = []loopStep{
		{order: 10, name: "peer", run: func(*Service, context.Context) error { order = append(order, "peer"); return nil }},
		{order: 5, name: "trust", run: func(*Service, context.Context) error { order = append(order, "trust"); return nil }},
		{order: 10, name: "alpha", run: func(*Service, context.Context) error { order = append(order, "alpha"); return failure }},
		{order: 20, name: "audit", run: func(*Service, context.Context) error { order = append(order, "audit"); return nil }},
	}
	svc := testService(t, t.TempDir(), &testChain{})
	defer svc.Close()
	if err := svc.RunOnce(context.Background()); !errors.Is(err, failure) {
		t.Fatalf("RunOnce error = %v; want stop", err)
	}
	if want := []string{"trust", "alpha"}; !reflect.DeepEqual(order, want) {
		t.Errorf("step order = %v; want %v", order, want)
	}
}

func TestViewGenesisCommittee(t *testing.T) {
	svc := testService(t, t.TempDir(), &testChain{})
	defer svc.Close()
	view := svc.View()
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	want := []string{
		keys.DIDOf(devnet.MonitorKey(1)),
		keys.DIDOf(devnet.MonitorKey(0)),
		keys.DIDOf(devnet.MonitorKey(3)),
		keys.DIDOf(devnet.MonitorKey(2)),
		keys.DIDOf(devnet.MonitorKey(4)),
	}
	if got := view.CommitteeAt(at); !reflect.DeepEqual(got, want) {
		t.Errorf("CommitteeAt = %v; want %v", got, want)
	}
}

func TestViewCommitteeReplacementMembershipWindow(t *testing.T) {
	svc := testService(t, t.TempDir(), &testChain{})
	defer svc.Close()
	view := svc.View()
	genesisPolicy := devnet.Genesis().DistributionPolicy
	departed := genesisPolicy.Monitors[0]
	newcomer := keys.DIDOf(devnet.ProviderKey(40))
	otherNewcomer := keys.DIDOf(devnet.ProviderKey(41))
	next := append([]string(nil), genesisPolicy.Monitors[2:]...)
	next = append(next, newcomer, otherNewcomer)
	sort.Strings(next)
	effective := time.Date(2027, 4, 1, 0, 0, 0, 0, time.UTC)
	policy := wire.DistributionPolicy{Revision: 2, VaultShareBP: genesisPolicy.VaultShareBP, MonitorShareBP: genesisPolicy.MonitorShareBP, MaxRetentionYears: genesisPolicy.MaxRetentionYears, MinReports: genesisPolicy.MinReports, Monitors: next, ApprovedAt: wire.FormatTime(effective.Add(-30 * 24 * time.Hour)), EffectiveAt: wire.FormatTime(effective)}
	var err error
	policy.PolicyID, err = distribution.PolicyID(policy)
	if err != nil {
		t.Fatal(err)
	}
	unsignedBytes, err := canon.EncodeWithout(policy, "release_signatures")
	if err != nil {
		t.Fatal(err)
	}
	unsigned, err := canon.Parse(unsignedBytes, canon.Limits{MaxDepth: 32})
	if err != nil {
		t.Fatal(err)
	}
	policy.ReleaseSignatures, err = governance.SignRelease("distribution-policy", unsigned, devnet.ReleaseKeys())
	if err != nil {
		t.Fatal(err)
	}
	if err := view.AddPolicy(policy); err != nil {
		t.Fatal(err)
	}
	if view.IsMember(departed, effective) {
		t.Errorf("departed member %q remains in committee at effective_at", departed)
	}
	if !view.IsMember(newcomer, effective) {
		t.Errorf("new member %q missing at effective_at", newcomer)
	}
	if joined, ok := view.JoinedAt(newcomer, effective); !ok || !joined.Equal(effective) {
		t.Errorf("JoinedAt = %v, %t; want %v", joined, ok, effective)
	}
	if _, ok := view.JoinedAt(genesisPolicy.Monitors[1], effective); ok {
		t.Error("JoinedAt reported an effective time for a genesis member")
	}
	if prior, at, ok := view.PreviousCommittee(effective.Add(29 * 24 * time.Hour)); !ok || !at.Equal(effective) || !reflect.DeepEqual(prior, genesisPolicy.Monitors) {
		t.Errorf("PreviousCommittee before expiry = %v, %v, %t", prior, at, ok)
	}
	if _, _, ok := view.PreviousCommittee(effective.Add(30 * 24 * time.Hour)); ok {
		t.Error("PreviousCommittee remained available at 30 days")
	}
	before := view.CommitteeAt(effective)
	invalid := policy
	invalid.Revision = 3
	if err := view.AddPolicy(invalid); err == nil {
		t.Fatal("AddPolicy accepted a signature-invalid policy")
	}
	if got := view.CommitteeAt(effective); !reflect.DeepEqual(got, before) {
		t.Errorf("rejected policy changed committee: got %v, want %v", got, before)
	}
}

func TestDepositedCountsOnlyQualifiedTransfers(t *testing.T) {
	key := devnet.ProviderKey(0)
	commitment, err := settlement.DepositCommitment(keys.DIDOf(key), faux.BitcoinMainnet)
	if err != nil {
		t.Fatal(err)
	}
	data := hex.EncodeToString(commitment[:])
	otherCommitment, err := settlement.DepositCommitment(keys.DIDOf(devnet.ProviderKey(1)), faux.BitcoinMainnet)
	if err != nil {
		t.Fatal(err)
	}
	otherData := hex.EncodeToString(otherCommitment[:])
	recipient := devnet.Genesis().FoundationKeyCertificates[0].Recipients.BitcoinMainnet
	chain := &testChain{transfers: []faux.Transfer{
		{Network: faux.BitcoinMainnet, Recipient: recipient, AmountChainUnits: "10000", Data: data, Sequence: 1, BlockTime: "2026-10-01T00:00:00Z", Finalized: true, Success: true},
		{Network: faux.BitcoinMainnet, Recipient: recipient, AmountChainUnits: "9999", Data: data, Sequence: 2, BlockTime: "2026-10-01T00:00:00Z", Finalized: true, Success: true},
		{Network: faux.BitcoinMainnet, Recipient: recipient, AmountChainUnits: "20000", Data: otherData, Sequence: 3, BlockTime: "2026-10-01T00:00:00Z", Finalized: true, Success: true},
	}}
	source := &depositSource{chain: chain, view: &View{trust: mustTrust(t)}}
	got, err := source.Deposited(keys.DIDOf(key), faux.BitcoinMainnet)
	if err != nil || got.Cmp(big.NewInt(10000000)) != 0 {
		t.Errorf("Deposited = %v, %v; want 10000000", got, err)
	}
	if chain.query.Data != data {
		t.Errorf("transfer query data = %q; want %q", chain.query.Data, data)
	}
}

func TestDefaultMemberDataDirectoriesAreScoped(t *testing.T) {
	root := t.TempDir()
	svc := testService(t, root, &testChain{})
	defer svc.Close()
	_, err := os.Stat(filepath.Join(root, "0", "journal.jsonl"))
	if svc.Members()[0].DB() == svc.Members()[1].DB() || err != nil {
		t.Error("member journals are not separate index directories")
	}
}

func mustTrust(t *testing.T) *governance.Trust {
	t.Helper()
	trust, err := devnet.Trust(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return trust
}

func TestClockIsWholeSecondsForTrust(t *testing.T) {
	fractional := time.Date(2026, 11, 11, 23, 4, 21, 123456789, time.FixedZone("local", -5*3600))
	ledger, err := faux.Open(faux.Config{Now: func() time.Time { return fractional }})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := New(Config{Genesis: devnet.Genesis(), DataDir: t.TempDir(), Now: func() time.Time { return fractional }, Faux: ledger})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	if got, want := svc.Now(), time.Date(2026, 11, 12, 4, 4, 21, 0, time.UTC); !got.Equal(want) || got.Location() != time.UTC {
		t.Errorf("Now() = %v; want %v in UTC", got, want)
	}
	if _, ok := svc.View().Trust().CurrentFoundationID(svc.Now()); !ok {
		t.Error("with a wall clock that has fractions of a second, the genesis Foundation key must still be in force")
	}
}
