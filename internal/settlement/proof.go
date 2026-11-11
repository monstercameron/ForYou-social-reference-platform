package settlement

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"time"

	"foryou.local/foryou/internal/wire"
)

const (
	proofChainLimit     = 1000
	proofWitnessLimit   = 512
	proofAuditPathLimit = 14
	proofLeafLimit      = 10000
)

// ProofContext contains the committee policy and verified succession facts held by a verifier.
type ProofContext struct {
	// Committee is the policy monitors in force at the verifier's clock.
	Committee []string
	// PreviousCommittee is set only during the 30 days after a committee change.
	PreviousCommittee []string
	// ChangedAt is that committee change's effective_at; previous witnesses must predate it strictly.
	ChangedAt *time.Time
	// Successions returns only verified successions held by the verifier.
	Successions SuccessionLookup
}

// BuildProof assembles a receipt inclusion proof from a claim, its ordered leaves, witnesses, and later claims.
func BuildProof(c wire.SettlementClaim, leaves []wire.SettlementLeaf, index int, ws []wire.SettlementWitness, chain []wire.SettlementClaim) (wire.SettlementProof, error) {
	if len(ws) > proofWitnessLimit {
		return wire.SettlementProof{}, errors.New("settlement: witnesses exceed limit")
	}
	if len(chain) > proofChainLimit {
		return wire.SettlementProof{}, errors.New("settlement: chain exceeds limit")
	}
	if len(leaves) > proofLeafLimit || index < 0 || index >= len(leaves) {
		return wire.SettlementProof{}, errors.New("settlement: leaf index outside tree")
	}
	path, err := AuditPath(index, leaves)
	if err != nil {
		return wire.SettlementProof{}, err
	}
	if len(path) > proofAuditPathLimit {
		return wire.SettlementProof{}, errors.New("settlement: audit path exceeds limit")
	}
	proof := wire.SettlementProof{
		Claim: c, Leaf: leaves[index], LeafIndex: int64(index), AuditPath: append([]wire.AuditNode{}, path...),
		Witnesses: append([]wire.SettlementWitness{}, ws...),
	}
	if len(chain) > 0 {
		proof.Chain = append([]wire.SettlementClaim(nil), chain...)
	}
	return proof, nil
}

// VerifyProof validates receipt binding, claim links, the Merkle sum path, and one committee quorum.
func VerifyProof(r wire.SubmissionReceipt, p wire.SettlementProof, ctx ProofContext) (credit *big.Int, err error) {
	if len(p.Chain) > proofChainLimit || len(p.Witnesses) > proofWitnessLimit || len(p.AuditPath) > proofAuditPathLimit {
		return nil, errors.New("settlement: proof exceeds schema limits")
	}
	if p.LeafIndex < 0 || p.Claim.ReceiptCount < 1 || p.Claim.ReceiptCount > proofLeafLimit || p.LeafIndex >= p.Claim.ReceiptCount {
		return nil, errors.New("settlement: step 1: invalid leaf index or receipt count")
	}
	hash, hashErr := ReceiptHash(r)
	if hashErr != nil || p.Leaf.ReceiptHash != hash || p.Leaf.ScheduleID != r.ScheduleID || p.Leaf.AmountBaseUnits != r.AmountBaseUnits || p.Leaf.IssuedAt != r.IssuedAt || p.Claim.ProviderID != r.ProviderID || p.Claim.Network != r.Network {
		return nil, errors.New("settlement: step 1: receipt binding failed")
	}
	if err := VerifyClaimObject(p.Claim); err != nil {
		return nil, fmt.Errorf("settlement: step 2: verify claim: %w", err)
	}
	witnessed := p.Claim
	for _, later := range p.Chain {
		if err := VerifyClaimObject(later); err != nil {
			return nil, fmt.Errorf("settlement: step 2: verify chain claim: %w", err)
		}
		if err := CheckLink(later, &witnessed, ctx.Successions); err != nil {
			return nil, fmt.Errorf("settlement: step 2: verify chain link: %w", err)
		}
		witnessed = later
	}
	root, rootErr := RootFromPath(p.Leaf, int(p.LeafIndex), int(p.Claim.ReceiptCount), p.AuditPath)
	if rootErr != nil {
		return nil, fmt.Errorf("settlement: step 3: recompute root: %w", rootErr)
	}
	total, totalErr := wire.PositiveAmount(p.Claim.TotalBaseUnits)
	if totalErr != nil || root.Sum.Cmp(total) != 0 || hex.EncodeToString(root.Hash[:]) != p.Claim.ReceiptsRoot {
		return nil, errors.New("settlement: step 3: root or total mismatch")
	}
	current := CountWitnesses(witnessed, p.Witnesses, ctx.Committee, nil)
	if len(ctx.Committee) > 0 && Settled(current, ctx.Committee) {
		return minimumCredit(current), nil
	}
	if ctx.PreviousCommittee != nil && ctx.ChangedAt != nil {
		previous := CountWitnesses(witnessed, p.Witnesses, ctx.PreviousCommittee, ctx.ChangedAt)
		if len(ctx.PreviousCommittee) > 0 && Settled(previous, ctx.PreviousCommittee) {
			return minimumCredit(previous), nil
		}
	}
	return nil, errors.New("settlement: step 4: no committee quorum")
}

func minimumCredit(counted map[string]*big.Int) *big.Int {
	var minimum *big.Int
	for _, amount := range counted {
		if minimum == nil || amount.Cmp(minimum) < 0 {
			minimum = new(big.Int).Set(amount)
		}
	}
	return minimum
}

// ReceiptState classifies a receipt as settled only when its proof verifies.
func ReceiptState(r wire.SubmissionReceipt, p *wire.SettlementProof, ctx ProofContext) State {
	if p == nil {
		return StateUnsettled
	}
	if _, err := VerifyProof(r, *p, ctx); err != nil {
		return StateUnsettled
	}
	return StateSettled
}
