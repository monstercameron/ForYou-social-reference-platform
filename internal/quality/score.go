package quality

import (
	"math/big"

	"foryou.local/foryou/internal/wire"
)

// ReferenceScore returns the optional quality score in basis points.
func ReferenceScore(r wire.QualityReport) *int64 {
	if r.SampleCoverage != "complete" || r.AvailabilityAttempts < 20 || r.RetrievalAttempts < 20 {
		return nil
	}
	if r.AvailabilitySuccesses < 0 || r.AvailabilitySuccesses > r.AvailabilityAttempts || r.RetrievalPasses < 0 || r.RetrievalPasses > r.RetrievalAttempts {
		return nil
	}
	availability := basisPoints(r.AvailabilitySuccesses, r.AvailabilityAttempts)
	retrieval := basisPoints(r.RetrievalPasses, r.RetrievalAttempts)
	if retrieval.Cmp(availability) < 0 {
		availability = retrieval
	}
	score := availability.Int64()
	return &score
}

func basisPoints(successes, attempts int64) *big.Int {
	return new(big.Int).Quo(new(big.Int).Mul(big.NewInt(successes), big.NewInt(10000)), big.NewInt(attempts))
}
