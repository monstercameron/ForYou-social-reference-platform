package monitor

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"foryou.local/foryou/internal/apierr"
	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/committee"
	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/distribution"
	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/fee"
	"foryou.local/foryou/internal/governance"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/retention"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/wire"
)

var (
	peerTestBase     = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	peerTestChangeAt = time.Date(2027, 1, 20, 0, 0, 0, 0, time.UTC)
)

// peerTestWorld hosts committee members whose witness pages are served over HTTP, and one provider stub.
type peerTestWorld struct {
	service     *Service
	providerKey keys.PrivateKey
	declaration wire.RetentionDeclaration

	mu        sync.Mutex
	now       time.Time
	claims    []wire.SettlementClaim
	leaves    map[string][]wire.SettlementLeaf
	down      map[int]bool
	extra     map[int]wire.MonitorWitnessesPage
	delivered []wire.SettlementWitness
	withheld  map[string]bool
}

func TestPeerSyncCatchUp(t *testing.T) {
	t.Run("a member that missed a settled claim adopts it and delivers its witness", func(t *testing.T) {
		w := peerNewWorld(t, peerDevKeys(), peerTestBase.Add(72*time.Hour))
		first, firstLeaves := w.claim(t, nil, "first", 0)
		second, secondLeaves := w.claim(t, &first, "second", 20)
		w.serve(first, firstLeaves)
		w.serve(second, secondLeaves)
		w.audit(t, first, firstLeaves, 0, 1, 2, 3, 4)
		w.audit(t, second, secondLeaves, 0, 1, 2, 3)
		w.withheld[second.ClaimID] = true

		w.runOnce(t)

		if got := w.witnessAt(t, 4, 2); got != second.ClaimID {
			t.Errorf("member 4 witness at sequence 2 = %q; want %s", got, second.ClaimID)
		}
		if got := w.deliveredFor(keys.DIDOf(devnet.MonitorKey(4)), second.ClaimID); got == 0 {
			t.Errorf("provider received no witness from member 4 for %s; want its adopted witness", second.ClaimID)
		}
	})
	t.Run("a member that witnessed a rival follows the settled claim and keeps its own witness", func(t *testing.T) {
		w := peerNewWorld(t, peerDevKeys(), peerTestBase.Add(72*time.Hour))
		first, firstLeaves := w.claim(t, nil, "first", 0)
		forkA, forkALeaves := w.claim(t, &first, "fork a", 20)
		forkB, forkBLeaves := w.claim(t, &first, "fork b", 20)
		child, childLeaves := w.claim(t, &forkA, "child of a", 40)
		w.serve(first, firstLeaves)
		w.serve(forkA, forkALeaves)
		w.audit(t, first, firstLeaves, 0, 1, 2, 3, 4)
		w.audit(t, forkA, forkALeaves, 0, 1, 2, 3)
		w.audit(t, forkB, forkBLeaves, 4)

		w.runOnce(t)

		if got := w.witnessAt(t, 4, 2); got != forkB.ClaimID {
			t.Errorf("member 4 witness at sequence 2 = %q; want its own witness for %s", got, forkB.ClaimID)
		}
		var outcome committee.Outcome
		if err := w.service.Members()[4].Do(func(m *committee.Monitor) error {
			var err error
			outcome, err = m.Audit(child, childLeaves, w.service.Now(), w.service.Now())
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if outcome.Witness == nil {
			t.Errorf("member 4 audit of a's child = %+v; want a witness", outcome)
		}
	})
	t.Run("a settled claim is adopted with the unsettled ancestor where the member's chain differs", func(t *testing.T) {
		w := peerNewWorld(t, peerDevKeys(), peerTestBase.Add(72*time.Hour))
		a1, a1Leaves := w.claim(t, nil, "a1", 0)
		b1, b1Leaves := w.claim(t, nil, "b1", 0)
		a2, a2Leaves := w.claim(t, &a1, "a2", 20)
		w.serve(a1, a1Leaves)
		w.serve(a2, a2Leaves)
		w.audit(t, a1, a1Leaves, 0, 1, 2)
		w.audit(t, a2, a2Leaves, 0, 1, 2)
		w.audit(t, b1, b1Leaves, 4)
		w.publish(3, peerTestWitness(t, 3, a2), a2)

		w.runOnce(t)

		if got := w.witnessAt(t, 4, 2); got != a2.ClaimID {
			t.Errorf("member 4 witness at sequence 2 = %q; want %s", got, a2.ClaimID)
		}
		if got := w.witnessAt(t, 4, 1); got != b1.ClaimID {
			t.Errorf("member 4 witness at sequence 1 = %q; want its own witness for %s", got, b1.ClaimID)
		}
		var proofs []wire.EquivocationProof
		_ = w.service.Members()[4].Do(func(m *committee.Monitor) error {
			proofs = m.EquivocationProofs(a1.ProviderID)
			return nil
		})
		if len(proofs) != 1 || peerClaimIDs(proofs[0].Claims) != peerClaimIDs([]wire.SettlementClaim{a1, b1}) {
			t.Errorf("member 4 equivocation proofs = %+v; want one for a1 and b1 at sequence 1", proofs)
		}
	})
	t.Run("an unreachable peer does not fail the pass and its missing witness is not counted", func(t *testing.T) {
		w := peerNewWorld(t, peerDevKeys(), peerTestBase.Add(72*time.Hour))
		first, firstLeaves := w.claim(t, nil, "first", 0)
		w.serve(first, firstLeaves)
		w.audit(t, first, firstLeaves, 0, 1, 2, 3)
		w.down[0] = true

		w.runOnce(t)

		if got := w.witnessAt(t, 4, 1); got != "" {
			t.Errorf("member 4 witness at sequence 1 = %q; want none with three witnesses visible", got)
		}
	})
	t.Run("a witness with a bad signature on a peer page is not counted", func(t *testing.T) {
		w := peerNewWorld(t, peerDevKeys(), peerTestBase.Add(72*time.Hour))
		first, firstLeaves := w.claim(t, nil, "first", 0)
		w.serve(first, firstLeaves)
		w.audit(t, first, firstLeaves, 0, 1, 2)
		forged := peerTestWitness(t, 3, first)
		forged.CreditBaseUnits = "999999"
		w.publish(3, forged, first)

		w.runOnce(t)

		if got := w.witnessAt(t, 4, 1); got != "" {
			t.Errorf("member 4 witness at sequence 1 = %q; want none with a forged fourth witness", got)
		}
	})
}

func TestPeerSyncHandover(t *testing.T) {
	beforeChange := peerTestChangeAt.Sub(peerTestBase).Hours() - 30
	t.Run("newcomers sign nothing before effective_at plus 48 hours, then adopt the outgoing quorum's claim", func(t *testing.T) {
		w := peerNewWorld(t, peerHandoverKeys(), peerTestChangeAt.Add(-5*time.Hour))
		claim, leaves := w.claim(t, nil, "before the change", beforeChange)
		w.serve(claim, leaves)
		w.audit(t, claim, leaves, 0, 1, 2, 3, 4)
		w.addHandoverPolicy(t)

		w.at(peerTestChangeAt.Add(-time.Hour))
		w.runOnce(t)
		w.at(peerTestChangeAt.Add(47 * time.Hour))
		w.runOnce(t)
		if page := w.page(t, 5); len(page.Witnesses)+len(page.Holds) != 0 {
			t.Errorf("newcomer page before effective_at + 48h = %+v; want nothing signed", page)
		}

		w.at(peerTestChangeAt.Add(48 * time.Hour))
		w.runOnce(t)

		if got := w.witnessAt(t, 5, 1); got != claim.ClaimID {
			t.Errorf("newcomer 5 witness at sequence 1 = %q; want %s", got, claim.ClaimID)
		}
		if got := w.witnessAt(t, 6, 1); got != claim.ClaimID {
			t.Errorf("newcomer 6 witness at sequence 1 = %q; want %s", got, claim.ClaimID)
		}
		if got := w.deliveredFor(keys.DIDOf(devnet.ProviderKey(40)), claim.ClaimID); got == 0 {
			t.Errorf("provider received no witness from newcomer 5 for %s; want its adopted witness", claim.ClaimID)
		}
	})
	t.Run("split outgoing witnesses leave the newcomers holding both claims", func(t *testing.T) {
		w := peerNewWorld(t, peerHandoverKeys(), peerTestChangeAt.Add(-5*time.Hour))
		x, xLeaves := w.claim(t, nil, "x", beforeChange)
		y, yLeaves := w.claim(t, nil, "y", beforeChange)
		w.serve(x, xLeaves)
		w.audit(t, x, xLeaves, 0, 1)
		w.audit(t, y, yLeaves, 2, 3)
		w.addHandoverPolicy(t)
		w.at(peerTestChangeAt.Add(48 * time.Hour))

		w.runOnce(t)

		page := w.page(t, 5)
		want := peerClaimIDs([]wire.SettlementClaim{x, y})
		if len(page.Witnesses) != 0 || len(page.Holds) != 1 || strings.Join(page.Holds[0].ClaimIDs, ",") != want {
			t.Errorf("newcomer 5 page = %d witnesses, holds %+v; want one hold for %s", len(page.Witnesses), page.Holds, want)
		}
	})
	t.Run("witnesses departed monitors signed after the change do not make a newcomer adopt", func(t *testing.T) {
		w := peerNewWorld(t, peerHandoverKeys(), peerTestChangeAt.Add(-5*time.Hour))
		claim, leaves := w.claim(t, nil, "shown to departed monitors", beforeChange)
		w.withheld[claim.ClaimID] = true
		w.serve(claim, leaves)
		w.audit(t, claim, leaves, w.continuing(t)[0])
		w.addHandoverPolicy(t)
		w.at(peerTestChangeAt.Add(time.Hour))
		w.audit(t, claim, leaves, w.continuing(t)[1])
		w.audit(t, claim, leaves, w.departed(t)...)
		w.at(peerTestChangeAt.Add(48 * time.Hour))

		w.runOnce(t)

		page := w.page(t, 5)
		if len(page.Witnesses) != 0 || len(page.Holds) != 1 {
			t.Errorf("newcomer 5 page = %d witnesses, %d holds; want a hold and no witness", len(page.Witnesses), len(page.Holds))
		}
	})
	t.Run("the audit step leaves a newcomer's chain alone until its handover has read every continuing page", func(t *testing.T) {
		w := peerNewWorld(t, peerHandoverKeys(), peerTestChangeAt.Add(-5*time.Hour))
		settled, settledLeaves := w.claim(t, nil, "settled before the change", beforeChange)
		rival, rivalLeaves := w.claim(t, nil, "rival after the change", peerTestChangeAt.Sub(peerTestBase).Hours()+1)
		w.serve(rival, rivalLeaves)
		w.audit(t, settled, settledLeaves, 0, 1, 2, 3)
		w.register(t, 5)
		w.addHandoverPolicy(t)
		w.down[w.continuing(t)[2]] = true
		w.at(peerTestChangeAt.Add(48 * time.Hour))

		w.runOnce(t)

		if page := w.page(t, 5); len(page.Witnesses)+len(page.Refusals) != 0 {
			t.Errorf("newcomer 5 page with a continuing page unread = %+v; want the chain left alone", page)
		}
		w.down[w.continuing(t)[2]] = false
		w.runOnce(t)
		if got := w.witnessAt(t, 5, 1); got != settled.ClaimID {
			t.Errorf("newcomer 5 witness at sequence 1 = %q; want the outgoing quorum's %s", got, settled.ClaimID)
		}
	})
}

func peerDevKeys() []keys.PrivateKey {
	return []keys.PrivateKey{devnet.MonitorKey(0), devnet.MonitorKey(1), devnet.MonitorKey(2), devnet.MonitorKey(3), devnet.MonitorKey(4)}
}

// peerHandoverKeys hosts the five genesis monitors and, at indexes 5 and 6, the two newcomers.
func peerHandoverKeys() []keys.PrivateKey {
	return append(peerDevKeys(), devnet.ProviderKey(40), devnet.ProviderKey(41))
}

func peerNewWorld(t *testing.T, members []keys.PrivateKey, now time.Time) *peerTestWorld {
	t.Helper()
	w := &peerTestWorld{providerKey: devnet.ProviderKey(0), now: now, leaves: make(map[string][]wire.SettlementLeaf), withheld: make(map[string]bool), down: make(map[int]bool), extra: make(map[int]wire.MonitorWitnessesPage)}
	payout := wire.PayoutRecipients{BitcoinMainnet: devnet.Recipient(devnet.ProviderLabelPrefix+"0", faux.BitcoinMainnet)}
	var err error
	w.declaration, err = retention.Declare(w.providerKey, retention.MinSeconds, payout, peerTestBase.Add(-48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	provider := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		defer w.mu.Unlock()
		rw.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		switch {
		case path == "/v1/settlement-claims":
			after, _ := strconv.ParseInt(r.URL.Query().Get("after_sequence"), 10, 64)
			page := wire.SettlementClaimsPage{Claims: []wire.SettlementClaim{}}
			for _, claim := range w.claims {
				if claim.Network == r.URL.Query().Get("network") && claim.Sequence > after {
					page.Claims = append(page.Claims, claim)
				}
			}
			_ = json.NewEncoder(rw).Encode(page)
		case strings.HasSuffix(path, "/leaves"):
			id := strings.TrimSuffix(strings.TrimPrefix(path, "/v1/settlement-claims/"), "/leaves")
			leaves, ok := w.leaves[id]
			if !ok || w.withheld[id] {
				apierr.Write(rw, apierr.PublicReadError{Status: apierr.NotFound})
				return
			}
			_ = json.NewEncoder(rw).Encode(wire.SettlementLeavesPage{ClaimID: id, Offset: 0, Leaves: leaves})
		case strings.HasSuffix(path, "/witnesses"):
			_ = json.NewEncoder(rw).Encode(wire.SettlementWitnessesPage{Witnesses: []wire.SettlementWitness{}})
		case r.Method == http.MethodPost && path == "/v1/settlement-witnesses":
			body, _ := io.ReadAll(r.Body)
			var witness wire.SettlementWitness
			if err := wire.Decode(body, "SettlementWitness", &witness); err != nil {
				t.Errorf("decode delivered witness: %v", err)
			}
			w.delivered = append(w.delivered, witness)
			rw.WriteHeader(http.StatusCreated)
		default:
			apierr.Write(rw, apierr.PublicReadError{Status: apierr.NotFound})
		}
	}))
	t.Cleanup(provider.Close)
	monitors := httptest.NewServer(http.HandlerFunc(w.servePage))
	t.Cleanup(monitors.Close)
	commitment, err := settlement.DepositCommitment(keys.DIDOf(w.providerKey), faux.BitcoinMainnet)
	if err != nil {
		t.Fatal(err)
	}
	recipient := devnet.Genesis().FoundationKeyCertificates[0].Recipients.BitcoinMainnet
	chain := &testChain{transfers: []faux.Transfer{{Network: faux.BitcoinMainnet, Recipient: recipient, AmountChainUnits: "100000000", Data: hex.EncodeToString(commitment[:]), Sequence: 1, BlockTime: "2026-12-30T00:00:00Z", Finalized: true, Success: true}}}
	w.service, err = New(Config{
		Genesis: devnet.Genesis(), Members: members, DataDir: t.TempDir(), Faux: chain, Now: w.clock,
		Peers:     map[string]string{"monitor": monitors.URL},
		Providers: map[string]string{keys.DIDOf(w.providerKey): provider.URL},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.service.Close() })
	return w
}

// servePage answers GET /monitors/{n}/v1/witnesses from member n's state plus anything published for it.
func (w *peerTestWorld) servePage(rw http.ResponseWriter, r *http.Request) {
	index, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/monitors/"), "/")
	n, err := strconv.Atoi(index)
	w.mu.Lock()
	down, extra := w.down[n], w.extra[n]
	w.mu.Unlock()
	if err != nil || down || n >= len(w.service.Members()) || r.URL.Path != "/monitors/"+index+"/v1/witnesses" {
		apierr.Write(rw, apierr.PublicReadError{Status: apierr.NotFound})
		return
	}
	query := r.URL.Query()
	after, _ := strconv.ParseInt(query.Get("after_sequence"), 10, 64)
	var page wire.MonitorWitnessesPage
	_ = w.service.Members()[n].Do(func(m *committee.Monitor) error {
		page = m.Page(query.Get("provider_id"), query.Get("network"), after, 100)
		return nil
	})
	for _, witness := range extra.Witnesses {
		if witness.Sequence > after && witness.Network == query.Get("network") {
			page.Witnesses = append(page.Witnesses, witness)
			page.Claims = append(page.Claims, extra.Claims...)
		}
	}
	rw.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(rw).Encode(page)
}

// peerServePages serves GET /monitors/{n}/v1/witnesses from the service's hosted members, as the monitor
// routes will, and returns the base URL to configure as the "monitor" peer.
func peerServePages(t *testing.T, service *Service) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		index, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/monitors/"), "/")
		n, err := strconv.Atoi(index)
		if err != nil || n < 0 || n >= len(service.Members()) || rest != "v1/witnesses" {
			apierr.Write(rw, apierr.PublicReadError{Status: apierr.NotFound})
			return
		}
		query := r.URL.Query()
		after, _ := strconv.ParseInt(query.Get("after_sequence"), 10, 64)
		var page wire.MonitorWitnessesPage
		_ = service.Members()[n].View(func(m *committee.Monitor) error {
			page = m.Page(query.Get("provider_id"), query.Get("network"), after, 100)
			return nil
		})
		rw.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(rw).Encode(page)
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func (w *peerTestWorld) clock() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.now
}

// claim builds a one-receipt claim whose 20-hour period starts the given hours after peerTestBase.
func (w *peerTestWorld) claim(t *testing.T, previous *wire.SettlementClaim, tag string, startHours float64) (wire.SettlementClaim, []wire.SettlementLeaf) {
	t.Helper()
	start := peerTestBase.Add(time.Duration(startHours * float64(time.Hour)))
	_, receipt := vaultTestMessage(t, w.providerKey, start.Add(time.Hour))
	receipt.ReceiptID = keys.EncodeB64URL([]byte(strings.Repeat(tag[:1], 16) + strings.Repeat(tag[len(tag)-1:], 16)))
	receipt, err := fee.SignReceipt(receipt, w.providerKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := settlement.LeafOf(receipt)
	if err != nil {
		t.Fatal(err)
	}
	root, err := settlement.TreeRoot([]wire.SettlementLeaf{leaf})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := settlement.BuildClaim(w.providerKey, settlement.ClaimInput{
		Network: faux.BitcoinMainnet, Previous: previous, PeriodStart: start, PeriodEnd: start.Add(20 * time.Hour),
		DeclarationID: w.declaration.DeclarationID, ReceiptCount: 1, Total: root.Sum, ReceiptsRoot: hex.EncodeToString(root.Hash[:]),
	})
	if err != nil {
		t.Fatal(err)
	}
	return claim, []wire.SettlementLeaf{leaf}
}

// serve publishes a claim and its leaves on the provider stub.
func (w *peerTestWorld) serve(claim wire.SettlementClaim, leaves []wire.SettlementLeaf) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.claims = append(w.claims, claim)
	w.leaves[claim.ClaimID] = leaves
}

// publish adds a witness and its claim to member n's served page without changing the member's state.
func (w *peerTestWorld) publish(n int, witness wire.SettlementWitness, claim wire.SettlementClaim) {
	w.mu.Lock()
	defer w.mu.Unlock()
	page := w.extra[n]
	page.Witnesses = append(page.Witnesses, witness)
	page.Claims = append(page.Claims, claim)
	w.extra[n] = page
}

// register gives the members the provider's retention declaration, which the provider stub does not serve, so that
// only members a test names can audit a claim themselves.
func (w *peerTestWorld) register(t *testing.T, members ...int) {
	t.Helper()
	for _, n := range members {
		if err := w.service.Members()[n].Do(func(m *committee.Monitor) error { return m.RegisterDeclaration(w.declaration) }); err != nil {
			t.Fatal(err)
		}
	}
}

func (w *peerTestWorld) audit(t *testing.T, claim wire.SettlementClaim, leaves []wire.SettlementLeaf, members ...int) {
	t.Helper()
	now := w.clock()
	w.register(t, members...)
	for _, n := range members {
		var outcome committee.Outcome
		if err := w.service.Members()[n].Do(func(m *committee.Monitor) error {
			var err error
			outcome, err = m.Audit(claim, leaves, now, now)
			return err
		}); err != nil || outcome.Witness == nil {
			t.Fatalf("member %d audit of sequence %d = %+v, %v; want a witness", n, claim.Sequence, outcome, err)
		}
	}
}

func (w *peerTestWorld) at(now time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.now = now
}

func (w *peerTestWorld) runOnce(t *testing.T) {
	t.Helper()
	if err := w.service.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
}

func (w *peerTestWorld) page(t *testing.T, n int) wire.MonitorWitnessesPage {
	t.Helper()
	var page wire.MonitorWitnessesPage
	if err := w.service.Members()[n].Do(func(m *committee.Monitor) error {
		page = m.Page(keys.DIDOf(w.providerKey), faux.BitcoinMainnet, 0, 100)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return page
}

func (w *peerTestWorld) witnessAt(t *testing.T, n int, sequence int64) string {
	t.Helper()
	for _, witness := range w.page(t, n).Witnesses {
		if witness.Sequence == sequence {
			return witness.ClaimID
		}
	}
	return ""
}

func (w *peerTestWorld) deliveredFor(did, claimID string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	count := 0
	for _, witness := range w.delivered {
		if witness.WitnessID == did && witness.ClaimID == claimID {
			count++
		}
	}
	return count
}

// addHandoverPolicy replaces the first two genesis monitors with the newcomers from peerTestChangeAt.
func (w *peerTestWorld) addHandoverPolicy(t *testing.T) {
	t.Helper()
	genesis := devnet.Genesis().DistributionPolicy
	next := append([]string(nil), genesis.Monitors[2:]...)
	next = append(next, keys.DIDOf(devnet.ProviderKey(40)), keys.DIDOf(devnet.ProviderKey(41)))
	sort.Strings(next)
	policy := wire.DistributionPolicy{Revision: 2, VaultShareBP: genesis.VaultShareBP, MonitorShareBP: genesis.MonitorShareBP, MaxRetentionYears: genesis.MaxRetentionYears, MinReports: genesis.MinReports, Monitors: next, ApprovedAt: wire.FormatTime(peerTestChangeAt.Add(-30 * 24 * time.Hour)), EffectiveAt: wire.FormatTime(peerTestChangeAt)}
	var err error
	if policy.PolicyID, err = distribution.PolicyID(policy); err != nil {
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
	if policy.ReleaseSignatures, err = governance.SignRelease("distribution-policy", unsigned, devnet.ReleaseKeys()); err != nil {
		t.Fatal(err)
	}
	if err := w.service.View().AddPolicy(policy); err != nil {
		t.Fatal(err)
	}
}

// departed returns the member indexes of the two genesis monitors the handover policy removes.
func (w *peerTestWorld) departed(t *testing.T) []int {
	t.Helper()
	return w.indexes(t, devnet.Genesis().DistributionPolicy.Monitors[:2])
}

// continuing returns the member indexes of the three genesis monitors the handover policy keeps.
func (w *peerTestWorld) continuing(t *testing.T) []int {
	t.Helper()
	return w.indexes(t, devnet.Genesis().DistributionPolicy.Monitors[2:])
}

func (w *peerTestWorld) indexes(t *testing.T, dids []string) []int {
	t.Helper()
	var out []int
	for _, did := range dids {
		for _, member := range w.service.Members() {
			if member.DID() == did {
				out = append(out, member.Index())
			}
		}
	}
	if len(out) != len(dids) {
		t.Fatalf("hosted members do not include %v", dids)
	}
	return out
}

func peerTestWitness(t *testing.T, index int, claim wire.SettlementClaim) wire.SettlementWitness {
	t.Helper()
	witness, err := settlement.SignWitness(devnet.MonitorKey(index), claim, big.NewInt(1000), peerTestBase.Add(72*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return witness
}

func peerClaimIDs(claims []wire.SettlementClaim) string {
	ids := make([]string, 0, len(claims))
	for _, claim := range claims {
		ids = append(ids, claim.ClaimID)
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}
