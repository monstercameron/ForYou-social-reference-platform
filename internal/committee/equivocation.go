package committee

import (
	"sort"

	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/wire"
)

// EquivocationProofs returns the verifiable conflicting claims held for a provider line.
func (m *Monitor) EquivocationProofs(providerID string) []wire.EquivocationProof {
	line := m.Line(providerID)
	proofs := make([]wire.EquivocationProof, 0)
	seen := make(map[string]bool)
	for _, pair := range m.state.Equivocations {
		a, b := pair[0], pair[1]
		if m.Line(a.ProviderID) != line || m.Line(b.ProviderID) != line {
			continue
		}
		ids := []string{a.ClaimID, b.ClaimID}
		sort.Strings(ids)
		key := ids[0] + "\x00" + ids[1]
		if seen[key] {
			continue
		}
		predecessor := m.predecessor(a)
		proof := settlement.BuildEquivocationProof(a, b, predecessor, m.state.Successions)
		if !settlement.VerifyEquivocation(proof) {
			continue
		}
		seen[key] = true
		proofs = append(proofs, proof)
	}
	sort.Slice(proofs, func(i, j int) bool {
		a, b := proofs[i].Claims, proofs[j].Claims
		if a[0].Sequence != b[0].Sequence {
			return a[0].Sequence < b[0].Sequence
		}
		ai, bi := orderedClaimIDs(a), orderedClaimIDs(b)
		if ai[0] != bi[0] {
			return ai[0] < bi[0]
		}
		return ai[1] < bi[1]
	})
	if len(proofs) > 1000 {
		proofs = proofs[:1000]
	}
	return proofs
}

func orderedClaimIDs(claims []wire.SettlementClaim) [2]string {
	ids := [2]string{claims[0].ClaimID, claims[1].ClaimID}
	if ids[1] < ids[0] {
		ids[0], ids[1] = ids[1], ids[0]
	}
	return ids
}
