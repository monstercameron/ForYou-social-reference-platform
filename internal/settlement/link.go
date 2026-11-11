package settlement

import (
	"errors"
	"math"
	"math/big"
	"time"

	"foryou.local/foryou/internal/wire"
)

// LinkKind classifies a claim link failure by whether public objects prove the fault.
type LinkKind int

const (
	// LinkKey identifies missing succession data or a predecessor on another network.
	LinkKey LinkKind = iota + 1
	// LinkSuccession identifies a provable succession rule violation.
	LinkSuccession
	// LinkChain identifies incorrect sequence, predecessor ID, or cumulative amount.
	LinkChain
	// LinkPeriod identifies an invalid or overlapping claim period.
	LinkPeriod
)

// LinkError reports the category and detail of a claim link failure.
type LinkError struct {
	Kind   LinkKind
	Detail string
}

// Error returns the link failure detail.
func (e *LinkError) Error() string { return "settlement: " + e.Detail }

// SuccessionLookup returns a succession only when the caller has verified and accepted it.
type SuccessionLookup func(successionID string) (wire.KeySuccession, bool)

// DeclarationLookup returns a declaration only when the caller has verified it.
type DeclarationLookup func(declarationID string) (wire.RetentionDeclaration, bool)

// ErrDeclarationUnknown means the named declaration is not held by the caller.
var ErrDeclarationUnknown = errors.New("settlement: named declaration not held")

// ErrDeclarationRule means the named declaration violates the claim declaration rules.
var ErrDeclarationRule = errors.New("settlement: named declaration breaks the declaration rule")

// CheckLink validates a claim's relationship to its predecessor and any named succession.
func CheckLink(c wire.SettlementClaim, previous *wire.SettlementClaim, successions SuccessionLookup) error {
	if previous != nil && previous.Network != c.Network {
		return &LinkError{Kind: LinkKey, Detail: "predecessor is on another network"}
	}
	if err := checkClaimNetwork(c.Network); err != nil {
		return err
	}
	if previous != nil {
		if err := checkClaimNetwork(previous.Network); err != nil {
			return err
		}
	}
	if previous == nil || previous.ProviderID == c.ProviderID {
		if c.SuccessionID != nil {
			return &LinkError{Kind: LinkSuccession, Detail: "claim names succession without changing provider key"}
		}
	} else {
		if c.SuccessionID == nil {
			return &LinkError{Kind: LinkSuccession, Detail: "new provider key does not name a succession"}
		}
		succession, ok := wire.KeySuccession{}, false
		if successions != nil {
			succession, ok = successions(*c.SuccessionID)
		}
		if !ok {
			return &LinkError{Kind: LinkKey, Detail: "named succession is not held"}
		}
		effectiveAt, err := wire.ParseTime(succession.EffectiveAt)
		previousEnd, previousErr := wire.ParseTime(previous.PeriodEnd)
		claimStart, claimErr := wire.ParseTime(c.PeriodStart)
		if succession.OldID != previous.ProviderID || succession.NewID != c.ProviderID || err != nil || previousErr != nil || claimErr != nil || effectiveAt.Before(previousEnd) || effectiveAt.After(claimStart) {
			return &LinkError{Kind: LinkSuccession, Detail: "named succession does not connect the claim keys and periods"}
		}
	}
	expectedSequence := int64(1)
	var expectedPreviousID *string
	if previous != nil {
		if previous.Sequence == math.MaxInt64 {
			return &LinkError{Kind: LinkChain, Detail: "predecessor sequence overflows"}
		}
		expectedSequence = previous.Sequence + 1
		id := previous.ClaimID
		expectedPreviousID = &id
	}
	claimTotal, totalErr := wire.PositiveAmount(c.TotalBaseUnits)
	claimCumulative, cumulativeErr := wire.PositiveAmount(c.CumulativeBaseUnits)
	if totalErr != nil || cumulativeErr != nil || c.Sequence != expectedSequence || !sameOptionalClaimID(c.PreviousClaimID, expectedPreviousID) {
		return &LinkError{Kind: LinkChain, Detail: "claim sequence or predecessor fields are incorrect"}
	}
	priorCumulative := new(big.Int)
	if previous != nil {
		var err error
		priorCumulative, err = wire.PositiveAmount(previous.CumulativeBaseUnits)
		if err != nil {
			return &LinkError{Kind: LinkChain, Detail: "predecessor cumulative amount is invalid"}
		}
	}
	if claimCumulative.Cmp(new(big.Int).Add(priorCumulative, claimTotal)) != 0 {
		return &LinkError{Kind: LinkChain, Detail: "claim cumulative amount is incorrect"}
	}
	start, startErr := wire.ParseTime(c.PeriodStart)
	end, endErr := wire.ParseTime(c.PeriodEnd)
	if startErr != nil || endErr != nil || !end.After(start) || end.Sub(start) > 24*time.Hour {
		return &LinkError{Kind: LinkPeriod, Detail: "claim period is invalid or exceeds 24 hours"}
	}
	if previous != nil {
		previousEnd, err := wire.ParseTime(previous.PeriodEnd)
		if err != nil || start.Before(previousEnd) {
			return &LinkError{Kind: LinkPeriod, Detail: "claim period overlaps its predecessor"}
		}
	}
	return nil
}

// CheckDeclaration validates the declaration selected by a claim.
func CheckDeclaration(c wire.SettlementClaim, previous *wire.SettlementClaim, declarations DeclarationLookup) error {
	if previous != nil && c.DeclarationID == previous.DeclarationID {
		if c.ProviderID != previous.ProviderID {
			return ErrDeclarationRule
		}
		return nil
	}
	declaration, ok := wire.RetentionDeclaration{}, false
	if declarations != nil {
		declaration, ok = declarations(c.DeclarationID)
	}
	if !ok {
		return ErrDeclarationUnknown
	}
	minimum, err := wire.ParseTime(declaration.EffectiveAt)
	start, startErr := wire.ParseTime(c.PeriodStart)
	if err != nil || startErr != nil || declaration.ProviderID != c.ProviderID || declaration.RetentionSeconds < 31536000 || minimum.After(start) {
		return ErrDeclarationRule
	}
	if previous != nil {
		previousEnd, err := wire.ParseTime(previous.PeriodEnd)
		if err != nil || minimum.Before(previousEnd) {
			return ErrDeclarationRule
		}
	}
	return nil
}

func sameOptionalClaimID(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
