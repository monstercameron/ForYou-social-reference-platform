package committee

import (
	"errors"
	"math/big"
	"time"

	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/wire"
)

// Adopt witnesses a claim that a quorum of committee witnessed, for catching up or a handover, without the lateness
// and leaf rules. Its ancestors become this monitor's predecessors, and its holds at their sequences end.
func (m *Monitor) Adopt(c wire.SettlementClaim, ws []wire.SettlementWitness, committee []string, at time.Time, successions []wire.KeySuccession, ancestors []wire.SettlementClaim) (wire.SettlementWitness, error) {
	for _, item := range successions {
		if err := m.RegisterSuccession(item); err != nil {
			return wire.SettlementWitness{}, err
		}
	}
	slot := slotKey(m.Line(c.ProviderID), c.Network, c.Sequence)
	var existing *wire.SettlementWitness
	if held, ok := m.state.Witnessed[slot]; ok {
		if held.ClaimID != c.ClaimID {
			return wire.SettlementWitness{}, errors.New("committee: already witnessed another claim at sequence")
		}
		existing = &held
	}
	if err := settlement.VerifyClaimObject(c); err != nil {
		return wire.SettlementWitness{}, err
	}
	if !settlement.Settled(settlement.CountWitnesses(c, ws, committee, nil), committee) {
		return wire.SettlementWitness{}, errors.New("committee: claim has no committee quorum")
	}
	if existing != nil {
		return *existing, nil
	}
	chain := append(append([]wire.SettlementClaim(nil), ancestors...), c)
	for i, older := range chain {
		if err := settlement.VerifyClaimObject(older); err != nil {
			return wire.SettlementWitness{}, err
		}
		if i > 0 {
			if err := settlement.CheckLink(chain[i], &chain[i-1], m.succession); err != nil {
				return wire.SettlementWitness{}, err
			}
		}
	}
	previous := m.predecessor(c)
	if len(ancestors) > 0 {
		last := cloneClaim(ancestors[len(ancestors)-1])
		previous = &last
	}
	if c.Sequence > 1 && previous == nil {
		return wire.SettlementWitness{}, errors.New("committee: adopted claim predecessor is unknown")
	}
	if err := settlement.CheckLink(c, previous, m.succession); err != nil {
		return wire.SettlementWitness{}, err
	}
	deposited, err := m.deposited(m.Line(c.ProviderID), c.Network)
	if err != nil {
		return wire.SettlementWitness{}, err
	}
	credit, err := settlement.Credit(deposited, c)
	if err != nil {
		return wire.SettlementWitness{}, err
	}
	if credit.Cmp(big.NewInt(0)) < 0 {
		return wire.SettlementWitness{}, errors.New("committee: adopted claim is unfunded")
	}
	witness, err := settlement.SignWitness(m.key, c, credit, at)
	if err != nil {
		return wire.SettlementWitness{}, err
	}
	for _, ancestor := range ancestors {
		ancestorSlot := slotKey(m.Line(ancestor.ProviderID), ancestor.Network, ancestor.Sequence)
		if held, ok := m.state.Witnessed[ancestorSlot]; !ok || held.ClaimID != ancestor.ClaimID {
			if ok {
				m.state.Equivocations = append(m.state.Equivocations, [2]wire.SettlementClaim{cloneClaim(m.state.Claims[held.ClaimID]), cloneClaim(ancestor)})
			}
			m.state.Followed[ancestorSlot] = cloneClaim(ancestor)
			m.markDependentClaimsDue(ancestor)
		}
		delete(m.state.Holds, ancestorSlot)
	}
	m.state.Witnessed[slot] = witness
	delete(m.state.Holds, slot)
	m.recordAdopted(c, nil)
	return witness, nil
}

// Follow records a settled claim as the predecessor while preserving this monitor's conflicting witness.
func (m *Monitor) Follow(c wire.SettlementClaim, ws []wire.SettlementWitness, committee []string) error {
	slot := slotKey(m.Line(c.ProviderID), c.Network, c.Sequence)
	held, ok := m.state.Witnessed[slot]
	if !ok || held.ClaimID == c.ClaimID {
		return errors.New("committee: nothing to follow")
	}
	if err := settlement.VerifyClaimObject(c); err != nil {
		return err
	}
	if !settlement.Settled(settlement.CountWitnesses(c, ws, committee, nil), committee) {
		return errors.New("committee: claim has no committee quorum")
	}
	m.state.Equivocations = append(m.state.Equivocations, [2]wire.SettlementClaim{cloneClaim(m.state.Claims[held.ClaimID]), cloneClaim(c)})
	m.state.Followed[slot] = cloneClaim(c)
	m.markDependentClaimsDue(c)
	return nil
}
