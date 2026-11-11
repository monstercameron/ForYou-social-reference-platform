package settlement

import (
	"math/big"
	"time"

	"foryou.local/foryou/internal/wire"
)

// Quorum returns the number of distinct committee members required to settle a claim.
func Quorum(n int) int {
	return 2*n/3 + 1
}

// MaxFaulty returns the largest number of faulty members allowed by the trust assumption.
func MaxFaulty(n int) int {
	return (n - 1) / 3
}

// CountWitnesses returns, for each distinct committee member, the smallest credit among its valid witnesses that
// match c; when before is non-nil, only witnesses with witnessed_at earlier than *before count.
func CountWitnesses(c wire.SettlementClaim, ws []wire.SettlementWitness, members []string, before *time.Time) map[string]*big.Int {
	memberSet := make(map[string]struct{}, len(members))
	for _, member := range members {
		memberSet[member] = struct{}{}
	}
	counted := make(map[string]*big.Int)
	for _, witness := range ws {
		if _, member := memberSet[witness.WitnessID]; !member || !WitnessMatches(witness, c) || VerifyWitness(witness) != nil {
			continue
		}
		witnessedAt, err := wire.ParseTime(witness.WitnessedAt)
		if err != nil || before != nil && !witnessedAt.Before(*before) {
			continue
		}
		credit, err := wire.Amount(witness.CreditBaseUnits)
		if err != nil {
			continue
		}
		if existing, ok := counted[witness.WitnessID]; !ok || credit.Cmp(existing) < 0 {
			counted[witness.WitnessID] = credit
		}
	}
	return counted
}

// Settled reports whether distinct committee members supplied a quorum of witnesses.
func Settled(counted map[string]*big.Int, members []string) bool {
	memberSet := make(map[string]struct{}, len(members))
	for _, member := range members {
		memberSet[member] = struct{}{}
	}
	count := 0
	for member := range counted {
		if _, exists := memberSet[member]; exists {
			count++
		}
	}
	return count >= Quorum(len(memberSet))
}
