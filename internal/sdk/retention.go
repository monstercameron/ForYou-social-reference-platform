package sdk

import (
	"time"

	"foryou.local/foryou/internal/retention"
	"foryou.local/foryou/internal/wire"
)

// RetentionView describes the provider's claim-bound retention period for one receipt.
type RetentionView struct {
	DeclarationID    string
	RetentionSeconds int64
	RetainUntil      time.Time
}

// RetainUntil verifies the named declaration and computes retention from an already verified receipt and claim. Readers can rely on a period only once the claim naming it exists; the Lifetime Vault keeps settled records with no scheduled expiry.
func RetainUntil(r wire.SubmissionReceipt, c wire.SettlementClaim, ds []wire.RetentionDeclaration) (RetentionView, error) {
	if r.ProviderID != c.ProviderID {
		return RetentionView{}, retention.ErrProviderMismatch
	}
	var named *wire.RetentionDeclaration
	for i := range ds {
		if ds[i].DeclarationID == c.DeclarationID {
			named = &ds[i]
			break
		}
	}
	if named == nil {
		return RetentionView{}, retention.ErrUnknownDeclaration
	}
	if named.RetentionSeconds < retention.MinSeconds {
		return RetentionView{}, retention.ErrBelowMinimum
	}
	issued, err := wire.ParseTime(r.IssuedAt)
	if err != nil {
		return RetentionView{}, err
	}
	until, err := retention.RetainUntil(issued, c.ProviderID, c.DeclarationID, ds)
	if err != nil {
		return RetentionView{}, err
	}
	return RetentionView{DeclarationID: named.DeclarationID, RetentionSeconds: named.RetentionSeconds, RetainUntil: until}, nil
}
