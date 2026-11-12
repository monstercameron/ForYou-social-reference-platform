package committee

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/wire"
)

// Candidate contains a provider claim and the public evidence available during handover.
type Candidate struct {
	Claim     wire.SettlementClaim
	Leaves    []wire.SettlementLeaf
	Witnesses []wire.SettlementWitness
}

// TakeoverResult contains the decision signed during handover, if any.
type TakeoverResult struct {
	Witness *wire.SettlementWitness
	Hold    *wire.SettlementHold
}

// Takeover decides one sequence at a committee handover: it adopts a claim with an outgoing quorum (registering the
// successions and following the ancestors given for it), witnesses the only valid claim when every other known claim
// is proven invalid, or signs and records a hold naming every known claim. A monitor that already witnessed a claim at
// the sequence returns that witness and decides nothing more there.
func (m *Monitor) Takeover(cs []Candidate, outgoing []string, at time.Time, holds []wire.SettlementHold, pastMembers []string, ancestors map[string][]wire.SettlementClaim, successions []wire.KeySuccession) (TakeoverResult, error) {
	if len(cs) == 0 {
		return TakeoverResult{}, nil
	}
	first := cs[0].Claim
	slot := slotKey(m.Line(first.ProviderID), first.Network, first.Sequence)
	// A witness is final: the monitor keeps it and neither holds nor witnesses another claim at its sequence.
	if kept, ok := m.state.Witnessed[slot]; ok {
		return TakeoverResult{Witness: &kept}, nil
	}
	members := append(append([]string(nil), outgoing...), pastMembers...)
	memberSet := make(map[string]bool, len(members))
	for _, id := range members {
		memberSet[id] = true
	}
	heldIDs := make(map[string]bool)
	for _, hold := range holds {
		if !contains(outgoing, hold.WitnessID) || settlement.VerifyHold(hold) != nil || m.Line(hold.ProviderID) != m.Line(first.ProviderID) || hold.Network != first.Network || hold.Sequence != first.Sequence {
			continue
		}
		for _, id := range hold.ClaimIDs {
			heldIDs[id] = true
		}
	}
	known := make([]Candidate, 0, len(cs))
	knownIDs := make(map[string]bool)
	classified := make(map[string]bool)
	valid := make([]Candidate, 0, 1)
	blocking := false
	for _, candidate := range cs {
		claim := candidate.Claim
		if m.Line(claim.ProviderID) != m.Line(first.ProviderID) || claim.Network != first.Network || claim.Sequence != first.Sequence {
			return TakeoverResult{}, errors.New("committee: candidates must identify one provider sequence")
		}
		if settlement.VerifyClaimObject(claim) != nil {
			continue
		}
		if classified[claim.ClaimID] {
			continue
		}
		classified[claim.ClaimID] = true
		allWitnesses := make([]wire.SettlementWitness, 0, len(candidate.Witnesses))
		outgoingWitnesses := make([]wire.SettlementWitness, 0, len(candidate.Witnesses))
		for _, witness := range candidate.Witnesses {
			if !memberSet[witness.WitnessID] || !settlement.WitnessMatches(witness, claim) || settlement.VerifyWitness(witness) != nil {
				continue
			}
			allWitnesses = append(allWitnesses, witness)
			if contains(outgoing, witness.WitnessID) {
				outgoingWitnesses = append(outgoingWitnesses, witness)
			}
		}
		if len(allWitnesses) == 0 && !heldIDs[claim.ClaimID] {
			continue
		}
		known = append(known, candidate)
		knownIDs[claim.ClaimID] = true
		if settlement.Settled(settlement.CountWitnesses(claim, outgoingWitnesses, outgoing, nil), outgoing) {
			chain := ancestors[claim.ClaimID]
			witness, err := m.Adopt(claim, candidate.Witnesses, outgoing, at, successions, chain)
			if err != nil {
				return TakeoverResult{}, err
			}
			return TakeoverResult{Witness: &witness}, nil
		}
		if candidate.Leaves == nil {
			blocking = true
			continue
		}
		_, problem, err := m.Check(claim, candidate.Leaves, at, false)
		if err != nil {
			return TakeoverResult{}, err
		}
		if problem == nil {
			valid = append(valid, candidate)
		} else if !problem.Provable {
			blocking = true
		}
	}
	if len(valid) == 1 && !blocking {
		delete(m.state.Holds, slot)
		delete(m.state.HeldLeaves, slot)
		outcome, err := m.audit(valid[0].Claim, valid[0].Leaves, at, at, false)
		if err != nil {
			return TakeoverResult{}, err
		}
		return TakeoverResult{Witness: outcome.Witness}, nil
	}
	if len(known) == 0 {
		return TakeoverResult{}, nil
	}
	if len(knownIDs) > 64 {
		return TakeoverResult{}, fmt.Errorf("committee: handover knows %d claims; hold limit is 64", len(knownIDs))
	}
	heldLeaves := make(map[string][]wire.SettlementLeaf)
	for _, candidate := range known {
		m.state.Claims[candidate.Claim.ClaimID] = cloneClaim(candidate.Claim)
		if candidate.Leaves != nil {
			heldLeaves[candidate.Claim.ClaimID] = append([]wire.SettlementLeaf(nil), candidate.Leaves...)
		}
	}
	ids := make([]string, 0, len(knownIDs))
	for id := range knownIDs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	hold, err := settlement.SignHold(m.key, first.ProviderID, first.Network, first.Sequence, ids, at)
	if err != nil {
		return TakeoverResult{}, err
	}
	m.state.Holds[slot] = hold
	m.state.HeldLeaves[slot] = heldLeaves
	return TakeoverResult{Hold: &hold}, nil
}

// reviewHold checks the hold at slot again when claimID arrives there and reports whether it still blocks that claim.
// A hold stands until all but one of its claims are proven invalid, so the claims now proven invalid are dropped and the
// hold is signed again; it ends when none is left or only the arriving claim is.
func (m *Monitor) reviewHold(slot, claimID string, now time.Time) (bool, error) {
	hold := m.state.Holds[slot]
	standing := make([]string, 0, len(hold.ClaimIDs))
	for _, id := range hold.ClaimIDs {
		if id != claimID {
			invalid, err := m.provenInvalid(id, m.state.HeldLeaves[slot][id], now)
			if err != nil {
				return false, err
			}
			if invalid {
				continue
			}
		}
		standing = append(standing, id)
	}
	if len(standing) == 0 || len(standing) == 1 && standing[0] == claimID {
		delete(m.state.Holds, slot)
		delete(m.state.HeldLeaves, slot)
		return false, nil
	}
	if len(standing) < len(hold.ClaimIDs) {
		narrowed, err := settlement.SignHold(m.key, hold.ProviderID, hold.Network, hold.Sequence, standing, now)
		if err != nil {
			return false, err
		}
		m.state.Holds[slot] = narrowed
		for id := range m.state.HeldLeaves[slot] {
			if !contains(standing, id) {
				delete(m.state.HeldLeaves[slot], id)
			}
		}
	}
	return true, nil
}

// provenInvalid reports whether a held claim fails audit by facts that cannot change: a provable fault found from the
// claim, its adopted predecessor and the objects it names. Without its leaves only the rules that need none can prove it.
func (m *Monitor) provenInvalid(claimID string, leaves []wire.SettlementLeaf, now time.Time) (bool, error) {
	claim, ok := m.state.Claims[claimID]
	if !ok {
		return false, nil
	}
	var problem *Fault
	var err error
	if leaves == nil {
		problem, err = m.checkWithoutLeaves(claim, now, false)
	} else {
		_, problem, err = m.Check(claim, leaves, now, false)
	}
	if err != nil {
		return false, err
	}
	return problem != nil && problem.Provable, nil
}

// HandoverWait returns the earliest takeover time required by known claim periods.
func HandoverWait(effectiveAt time.Time, knownPeriodEnds []time.Time) time.Time {
	for _, end := range knownPeriodEnds {
		if end.Before(effectiveAt) {
			return effectiveAt.Add(48 * time.Hour)
		}
	}
	return effectiveAt
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
