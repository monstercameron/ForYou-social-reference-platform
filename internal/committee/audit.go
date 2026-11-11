package committee

import (
	"errors"
	"math/big"
	"time"

	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/wire"
)

// Outcome contains exactly one signed monitor decision.
type Outcome struct {
	Witness *wire.SettlementWitness
	Refusal *wire.SettlementRefusal
}

// ClaimRef identifies a claim that should be audited again.
type ClaimRef struct {
	ProviderID, Network, ClaimID string
	Sequence                     int64
}

// Check audits a claim and returns its available credit or its first refusal fault.
func (m *Monitor) Check(c wire.SettlementClaim, leaves []wire.SettlementLeaf, seenAt time.Time, lateness bool) (*big.Int, *Fault, error) {
	if err := settlement.VerifyClaimObject(c); err != nil {
		return nil, fault(settlement.ReasonBadSignature, true), nil
	}
	previous := m.predecessor(c)
	if previous == nil && c.Sequence > 1 {
		if m.hasWitnessedClaim(value(c.PreviousClaimID)) {
			// The named predecessor is held under another line, so what is missing is the succession to this key.
			f := fault(settlement.ReasonUnknownProviderKey, false)
			f.dependency = dependencySuccession
			return nil, f, nil
		}
		f := fault(settlement.ReasonBadChain, false)
		f.dependency = dependencyPredecessor
		return nil, f, nil
	}
	if err := settlement.CheckLink(c, previous, m.succession); err != nil {
		if mapped := mapLinkFault(err); mapped != nil {
			return nil, mapped, nil
		}
		return nil, nil, err
	}
	periodEnd, err := wire.ParseTime(c.PeriodEnd)
	if err != nil {
		return nil, nil, err
	}
	seenAt = seenAt.UTC().Truncate(time.Second)
	if seenAt.Before(periodEnd) || lateness && seenAt.Sub(periodEnd) > 90*24*time.Hour {
		return nil, fault(settlement.ReasonBadPeriod, false), nil
	}
	if err := settlement.CheckDeclaration(c, previous, func(id string) (wire.RetentionDeclaration, bool) {
		d, ok := m.state.Declarations[id]
		return d, ok
	}); err != nil {
		switch {
		case errors.Is(err, settlement.ErrDeclarationRule):
			return nil, fault(settlement.ReasonBadPeriod, true), nil
		case errors.Is(err, settlement.ErrDeclarationUnknown):
			f := fault(settlement.ReasonBadPeriod, false)
			f.dependency = dependencyDeclaration
			return nil, f, nil
		default:
			return nil, nil, err
		}
	}
	if err := settlement.CheckLeafSet(leaves); err != nil {
		return nil, fault(settlement.ReasonBadLeaf, true), nil
	}
	for _, leaf := range leaves {
		if err := settlement.CheckLeaf(c, leaf, m.schedules); err != nil {
			switch {
			case errors.Is(err, settlement.ErrLeafPeriod), errors.Is(err, settlement.ErrLeafFee):
				return nil, fault(settlement.ReasonBadLeaf, true), nil
			case errors.Is(err, settlement.ErrScheduleUnknown):
				f := fault(settlement.ReasonBadLeaf, false)
				f.dependency = dependencySchedule
				return nil, f, nil
			default:
				return nil, nil, err
			}
		}
		chain := chainKey(m.Line(c.ProviderID), c.Network)
		if sequence, exists := m.state.ReceiptIndex[chain][leaf.ReceiptHash]; exists && sequence != c.Sequence {
			return nil, fault(settlement.ReasonDuplicateReceipt, true), nil
		}
	}
	if err := settlement.CheckTotals(c, leaves); err != nil {
		if errors.Is(err, settlement.ErrTotals) {
			return nil, fault(settlement.ReasonBadTotals, true), nil
		}
		return nil, nil, err
	}
	deposited, err := m.deposited(m.Line(c.ProviderID), c.Network)
	if err != nil {
		return nil, nil, err
	}
	credit, err := settlement.Credit(deposited, c)
	if err != nil {
		return nil, nil, err
	}
	if credit.Sign() < 0 {
		return nil, fault(settlement.ReasonUnfunded, false), nil
	}
	return credit, nil, nil
}

// Audit records and returns the monitor's one decision for the claim's sequence.
func (m *Monitor) Audit(c wire.SettlementClaim, leaves []wire.SettlementLeaf, seenAt, now time.Time) (Outcome, error) {
	return m.audit(c, leaves, seenAt, now, true)
}

func (m *Monitor) audit(c wire.SettlementClaim, leaves []wire.SettlementLeaf, seenAt, now time.Time, lateness bool) (Outcome, error) {
	line := m.Line(c.ProviderID)
	slot := slotKey(line, c.Network, c.Sequence)
	if old, ok := m.state.Witnessed[slot]; ok {
		if old.ClaimID == c.ClaimID {
			witness := old
			return Outcome{Witness: &witness}, nil
		}
		if settlement.VerifyClaimObject(c) == nil {
			_, problem, err := m.Check(c, leaves, seenAt, lateness)
			if err == nil && problem == nil {
				m.state.Equivocations = append(m.state.Equivocations, [2]wire.SettlementClaim{cloneClaim(m.state.Claims[old.ClaimID]), cloneClaim(c)})
			}
		}
		return m.refuse(c, leaves, settlement.ReasonBadChain, now, dependencyNone, false)
	}
	if hold, ok := m.state.Holds[slot]; ok {
		if len(hold.ClaimIDs) != 1 || hold.ClaimIDs[0] != c.ClaimID {
			return m.refuse(c, leaves, settlement.ReasonBadChain, now, dependencyNone, false)
		}
		delete(m.state.Holds, slot)
	}
	credit, problem, err := m.Check(c, leaves, seenAt, lateness)
	if err != nil {
		return Outcome{}, err
	}
	if problem != nil {
		return m.refuse(c, leaves, problem.Reason, now, problem.dependency, problem.Reason == settlement.ReasonUnfunded)
	}
	witness, err := settlement.SignWitness(m.key, c, credit, now)
	if err != nil {
		return Outcome{}, err
	}
	m.state.Witnessed[slot] = witness
	m.recordAdopted(c, leaves)
	return Outcome{Witness: &witness}, nil
}

func (m *Monitor) refuse(c wire.SettlementClaim, leaves []wire.SettlementLeaf, reason settlement.Reason, now time.Time, dependency dependency, unfunded bool) (Outcome, error) {
	refusal, err := settlement.SignRefusal(m.key, c, reason, now)
	if err != nil {
		return Outcome{}, err
	}
	record := refusalRecord{Refusal: refusal, Claim: cloneClaim(c), Dependency: dependency}
	key := refusalKey(c)
	refusals := m.state.UnverifiedRefusals
	if settlement.VerifyClaimObject(c) == nil {
		refusals = m.state.Refusals
		record.Leaves = append([]wire.SettlementLeaf(nil), leaves...)
		m.state.Claims[c.ClaimID] = cloneClaim(c)
	}
	if prior, ok := refusals[key]; ok && prior.FirstUnfunded != nil {
		record.FirstUnfunded = prior.FirstUnfunded
	}
	if unfunded && record.FirstUnfunded == nil {
		first := now.UTC().Truncate(time.Second)
		record.FirstUnfunded = &first
	}
	refusals[key] = record
	return Outcome{Refusal: &refusal}, nil
}

func value(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
