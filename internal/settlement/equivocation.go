package settlement

import (
	"foryou.local/foryou/internal/succession"
	"foryou.local/foryou/internal/wire"
)

// BuildEquivocationProof assembles two claims, their predecessor, and only the successions they name.
func BuildEquivocationProof(a, b wire.SettlementClaim, predecessor *wire.SettlementClaim, successions []wire.KeySuccession) wire.EquivocationProof {
	proof := wire.EquivocationProof{Claims: []wire.SettlementClaim{a, b}, Predecessor: predecessor, Successions: []wire.KeySuccession{}}
	wanted := make(map[string]struct{}, 2)
	if a.SuccessionID != nil {
		wanted[*a.SuccessionID] = struct{}{}
	}
	if b.SuccessionID != nil {
		wanted[*b.SuccessionID] = struct{}{}
	}
	for _, candidate := range successions {
		if _, named := wanted[candidate.SuccessionID]; named && len(proof.Successions) < 2 {
			proof.Successions = append(proof.Successions, candidate)
			delete(wanted, candidate.SuccessionID)
		}
	}
	return proof
}

// VerifyEquivocation reports whether a proof establishes two valid claims on one provider chain at one sequence.
func VerifyEquivocation(p wire.EquivocationProof) bool {
	if len(p.Claims) != 2 || len(p.Successions) > 2 {
		return false
	}
	a, b := p.Claims[0], p.Claims[1]
	if VerifyClaimObject(a) != nil || VerifyClaimObject(b) != nil || a.ClaimID == b.ClaimID || a.Network != b.Network || a.Sequence != b.Sequence || !sameOptionalClaimID(a.PreviousClaimID, b.PreviousClaimID) {
		return false
	}
	verified := make(map[string]wire.KeySuccession, len(p.Successions))
	for _, item := range p.Successions {
		if succession.Verify(item) != nil {
			return false
		}
		verified[item.SuccessionID] = item
	}
	lookup := func(id string) (wire.KeySuccession, bool) {
		item, ok := verified[id]
		return item, ok
	}
	if a.Sequence == 1 {
		return p.Predecessor == nil && a.ProviderID == b.ProviderID && CheckLink(a, nil, lookup) == nil && CheckLink(b, nil, lookup) == nil
	}
	if a.Sequence < 1 || p.Predecessor == nil || VerifyClaimObject(*p.Predecessor) != nil {
		return false
	}
	if err := CheckLink(a, p.Predecessor, lookup); err != nil {
		return false
	}
	if err := CheckLink(b, p.Predecessor, lookup); err != nil {
		return false
	}
	return true
}
