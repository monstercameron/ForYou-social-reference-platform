package provider

import (
	"errors"
	"net/http"
	"time"

	"foryou.local/foryou/internal/apierr"
	"foryou.local/foryou/internal/distribution"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/wire"
)

const (
	settleChainLimit   = 1000
	settleWitnessLimit = 512
)

var errSettleNotLocated = errors.New("provider settlement: receipt is not in a published claim")

func settleRegisterProof(s *Service, mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/settlements/{receipt_id}", func(w http.ResponseWriter, r *http.Request) {
		settleProofRoute(w, r, s)
	})
}

func settleProofRoute(w http.ResponseWriter, r *http.Request, s *Service) {
	w = settleHead(w, r)
	receiptID := r.PathValue("receipt_id")
	if !settleReceiptID(receiptID) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if !s.LimitRead(w, r, ReadSettlement) {
		return
	}
	receipt, err := settleLocatedReceipt(s, receiptID)
	if errors.Is(err, errSettleNotLocated) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", apierr.DefaultRetryAfterSeconds)
		return
	}
	restriction, err := s.Restriction(receipt.MessageID)
	if err != nil {
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", apierr.DefaultRetryAfterSeconds)
		return
	}
	switch restriction {
	case Unrestricted:
	case Concealed:
		writeError(w, http.StatusNotFound, "not_found")
		return
	case PolicyRestricted, LegalRestricted, Restricted:
		writeError(w, http.StatusForbidden, string(restriction))
		return
	default:
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", apierr.DefaultRetryAfterSeconds)
		return
	}
	proof, err := settleBuildProof(s, receipt)
	if errors.Is(err, errSettleNotLocated) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", apierr.DefaultRetryAfterSeconds)
		return
	}
	body, err := wire.Encode("SettlementProof", proof)
	if err != nil {
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", apierr.DefaultRetryAfterSeconds)
		return
	}
	writeCanonical(w, http.StatusOK, body, "no-store")
}

// settleLocatedReceipt returns the provider's own receipt for an ID that a published
// claim holds, searching only the ledger of that claim's network.
func settleLocatedReceipt(s *Service, receiptID string) (wire.SubmissionReceipt, error) {
	claimID, _, located, err := s.Settlement().Locate(receiptID)
	if err != nil {
		return wire.SubmissionReceipt{}, err
	}
	if !located {
		return wire.SubmissionReceipt{}, errSettleNotLocated
	}
	claim, ok, err := s.Settlement().Claim(claimID)
	if err != nil {
		return wire.SubmissionReceipt{}, err
	}
	if !ok {
		return wire.SubmissionReceipt{}, errSettleNotLocated
	}
	const page = 1000
	for after := uint64(0); ; {
		entries := s.Ledger.Receipts(claim.Network, after, page)
		for _, entry := range entries {
			if entry.Receipt.ReceiptID == receiptID {
				return entry.Receipt, nil
			}
		}
		if len(entries) < page {
			return wire.SubmissionReceipt{}, errSettleNotLocated
		}
		after = entries[len(entries)-1].Sequence
	}
}

// settleBuildProof assembles the proof for a receipt only when the stored leaf at its
// claimed index binds that exact receipt. It returns the first proof that settles under
// the provider's committee view: the claim's own witnesses, or those of the nearest later
// claim within the chain limit. Otherwise it returns the claim with its own witnesses.
func settleBuildProof(s *Service, receipt wire.SubmissionReceipt) (wire.SettlementProof, error) {
	claimID, index, located, err := s.Settlement().Locate(receipt.ReceiptID)
	if err != nil {
		return wire.SettlementProof{}, err
	}
	if !located {
		return wire.SettlementProof{}, errSettleNotLocated
	}
	claim, ok, err := s.Settlement().Claim(claimID)
	if err != nil {
		return wire.SettlementProof{}, err
	}
	if !ok || claim.Network != receipt.Network || index < 0 || index >= int(claim.ReceiptCount) {
		return wire.SettlementProof{}, errSettleNotLocated
	}
	leaves, err := s.Settlement().Leaves(claimID, 0, int(claim.ReceiptCount))
	if err != nil {
		return wire.SettlementProof{}, err
	}
	if len(leaves) != int(claim.ReceiptCount) {
		return wire.SettlementProof{}, errSettleNotLocated
	}
	hash, err := settlement.ReceiptHash(receipt)
	if err != nil {
		return wire.SettlementProof{}, err
	}
	leaf := leaves[index]
	if leaf.ReceiptHash != hash || leaf.ScheduleID != receipt.ScheduleID || leaf.AmountBaseUnits != receipt.AmountBaseUnits || leaf.IssuedAt != receipt.IssuedAt {
		return wire.SettlementProof{}, errSettleNotLocated
	}
	witnesses, err := settleWitnesses(s, claimID)
	if err != nil {
		return wire.SettlementProof{}, err
	}
	own, err := settlement.BuildProof(claim, leaves, index, witnesses, nil)
	if err != nil {
		return wire.SettlementProof{}, err
	}
	ctx := settleProofContext(s, append([]wire.SettlementClaim{claim}, own.Chain...))
	if settleHasQuorum(claim, witnesses, ctx) {
		if _, err := settlement.VerifyProof(receipt, own, ctx); err == nil {
			return own, nil
		}
	}
	later, err := s.Settlement().Claims(claim.Network, claim.Sequence, settleChainLimit)
	if err != nil {
		return wire.SettlementProof{}, err
	}
	for i, candidate := range later {
		candidateWitnesses, err := settleWitnesses(s, candidate.ClaimID)
		if err != nil {
			return wire.SettlementProof{}, err
		}
		chain := later[:i+1]
		ctx := settleProofContext(s, append([]wire.SettlementClaim{claim}, chain...))
		if !settleHasQuorum(candidate, candidateWitnesses, ctx) {
			continue
		}
		linked, err := settlement.BuildProof(claim, leaves, index, candidateWitnesses, chain)
		if err != nil {
			return wire.SettlementProof{}, err
		}
		if _, err := settlement.VerifyProof(receipt, linked, ctx); err == nil {
			return linked, nil
		}
	}
	return own, nil
}

func settleWitnesses(s *Service, claimID string) ([]wire.SettlementWitness, error) {
	witnesses, err := s.Settlement().Witnesses(claimID)
	if err != nil {
		return nil, err
	}
	if len(witnesses) > settleWitnessLimit {
		return nil, errors.New("provider settlement: witness limit exceeded")
	}
	return witnesses, nil
}

// settleHasQuorum is the committee count of verification step 4, checked before the
// whole proof is verified so that a chain search verifies at most one candidate fully.
func settleHasQuorum(witnessed wire.SettlementClaim, witnesses []wire.SettlementWitness, ctx settlement.ProofContext) bool {
	if len(ctx.Committee) > 0 && settlement.Settled(settlement.CountWitnesses(witnessed, witnesses, ctx.Committee, nil), ctx.Committee) {
		return true
	}
	return ctx.ChangedAt != nil && len(ctx.PreviousCommittee) > 0 &&
		settlement.Settled(settlement.CountWitnesses(witnessed, witnesses, ctx.PreviousCommittee, ctx.ChangedAt), ctx.PreviousCommittee)
}

// settleProofContext builds the committee view the sdk verifier uses: the policy in
// force at the provider clock and, during the 30 days after it took effect, the policy
// before it. Successions are those the provider verified for the claims' keys.
func settleProofContext(s *Service, claims []wire.SettlementClaim) settlement.ProofContext {
	now := s.Now()
	policies := s.Policies()
	byID := make(map[string]wire.KeySuccession)
	for i := 1; i < len(claims); i++ {
		if claims[i].SuccessionID == nil {
			continue
		}
		for _, succession := range s.Successions.ForOld(claims[i-1].ProviderID) {
			if succession.SuccessionID == *claims[i].SuccessionID {
				byID[succession.SuccessionID] = succession
				break
			}
		}
	}
	ctx := settlement.ProofContext{Successions: func(id string) (wire.KeySuccession, bool) {
		succession, ok := byID[id]
		return succession, ok
	}}
	policy, ok := distribution.InForce(policies, now)
	if !ok {
		return ctx
	}
	ctx.Committee = append([]string(nil), policy.Monitors...)
	for i, candidate := range policies {
		if candidate.PolicyID != policy.PolicyID {
			continue
		}
		changedAt, err := wire.ParseTime(policy.EffectiveAt)
		if i > 0 && err == nil && now.Before(changedAt.Add(30*24*time.Hour)) {
			ctx.PreviousCommittee = append([]string(nil), policies[i-1].Monitors...)
			ctx.ChangedAt = &changedAt
		}
		break
	}
	return ctx
}

// settleReceiptIsSettled reports whether a proof settles the receipt under the
// provider's committee view.
func settleReceiptIsSettled(s *Service, receipt wire.SubmissionReceipt, proof wire.SettlementProof) bool {
	ctx := settleProofContext(s, append([]wire.SettlementClaim{proof.Claim}, proof.Chain...))
	_, err := settlement.VerifyProof(receipt, proof, ctx)
	return err == nil
}
