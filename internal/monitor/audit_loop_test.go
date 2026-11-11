package monitor

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/committee"
	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/distribution"
	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/governance"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/retention"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/wire"
)

type alProviderStub struct {
	service      *Service
	claim        wire.SettlementClaim
	leaves       []wire.SettlementLeaf
	declarations []wire.RetentionDeclaration
	withhold     bool
	postStatus   int
	posts        []wire.SettlementWitness
	pageAtPost   []bool
	delivered    int
}

func (stub *alProviderStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/retention":
		declarations := stub.declarations
		if declarations == nil {
			declarations = []wire.RetentionDeclaration{}
		}
		_ = json.NewEncoder(w).Encode(wire.RetentionDeclarationsPage{Declarations: declarations})
	case r.Method == http.MethodGet && r.URL.Path == "/v1/settlement-claims":
		claims := []wire.SettlementClaim{}
		if r.URL.Query().Get("network") == stub.claim.Network && stub.claim.Sequence > 0 && uint64(stub.claim.Sequence) > alTestAfter(r) {
			claims = append(claims, stub.claim)
		}
		_ = json.NewEncoder(w).Encode(wire.SettlementClaimsPage{Claims: claims})
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/leaves"):
		if stub.withhold {
			_ = json.NewEncoder(w).Encode(wire.SettlementLeavesPage{ClaimID: stub.claim.ClaimID, Leaves: []wire.SettlementLeaf{}})
			return
		}
		_ = json.NewEncoder(w).Encode(wire.SettlementLeavesPage{ClaimID: stub.claim.ClaimID, Leaves: stub.leaves})
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/successions/"):
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"status":"not_found"}`))
	case r.Method == http.MethodPost && r.URL.Path == "/v1/settlement-witnesses":
		var witness wire.SettlementWitness
		if err := json.NewDecoder(r.Body).Decode(&witness); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		stub.posts = append(stub.posts, witness)
		found := alWitnessOnPage(stub.service.Members()[0], witness)
		if witness.WitnessID == stub.service.Members()[1].DID() {
			found = alWitnessOnPage(stub.service.Members()[1], witness)
		}
		if witness.WitnessID == stub.service.Members()[2].DID() {
			found = alWitnessOnPage(stub.service.Members()[2], witness)
		}
		if witness.WitnessID == stub.service.Members()[3].DID() {
			found = alWitnessOnPage(stub.service.Members()[3], witness)
		}
		if witness.WitnessID == stub.service.Members()[4].DID() {
			found = alWitnessOnPage(stub.service.Members()[4], witness)
		}
		stub.pageAtPost = append(stub.pageAtPost, found)
		status := stub.postStatus
		if status == 0 {
			status = http.StatusCreated
		}
		if status == http.StatusServiceUnavailable {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"status":"unavailable","retry_after_seconds":30}`))
			return
		}
		stub.delivered++
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{}`))
	default:
		http.NotFound(w, r)
	}
}

func alWitnessOnPage(member *Member, witness wire.SettlementWitness) bool {
	var page wire.MonitorWitnessesPage
	_ = member.Do(func(core *committee.Monitor) error {
		page = core.Page(witness.ProviderID, witness.Network, 0, 10)
		return nil
	})
	return len(page.Witnesses) == 1 && page.Witnesses[0].WitnessID == witness.WitnessID && page.Witnesses[0].ClaimID == witness.ClaimID
}

func alTestAfter(r *http.Request) uint64 {
	var after uint64
	_, _ = fmt.Sscan(r.URL.Query().Get("after_sequence"), &after)
	return after
}

func alNewAuditFixture(t *testing.T, now *time.Time, funded bool, members []keys.PrivateKey) (*Service, *alProviderStub, *testChain, wire.RetentionDeclaration) {
	t.Helper()
	// The specification example uses another genesis; retain its one-receipt shape and sign a local leaf and claim for devnet verification.
	declaration, err := retention.Declare(devnet.ProviderKey(0), retention.MinSeconds, wire.PayoutRecipients{}, now.Add(-48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	start, end := now.Add(-2*time.Hour), now.Add(-time.Hour)
	leaves := []wire.SettlementLeaf{{ReceiptHash: strings.Repeat("a", 64), ScheduleID: devnet.Genesis().FeeSchedules[0].ScheduleID, AmountBaseUnits: "136", IssuedAt: wire.FormatTime(start.Add(time.Minute))}}
	root, err := settlement.TreeRoot(leaves)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := settlement.BuildClaim(devnet.ProviderKey(0), settlement.ClaimInput{Network: wire.BitcoinMainnet, PeriodStart: start, PeriodEnd: end, DeclarationID: declaration.DeclarationID, ReceiptCount: 1, Total: root.Sum, ReceiptsRoot: hex.EncodeToString(root.Hash[:])})
	if err != nil {
		t.Fatal(err)
	}
	stub := &alProviderStub{claim: claim, leaves: leaves, declarations: []wire.RetentionDeclaration{declaration}}
	provider := httptest.NewServer(stub)
	chain := &testChain{}
	if funded {
		commitment, err := settlement.DepositCommitment(claim.ProviderID, faux.BitcoinMainnet)
		if err != nil {
			t.Fatal(err)
		}
		chain.transfers = []faux.Transfer{{Network: faux.BitcoinMainnet, Recipient: devnet.Genesis().FoundationKeyCertificates[0].Recipients.BitcoinMainnet, AmountChainUnits: "1000000", Data: hex.EncodeToString(commitment[:]), Sequence: 1, BlockTime: wire.FormatTime(now.Add(-time.Hour)), Finalized: true, Success: true}}
	}
	service, err := New(Config{Genesis: devnet.Genesis(), Members: members, DataDir: t.TempDir(), Now: func() time.Time { return *now }, Peers: map[string]string{"provider0": provider.URL}, Faux: faux.Chain(chain)})
	if err != nil {
		provider.Close()
		t.Fatal(err)
	}
	stub.service = service
	t.Cleanup(func() { _ = service.Close(); provider.Close() })
	return service, stub, chain, declaration
}

func TestAuditLoopWitnessesBeforeDelivery(t *testing.T) {
	now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	service, stub, _, _ := alNewAuditFixture(t, &now, true, nil)
	if err := service.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(stub.posts) != 5 {
		t.Fatalf("POST count = %d; want 5", len(stub.posts))
	}
	t.Run("all members delivered persisted witness", func(t *testing.T) {
		if len(stub.pageAtPost) != 5 {
			t.Fatalf("page observations = %d; want 5", len(stub.pageAtPost))
		}
		if !stub.pageAtPost[0] || !stub.pageAtPost[1] || !stub.pageAtPost[2] || !stub.pageAtPost[3] || !stub.pageAtPost[4] {
			t.Fatalf("witness was not on member page before POST: %v", stub.pageAtPost)
		}
	})
}

func TestAuditLoopUnfundedReaudit(t *testing.T) {
	t.Run("funding appears after 24 hours", func(t *testing.T) {
		now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
		service, stub, chain, _ := alNewAuditFixture(t, &now, false, nil)
		if err := service.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		alAssertRefusals(t, service, stub.claim, "unfunded")
		if len(stub.posts) != 0 {
			t.Fatalf("unfunded claim delivered %d witnesses", len(stub.posts))
		}
		commitment, err := settlement.DepositCommitment(stub.claim.ProviderID, faux.BitcoinMainnet)
		if err != nil {
			t.Fatal(err)
		}
		chain.transfers = []faux.Transfer{{Network: faux.BitcoinMainnet, Recipient: devnet.Genesis().FoundationKeyCertificates[0].Recipients.BitcoinMainnet, AmountChainUnits: "1000000", Data: hex.EncodeToString(commitment[:]), Sequence: 1, BlockTime: wire.FormatTime(now.Add(-time.Hour)), Finalized: true, Success: true}}
		now = now.Add(24 * time.Hour)
		if err := service.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(stub.posts) != 5 {
			t.Fatalf("delivered witnesses after funding = %d; want 5", len(stub.posts))
		}
	})
	t.Run("no re-audit within 24 hours", func(t *testing.T) {
		now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
		service, stub, _, _ := alNewAuditFixture(t, &now, false, nil)
		if err := service.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		before := alRefusedAt(t, service.Members()[0], stub.claim)
		now = now.Add(time.Hour)
		if err := service.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		if after := alRefusedAt(t, service.Members()[0], stub.claim); after != before {
			t.Fatalf("refused_at changed one hour later: %s -> %s", before, after)
		}
	})
	t.Run("no re-audit after seven days", func(t *testing.T) {
		now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
		service, stub, _, _ := alNewAuditFixture(t, &now, false, nil)
		if err := service.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		before := alRefusedAt(t, service.Members()[0], stub.claim)
		now = now.Add(7*24*time.Hour + time.Second)
		if err := service.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		if after := alRefusedAt(t, service.Members()[0], stub.claim); after != before {
			t.Fatalf("refused_at changed after re-audit window: %s -> %s", before, after)
		}
	})
}

func TestAuditLoopWaitsForRetentionDeclaration(t *testing.T) {
	now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	service, stub, _, declaration := alNewAuditFixture(t, &now, true, nil)
	stub.declarations = nil
	if err := service.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	alAssertRefusals(t, service, stub.claim, "bad_period")
	stub.declarations = []wire.RetentionDeclaration{declaration}
	if err := service.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(stub.posts) != 5 {
		t.Fatalf("delivered witnesses after declaration appeared = %d; want 5", len(stub.posts))
	}
}

func TestAuditLoopRetriesDeliveryAndWithheldLeaves(t *testing.T) {
	t.Run("503 retries witness delivery", func(t *testing.T) {
		now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
		service, stub, _, _ := alNewAuditFixture(t, &now, true, nil)
		stub.postStatus = http.StatusServiceUnavailable
		if err := service.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(stub.posts) != 5 {
			t.Fatalf("first pass delivery attempts = %d; want 5", len(stub.posts))
		}
		if stub.delivered != 0 {
			t.Fatalf("successful deliveries on unavailable pass = %d; want 0", stub.delivered)
		}
		stub.posts = nil
		stub.pageAtPost = nil
		stub.postStatus = http.StatusCreated
		if err := service.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(stub.posts) != 5 {
			t.Fatalf("retry delivery attempts = %d; want 5", len(stub.posts))
		}
		if stub.delivered != 5 {
			t.Fatalf("successful deliveries after retry = %d; want 5", stub.delivered)
		}
	})
	t.Run("missing leaves do not decide", func(t *testing.T) {
		now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
		service, stub, _, _ := alNewAuditFixture(t, &now, true, nil)
		stub.withhold = true
		if err := service.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		alAssertUndecided(t, service.Members()[0], stub.claim)
		alAssertUndecided(t, service.Members()[1], stub.claim)
		alAssertUndecided(t, service.Members()[2], stub.claim)
		alAssertUndecided(t, service.Members()[3], stub.claim)
		alAssertUndecided(t, service.Members()[4], stub.claim)
	})
}

func TestAuditLoopCommitteeHandoverBoundaries(t *testing.T) {
	t.Run("departed member signs nothing after effective time", func(t *testing.T) {
		now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
		service, stub, _, _ := alNewAuditFixture(t, &now, true, nil)
		alAddCommitteeChange(t, service, false, now)
		if err := service.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		alAssertUndecided(t, alMemberByDID(t, service, devnet.Genesis().DistributionPolicy.Monitors[1]), stub.claim)
		if len(stub.posts) != 4 {
			t.Fatalf("POST count = %d; want 4 from the continuing members", len(stub.posts))
		}
	})
	t.Run("newcomer leaves earlier period to handover", func(t *testing.T) {
		now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
		members := []keys.PrivateKey{devnet.MonitorKey(1), devnet.MonitorKey(2), devnet.MonitorKey(3), devnet.MonitorKey(4), devnet.ProviderKey(40)}
		service, stub, _, _ := alNewAuditFixture(t, &now, true, members)
		alAddCommitteeChange(t, service, true, now.Add(-30*time.Minute))
		if err := service.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		alAssertUndecided(t, service.Members()[4], stub.claim)
	})
	t.Run("newcomer audits later period", func(t *testing.T) {
		now := time.Date(2027, 1, 1, 2, 0, 0, 0, time.UTC)
		members := []keys.PrivateKey{devnet.MonitorKey(1), devnet.MonitorKey(2), devnet.MonitorKey(3), devnet.MonitorKey(4), devnet.ProviderKey(40)}
		service, stub, _, _ := alNewAuditFixture(t, &now, true, members)
		service.config.Peers["monitor"] = peerServePages(t, service)
		alAddCommitteeChange(t, service, true, now.Add(-2*time.Hour))
		if err := service.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		var page wire.MonitorWitnessesPage
		if err := service.Members()[4].Do(func(core *committee.Monitor) error {
			page = core.Page(stub.claim.ProviderID, stub.claim.Network, 0, 10)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if len(page.Witnesses) != 1 || page.Witnesses[0].WitnessID != service.Members()[4].DID() {
			t.Fatalf("newcomer witnesses = %+v; want one witness from %s", page.Witnesses, service.Members()[4].DID())
		}
	})
}

func alMemberByDID(t *testing.T, service *Service, did string) *Member {
	t.Helper()
	for _, member := range service.Members() {
		if member.DID() == did {
			return member
		}
	}
	t.Fatalf("no hosted member %s", did)
	return nil
}

func alAssertRefusals(t *testing.T, service *Service, claim wire.SettlementClaim, reason string) {
	t.Helper()
	alAssertMemberRefusal(t, service.Members()[0], claim, reason)
	alAssertMemberRefusal(t, service.Members()[1], claim, reason)
	alAssertMemberRefusal(t, service.Members()[2], claim, reason)
	alAssertMemberRefusal(t, service.Members()[3], claim, reason)
	alAssertMemberRefusal(t, service.Members()[4], claim, reason)
}

func alAssertMemberRefusal(t *testing.T, member *Member, claim wire.SettlementClaim, reason string) {
	t.Helper()
	var page wire.MonitorWitnessesPage
	if err := member.Do(func(core *committee.Monitor) error {
		page = core.Page(claim.ProviderID, claim.Network, 0, 10)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(page.Refusals) != 1 || page.Refusals[0].Reason != reason {
		t.Fatalf("member %d refusals = %+v; want %s", member.Index(), page.Refusals, reason)
	}
}

func alRefusedAt(t *testing.T, member *Member, claim wire.SettlementClaim) string {
	t.Helper()
	var page wire.MonitorWitnessesPage
	if err := member.Do(func(core *committee.Monitor) error {
		page = core.Page(claim.ProviderID, claim.Network, 0, 10)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(page.Refusals) != 1 {
		t.Fatalf("refusals = %+v; want one", page.Refusals)
	}
	return page.Refusals[0].RefusedAt
}

func alAssertUndecided(t *testing.T, member *Member, claim wire.SettlementClaim) {
	t.Helper()
	var page wire.MonitorWitnessesPage
	if err := member.Do(func(core *committee.Monitor) error {
		page = core.Page(claim.ProviderID, claim.Network, 0, 10)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(page.Witnesses) != 0 || len(page.Refusals) != 0 {
		t.Fatalf("member %d decided with missing leaves: %+v", member.Index(), page)
	}
}

func alSignedPolicy(t *testing.T, monitors []string, effective time.Time) wire.DistributionPolicy {
	t.Helper()
	genesisPolicy := devnet.Genesis().DistributionPolicy
	policy := wire.DistributionPolicy{Revision: 2, VaultShareBP: genesisPolicy.VaultShareBP, MonitorShareBP: genesisPolicy.MonitorShareBP, MaxRetentionYears: genesisPolicy.MaxRetentionYears, MinReports: genesisPolicy.MinReports, Monitors: monitors, ApprovedAt: wire.FormatTime(effective.Add(-30 * 24 * time.Hour)), EffectiveAt: wire.FormatTime(effective)}
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
	return policy
}

func alAddCommitteeChange(t *testing.T, service *Service, newcomer bool, effective time.Time) {
	t.Helper()
	prior := devnet.Genesis().DistributionPolicy.Monitors
	monitors := []string{prior[0], prior[2], prior[3], prior[4]}
	if newcomer {
		monitors = append(monitors, keys.DIDOf(devnet.ProviderKey(40)))
	} else {
		monitors = append(monitors, keys.DIDOf(devnet.ProviderKey(41)))
	}
	sort.Strings(monitors)
	if err := service.View().AddPolicy(alSignedPolicy(t, monitors, effective)); err != nil {
		t.Fatal(err)
	}
}
