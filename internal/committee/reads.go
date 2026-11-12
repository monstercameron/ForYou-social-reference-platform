package committee

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"foryou.local/foryou/internal/wire"
)

// AdoptedClaims returns this monitor's witnessed claims whose period end is in [from, to).
func (m *Monitor) AdoptedClaims(from, to time.Time) []ClaimRef {
	count := 0
	for slot, witness := range m.state.Witnessed {
		if _, ok := m.witnessedIn(slot, witness, from, to); ok {
			count++
		}
	}
	claims := make([]ClaimRef, 0, count)
	for slot, witness := range m.state.Witnessed {
		if claim, ok := m.witnessedIn(slot, witness, from, to); ok {
			claims = append(claims, ClaimRef{ProviderID: claim.ProviderID, Network: claim.Network, ClaimID: claim.ClaimID, Sequence: claim.Sequence})
		}
	}
	slices.SortFunc(claims, func(a, b ClaimRef) int {
		return cmp.Or(strings.Compare(a.ProviderID, b.ProviderID), strings.Compare(a.Network, b.Network), cmp.Compare(a.Sequence, b.Sequence))
	})
	return claims
}

func (m *Monitor) witnessedIn(slot string, witness wire.SettlementWitness, from, to time.Time) (wire.SettlementClaim, bool) {
	if _, held := m.state.Holds[slot]; held {
		return wire.SettlementClaim{}, false
	}
	claim, ok := m.state.Claims[witness.ClaimID]
	if !ok {
		return wire.SettlementClaim{}, false
	}
	periodEnd, err := wire.ParseTime(claim.PeriodEnd)
	return claim, err == nil && !periodEnd.Before(from) && periodEnd.Before(to)
}

// ReceiptClaim returns this monitor's witnessed or adopted claim containing receiptHash. providerID may be any key on the line.
func (m *Monitor) ReceiptClaim(providerID, network, receiptHash string) (wire.SettlementClaim, bool) {
	line := m.firstKey(providerID)
	var sequence int64
	indexed := false
	for chain, hashes := range m.state.ReceiptIndex {
		separator := strings.IndexByte(chain, '\x00')
		if separator < 0 || chain[:separator] != line || chain[separator+1:] != network {
			continue
		}
		sequence, indexed = hashes[receiptHash]
		break
	}
	if !indexed {
		return wire.SettlementClaim{}, false
	}
	for _, witness := range m.state.Witnessed {
		if witness.Network != network || witness.Sequence != sequence {
			continue
		}
		claim, ok := m.state.Claims[witness.ClaimID]
		if ok && m.firstKey(claim.ProviderID) == line {
			return cloneClaim(claim), true
		}
	}
	return wire.SettlementClaim{}, false
}

// firstKey resolves the first key without allocating, because Member.View reads may allocate only their result.
func (m *Monitor) firstKey(providerID string) string {
	line := providerID
	for {
		found := false
		for _, succession := range m.state.Successions {
			if succession.NewID == line {
				line = succession.OldID
				found = true
				break
			}
		}
		if !found {
			return line
		}
	}
}

// Declaration returns the registered retention declaration with declarationID.
func (m *Monitor) Declaration(declarationID string) (wire.RetentionDeclaration, bool) {
	declaration, ok := m.state.Declarations[declarationID]
	return declaration, ok
}
