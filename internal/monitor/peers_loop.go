package monitor

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"time"

	"foryou.local/foryou/internal/committee"
	"foryou.local/foryou/internal/sdk"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/store"
	"foryou.local/foryou/internal/wire"
)

const (
	peerPagesPerPass    = 100
	peerObjectsPerPass  = 10000
	peerAncestorLimit   = 1000
	peerClaimsPageLimit = 100
	peerHoldLimit       = 64
	peerLeafLimit       = 10000
	peerFetchTimeout    = 30 * time.Second
)

var (
	// peerSequencesPerChain bounds the sequences one member settles or hands over per provider chain in one pass.
	peerSequencesPerChain = 100

	peerCursors = store.Collection[peerCursor]{Name: "monitor.peer.cursor"}
)

// peerCursor is a member's progress on one provider chain: the last sequence it handled as settled, the claim
// settled there, and whether its handover of the chain has ended.
type peerCursor struct {
	Sequence     int64  `json:"sequence"`
	ClaimID      string `json:"claim_id"`
	HandoverDone bool   `json:"handover_done"`
}

// peerChain is one provider chain as seen in one pass.
type peerChain struct {
	provider   *sdk.Client
	providerID string
	network    string
	now        time.Time
	current    []string
	previous   []string
	changedAt  time.Time
}

// peerEvidence holds the verified objects gathered for one provider chain.
type peerEvidence struct {
	claims    map[string]wire.SettlementClaim
	witnesses map[string]map[string]wire.SettlementWitness
	holds     map[string]wire.SettlementHold
	complete  map[string]bool
}

type peerPlan struct {
	member   *Member
	cursor   peerCursor
	handover bool
}

func init() {
	loopSteps = append(loopSteps, loopStep{order: peerSyncOrder, name: "peer sync", run: peerSync})
}

// peerSync reads the committee's witness pages for every provider chain once per pass, then lets each hosted
// committee member catch up on, follow or take over the sequences a quorum settled. Unreachable peers and
// providers are logged and skipped.
func peerSync(s *Service, ctx context.Context) error {
	now := s.Now()
	current := s.View().CommitteeAt(now)
	var hosted []*Member
	for _, member := range s.Members() {
		if containsDID(current, member.DID()) {
			hosted = append(hosted, member)
		}
	}
	if len(hosted) == 0 {
		return nil
	}
	previous, changedAt, _ := s.View().PreviousCommittee(now)
	for _, configured := range s.ProviderList() {
		provider, err := vaultProviderClient(s, configured)
		if err != nil {
			log.Printf("monitor: peer sync provider %s: %v", configured.URL, err)
			continue
		}
		for _, network := range []string{wire.BitcoinMainnet, wire.EthereumMainnet} {
			chain := peerChain{provider: provider, providerID: configured.ID, network: network, now: now, current: current, previous: previous, changedAt: changedAt}
			if err := peerSyncChain(s, ctx, chain, hosted); err != nil {
				log.Printf("monitor: peer sync %s %s: %v", configured.ID, network, err)
			}
		}
	}
	return nil
}

func peerSyncChain(s *Service, ctx context.Context, chain peerChain, hosted []*Member) error {
	plans := make([]peerPlan, 0, len(hosted))
	after := int64(-1)
	anyHandover := false
	for _, member := range hosted {
		cursor, err := peerLoadCursor(member, chain.providerID, chain.network)
		if err != nil {
			return err
		}
		handover := peerHandoverPending(s, member, cursor, chain.now)
		plans = append(plans, peerPlan{member: member, cursor: cursor, handover: handover})
		anyHandover = anyHandover || handover
		if after < 0 || cursor.Sequence < after {
			after = cursor.Sequence
		}
	}
	ev := peerEvidence{claims: make(map[string]wire.SettlementClaim), witnesses: make(map[string]map[string]wire.SettlementWitness), holds: make(map[string]wire.SettlementHold), complete: make(map[string]bool)}
	providerCtx, cancel := context.WithTimeout(ctx, peerFetchTimeout)
	defer cancel()
	if err := peerCollectClaims(providerCtx, chain, after, ev); err != nil {
		log.Printf("monitor: peer sync claims of %s: %v", chain.providerID, err)
	}
	for _, did := range peerUnion(chain.current, chain.previous) {
		peerCollectPage(s, ctx, chain, did, after, ev)
	}
	if anyHandover {
		peerCollectProviderWitnesses(providerCtx, chain, after, ev)
	}
	for _, plan := range plans {
		memberCtx, cancelMember := context.WithTimeout(ctx, peerFetchTimeout)
		var err error
		if plan.handover {
			err = peerHandover(memberCtx, chain, plan, ev)
		} else {
			err = peerCatchUp(memberCtx, chain, plan, ev)
		}
		cancelMember()
		if err != nil {
			log.Printf("monitor: peer sync member %d on %s %s: %v", plan.member.Index(), chain.providerID, chain.network, err)
		}
	}
	return nil
}

// peerHandoverPending reports whether the member joined at the latest committee change and has not finished its
// handover of the chain whose cursor is given; until then it decides that chain only through the handover.
func peerHandoverPending(s *Service, member *Member, cursor peerCursor, now time.Time) bool {
	joinedAt, joined := s.View().JoinedAt(member.DID(), now)
	previous, changedAt, changed := s.View().PreviousCommittee(now)
	return joined && changed && len(previous) > 0 && joinedAt.Equal(changedAt) && !cursor.HandoverDone
}

// peerAuditBlocked reports whether the audit step must leave a provider chain alone for the member because its
// handover of that chain is not finished.
func peerAuditBlocked(s *Service, member *Member, providerURL, network string, now time.Time) bool {
	for _, provider := range s.ProviderList() {
		if provider.URL != providerURL {
			continue
		}
		cursor, err := peerLoadCursor(member, provider.ID, network)
		if err != nil || peerHandoverPending(s, member, cursor, now) {
			return true
		}
	}
	return false
}

func peerCollectClaims(ctx context.Context, chain peerChain, after int64, ev peerEvidence) error {
	cursor := uint64(after)
	for len(ev.claims) < peerAncestorLimit+peerSequencesPerChain {
		page, _, err := chain.provider.SettlementClaims(ctx, chain.network, cursor)
		if err != nil {
			return err
		}
		for _, claim := range page.Claims {
			peerAddClaim(chain, ev, claim)
			cursor = uint64(claim.Sequence)
		}
		if len(page.Claims) < peerClaimsPageLimit {
			return nil
		}
	}
	return nil
}

// peerCollectPage pages through one committee monitor's witnesses and records whether it read the page to its end.
func peerCollectPage(s *Service, ctx context.Context, chain peerChain, did string, after int64, ev peerEvidence) {
	base := s.Peers()[did]
	for _, member := range s.Members() {
		if member.DID() == did {
			base = s.MemberURL(member.Index())
		}
	}
	if base == "" {
		log.Printf("monitor: peer sync: no URL for monitor %s", did)
		return
	}
	client, err := sdk.New(base, sdk.Options{ServiceID: did, AllowLoopbackHTTP: true, HTTPClient: s.HTTPClient(), Now: s.Now})
	if err != nil {
		log.Printf("monitor: peer sync monitor %s: %v", did, err)
		return
	}
	peerCtx, cancel := context.WithTimeout(ctx, peerFetchTimeout)
	defer cancel()
	cursor, objects := after, 0
	for pages := 0; pages < peerPagesPerPass; pages++ {
		page, _, err := client.Witnesses(peerCtx, chain.providerID, chain.network, uint64(cursor))
		if err != nil {
			log.Printf("monitor: peer sync monitor %s: %v", did, err)
			return
		}
		objects += len(page.Witnesses) + len(page.Refusals) + len(page.Holds) + len(page.Claims)
		if objects > peerObjectsPerPass {
			log.Printf("monitor: peer sync monitor %s: more than %d objects in one pass", did, peerObjectsPerPass)
			return
		}
		if len(page.Witnesses)+len(page.Refusals)+len(page.Holds) == 0 {
			ev.complete[did] = true
			return
		}
		for _, claim := range page.Claims {
			if claim.Sequence > after {
				peerAddClaim(chain, ev, claim)
			}
		}
		last := cursor
		for _, witness := range page.Witnesses {
			claim, known := ev.claims[witness.ClaimID]
			if witness.WitnessID == did && witness.Sequence > after && known && settlement.WitnessMatches(witness, claim) && settlement.VerifyWitness(witness) == nil {
				peerAddWitness(ev, witness)
			}
			last = max(last, witness.Sequence)
		}
		for _, hold := range page.Holds {
			if hold.WitnessID == did && hold.ProviderID == chain.providerID && hold.Network == chain.network && hold.Sequence > after && settlement.VerifyHold(hold) == nil {
				ev.holds[fmt.Sprintf("%s\x00%d", did, hold.Sequence)] = hold
			}
			last = max(last, hold.Sequence)
		}
		for _, refusal := range page.Refusals {
			last = max(last, refusal.Sequence)
		}
		if last <= cursor {
			log.Printf("monitor: peer sync monitor %s: page does not advance past sequence %d", did, cursor)
			return
		}
		cursor = last
	}
}

// peerCollectProviderWitnesses gathers witnesses, departed monitors' included, that the provider holds for the claims
// a handover may decide in this pass.
func peerCollectProviderWitnesses(ctx context.Context, chain peerChain, after int64, ev peerEvidence) {
	members := peerUnion(chain.current, chain.previous)
	for _, claim := range ev.claims {
		if claim.Sequence > after+int64(peerSequencesPerChain) {
			continue
		}
		page, _, err := chain.provider.ClaimWitnesses(ctx, claim.ClaimID)
		if err != nil {
			log.Printf("monitor: peer sync witnesses of %s: %v", claim.ClaimID, err)
			continue
		}
		for _, witness := range page.Witnesses {
			if containsDID(members, witness.WitnessID) && settlement.WitnessMatches(witness, claim) && settlement.VerifyWitness(witness) == nil {
				peerAddWitness(ev, witness)
			}
		}
	}
}

// peerCatchUp adopts or follows, in sequence order, the claims a quorum of the current committee witnessed.
func peerCatchUp(ctx context.Context, chain peerChain, plan peerPlan, ev peerEvidence) error {
	held, err := peerHeld(plan.member, chain, plan.cursor.Sequence)
	if err != nil {
		return err
	}
	cursor := plan.cursor
	err = peerSettle(ctx, chain, plan.member, ev, held, &cursor)
	if cursor != plan.cursor {
		err = errors.Join(err, peerSaveCursor(plan.member, chain, cursor))
	}
	return err
}

func peerSettle(ctx context.Context, chain peerChain, member *Member, ev peerEvidence, held map[int64]string, cursor *peerCursor) error {
	handled := 0
	for sequence := cursor.Sequence + 1; sequence <= cursor.Sequence+peerAncestorLimit && handled < peerSequencesPerChain; sequence++ {
		settled, ok := ev.settledAt(sequence, chain.current)
		if !ok {
			if len(ev.claimsAt(sequence)) == 0 {
				return nil
			}
			continue
		}
		witnesses := ev.witnessList(settled.ClaimID)
		mine, holds := held[sequence]
		switch {
		case holds && mine == settled.ClaimID:
		case holds:
			if err := member.Do(func(m *committee.Monitor) error { return m.Follow(settled, witnesses, chain.current) }); err != nil {
				return err
			}
			handled++
		default:
			ancestors, err := ev.ancestors(*cursor, settled, held)
			if err != nil {
				return err
			}
			successions, err := peerSuccessions(ctx, chain, ev, append(ancestors, settled))
			if err != nil {
				return err
			}
			var signed wire.SettlementWitness
			if err := member.Do(func(m *committee.Monitor) error {
				var err error
				signed, err = m.Adopt(settled, witnesses, chain.current, chain.now, successions, ancestors)
				return err
			}); err != nil {
				return err
			}
			peerDeliver(ctx, chain, &signed)
			handled++
		}
		*cursor = peerCursor{Sequence: sequence, ClaimID: settled.ClaimID}
	}
	return nil
}

// peerHandover takes over a newcomer's chain sequence by sequence after its committee change, and ends the handover
// at the first sequence that yields a hold or has no known claim.
func peerHandover(ctx context.Context, chain peerChain, plan peerPlan, ev peerEvidence) error {
	effectiveAt := chain.changedAt
	if chain.now.Before(effectiveAt) {
		return nil
	}
	continuing := peerIntersect(chain.previous, chain.current)
	for _, did := range continuing {
		if !ev.complete[did] {
			return fmt.Errorf("continuing monitor %s's page was not read in full", did)
		}
	}
	held, err := peerHeld(plan.member, chain, plan.cursor.Sequence)
	if err != nil {
		return err
	}
	cursor := plan.cursor
	present := peerUnion(chain.current, chain.previous)
	for handled := 0; handled < peerSequencesPerChain; handled++ {
		sequence := cursor.Sequence + 1
		heldIDs := make(map[string]bool)
		var holds []wire.SettlementHold
		for _, hold := range ev.holds {
			if hold.Sequence == sequence && containsDID(chain.previous, hold.WitnessID) {
				holds = append(holds, hold)
				for _, id := range hold.ClaimIDs {
					heldIDs[id] = true
				}
			}
		}
		var candidates []committee.Candidate
		var periodEnds []time.Time
		for _, claim := range ev.claimsAt(sequence) {
			witnesses := peerHandoverWitnesses(chain, ev.witnessList(claim.ClaimID))
			if len(settlement.CountWitnesses(claim, witnesses, present, nil)) == 0 && !heldIDs[claim.ClaimID] {
				continue
			}
			end, err := wire.ParseTime(claim.PeriodEnd)
			if err != nil {
				return err
			}
			periodEnds = append(periodEnds, end)
			candidates = append(candidates, committee.Candidate{Claim: claim, Witnesses: witnesses})
		}
		if len(candidates) == 0 {
			cursor.HandoverDone = true
			return peerSaveCursor(plan.member, chain, cursor)
		}
		if len(candidates) > peerHoldLimit {
			return fmt.Errorf("sequence %d has %d known claims; hold limit is %d", sequence, len(candidates), peerHoldLimit)
		}
		if chain.now.Before(committee.HandoverWait(effectiveAt, periodEnds)) {
			return nil
		}
		ancestors := make(map[string][]wire.SettlementClaim, len(candidates))
		var successions []wire.KeySuccession
		for i := range candidates {
			candidates[i].Leaves = peerLeaves(ctx, chain, candidates[i].Claim)
			chainOf, err := ev.ancestors(cursor, candidates[i].Claim, held)
			if err != nil {
				continue
			}
			named, err := peerSuccessions(ctx, chain, ev, append(chainOf, candidates[i].Claim))
			if err != nil {
				continue
			}
			ancestors[candidates[i].Claim.ClaimID] = chainOf
			successions = append(successions, named...)
		}
		var result committee.TakeoverResult
		if err := plan.member.Do(func(m *committee.Monitor) error {
			var err error
			result, err = m.Takeover(candidates, chain.previous, chain.now, holds, chain.current, ancestors, successions)
			return err
		}); err != nil {
			return err
		}
		peerDeliver(ctx, chain, result.Witness)
		if result.Witness == nil {
			cursor.HandoverDone = true
			return peerSaveCursor(plan.member, chain, cursor)
		}
		cursor = peerCursor{Sequence: sequence, ClaimID: result.Witness.ClaimID}
		held[sequence] = result.Witness.ClaimID
		if err := peerSaveCursor(plan.member, chain, cursor); err != nil {
			return err
		}
	}
	return nil
}

// peerHandoverWitnesses drops witnesses that departed monitors signed at or after the change, when they were no
// longer allowed to sign.
func peerHandoverWitnesses(chain peerChain, witnesses []wire.SettlementWitness) []wire.SettlementWitness {
	kept := make([]wire.SettlementWitness, 0, len(witnesses))
	for _, witness := range witnesses {
		if !containsDID(chain.current, witness.WitnessID) {
			at, err := wire.ParseTime(witness.WitnessedAt)
			if err != nil || !at.Before(chain.changedAt) {
				continue
			}
		}
		kept = append(kept, witness)
	}
	return kept
}

// peerHeld returns the claim IDs the member witnessed on the chain after a sequence, by sequence.
func peerHeld(member *Member, chain peerChain, after int64) (map[int64]string, error) {
	held := make(map[int64]string)
	err := member.View(func(m *committee.Monitor) error {
		page := m.Page(chain.providerID, chain.network, after, peerAncestorLimit+peerSequencesPerChain)
		for _, witness := range page.Witnesses {
			held[witness.Sequence] = witness.ClaimID
		}
		return nil
	})
	return held, err
}

// ancestors returns the claims between the cursor and c, starting at the first sequence the member does not hold.
func (ev peerEvidence) ancestors(cursor peerCursor, c wire.SettlementClaim, held map[int64]string) ([]wire.SettlementClaim, error) {
	var chain []wire.SettlementClaim
	link := c
	for link.Sequence-1 > cursor.Sequence {
		if link.PreviousClaimID == nil {
			return nil, errors.New("claim names no predecessor")
		}
		previous, ok := ev.claims[*link.PreviousClaimID]
		if !ok || previous.Sequence != link.Sequence-1 {
			return nil, fmt.Errorf("predecessor of sequence %d is unavailable", link.Sequence)
		}
		chain = append(chain, previous)
		link = previous
	}
	if cursor.Sequence > 0 && link.Sequence > 1 && (link.PreviousClaimID == nil || *link.PreviousClaimID != cursor.ClaimID) {
		return nil, fmt.Errorf("claim at sequence %d does not chain from the settled claim at sequence %d", c.Sequence, cursor.Sequence)
	}
	sort.Slice(chain, func(i, j int) bool { return chain[i].Sequence < chain[j].Sequence })
	for len(chain) > 0 && held[chain[0].Sequence] == chain[0].ClaimID {
		chain = chain[1:]
	}
	return chain, nil
}

// peerSuccessions fetches the key successions that a run of linked claims names.
func peerSuccessions(ctx context.Context, chain peerChain, ev peerEvidence, claims []wire.SettlementClaim) ([]wire.KeySuccession, error) {
	var named []wire.KeySuccession
	for _, claim := range claims {
		if claim.SuccessionID == nil || claim.PreviousClaimID == nil {
			continue
		}
		previous, ok := ev.claims[*claim.PreviousClaimID]
		if !ok {
			return nil, fmt.Errorf("predecessor of succession claim %s is unavailable", claim.ClaimID)
		}
		items, _, _, err := chain.provider.Successions(ctx, previous.ProviderID)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			if item.SuccessionID == *claim.SuccessionID {
				named = append(named, item)
			}
		}
	}
	return named, nil
}

// peerLeaves returns a claim's leaves from the provider, or nil when they are unavailable.
func peerLeaves(ctx context.Context, chain peerChain, claim wire.SettlementClaim) []wire.SettlementLeaf {
	if claim.ReceiptCount <= 0 || claim.ReceiptCount > peerLeafLimit {
		return nil
	}
	leaves := make([]wire.SettlementLeaf, 0, claim.ReceiptCount)
	for int64(len(leaves)) < claim.ReceiptCount {
		page, _, err := chain.provider.ClaimLeaves(ctx, claim.ClaimID, uint64(len(leaves)))
		if err != nil || len(page.Leaves) == 0 || int64(len(leaves)+len(page.Leaves)) > claim.ReceiptCount {
			return nil
		}
		leaves = append(leaves, page.Leaves...)
	}
	return leaves
}

func peerDeliver(ctx context.Context, chain peerChain, witness *wire.SettlementWitness) {
	if witness == nil {
		return
	}
	if _, err := chain.provider.DeliverWitness(ctx, *witness); err != nil {
		log.Printf("monitor: peer sync deliver witness for %s: %v", witness.ClaimID, err)
	}
}

func peerAddClaim(chain peerChain, ev peerEvidence, claim wire.SettlementClaim) {
	if claim.ProviderID == chain.providerID && claim.Network == chain.network && settlement.VerifyClaimObject(claim) == nil {
		ev.claims[claim.ClaimID] = claim
	}
}

func peerAddWitness(ev peerEvidence, witness wire.SettlementWitness) {
	if ev.witnesses[witness.ClaimID] == nil {
		ev.witnesses[witness.ClaimID] = make(map[string]wire.SettlementWitness)
	}
	ev.witnesses[witness.ClaimID][witness.WitnessID] = witness
}

func (ev peerEvidence) witnessList(claimID string) []wire.SettlementWitness {
	list := make([]wire.SettlementWitness, 0, len(ev.witnesses[claimID]))
	for _, witness := range ev.witnesses[claimID] {
		list = append(list, witness)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].WitnessID < list[j].WitnessID })
	return list
}

// settledAt returns the claim at a sequence that a quorum of the committee witnessed.
func (ev peerEvidence) settledAt(sequence int64, members []string) (wire.SettlementClaim, bool) {
	for _, claim := range ev.claimsAt(sequence) {
		if settlement.Settled(settlement.CountWitnesses(claim, ev.witnessList(claim.ClaimID), members, nil), members) {
			return claim, true
		}
	}
	return wire.SettlementClaim{}, false
}

func (ev peerEvidence) claimsAt(sequence int64) []wire.SettlementClaim {
	var claims []wire.SettlementClaim
	for _, claim := range ev.claims {
		if claim.Sequence == sequence {
			claims = append(claims, claim)
		}
	}
	sort.Slice(claims, func(i, j int) bool { return claims[i].ClaimID < claims[j].ClaimID })
	return claims
}

func peerLoadCursor(member *Member, providerID, network string) (peerCursor, error) {
	var cursor peerCursor
	err := member.DB().View(func(tx store.Tx) error {
		var err error
		cursor, _, err = peerCursors.Get(tx, providerID+"\x00"+network)
		return err
	})
	return cursor, err
}

func peerSaveCursor(member *Member, chain peerChain, cursor peerCursor) error {
	return member.DB().Update(func(tx store.Tx) error {
		return peerCursors.Put(tx, chain.providerID+"\x00"+chain.network, cursor)
	})
}

func peerUnion(a, b []string) []string {
	union := append([]string(nil), a...)
	for _, did := range b {
		if !containsDID(union, did) {
			union = append(union, did)
		}
	}
	return union
}

func peerIntersect(a, b []string) []string {
	var both []string
	for _, did := range a {
		if containsDID(b, did) {
			both = append(both, did)
		}
	}
	return both
}
