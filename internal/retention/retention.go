package retention

import (
	"errors"
	"sort"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

var errDeclarationIDMismatch = errors.New("retention: declaration ID mismatch")

// MinSeconds is the protocol's minimum provider retention period.
const MinSeconds int64 = 31536000

var (
	// ErrBelowMinimum reports a declaration below MinSeconds.
	ErrBelowMinimum = errors.New("retention: below one year")
	// ErrUnknownDeclaration reports a claim naming no available declaration.
	ErrUnknownDeclaration = errors.New("retention: unknown declaration")
	// ErrProviderMismatch reports a declaration signed by another provider.
	ErrProviderMismatch = errors.New("retention: declaration signed by another provider")
	// ErrNoFittingDeclaration reports that no declaration can be named by the claim.
	ErrNoFittingDeclaration = errors.New("retention: no declaration fits the claim")
)

// Link is the information needed from a provider's previous claim.
type Link struct {
	DeclarationID string
	ProviderID    string
	PeriodEnd     time.Time
}

// ID returns the content-derived identifier of d.
func ID(d wire.RetentionDeclaration) (string, error) {
	canonical, err := canon.EncodeWithout(d, "declaration_id", "provider_signature")
	if err != nil {
		return "", err
	}
	return keys.DigestID("fyrd2:", "retention-declaration-id", canonical)
}

// Declare builds and signs a provider retention commitment.
func Declare(key keys.PrivateKey, seconds int64, payout wire.PayoutRecipients, effectiveAt time.Time) (wire.RetentionDeclaration, error) {
	if seconds < MinSeconds {
		return wire.RetentionDeclaration{}, ErrBelowMinimum
	}
	declaration := wire.RetentionDeclaration{
		ProviderID:       keys.DIDOf(key),
		RetentionSeconds: seconds,
		PayoutRecipients: payout,
		EffectiveAt:      wire.FormatTime(effectiveAt),
	}
	id, err := ID(declaration)
	if err != nil {
		return wire.RetentionDeclaration{}, err
	}
	declaration.DeclarationID = id
	canonical, err := canon.EncodeWithout(declaration, "provider_signature")
	if err != nil {
		return wire.RetentionDeclaration{}, err
	}
	declaration.ProviderSignature, err = keys.Sign(key, "retention-declaration", canonical)
	if err != nil {
		return wire.RetentionDeclaration{}, err
	}
	if err := Verify(declaration); err != nil {
		return wire.RetentionDeclaration{}, err
	}
	return declaration, nil
}

// Verify checks the schema, minimum retention, content ID and strict provider signature.
func Verify(declaration wire.RetentionDeclaration) error {
	encoded, err := canon.Encode(declaration)
	if err != nil {
		return err
	}
	value, err := canon.Parse(encoded, canon.MessageLimits)
	if err != nil {
		return err
	}
	if err := wire.Check("RetentionDeclaration", value); err != nil {
		return err
	}
	if declaration.RetentionSeconds < MinSeconds {
		return ErrBelowMinimum
	}
	id, err := ID(declaration)
	if err != nil {
		return err
	}
	if declaration.DeclarationID != id {
		return errDeclarationIDMismatch
	}
	canonical, err := canon.EncodeWithout(declaration, "provider_signature")
	if err != nil {
		return err
	}
	if err := keys.Verify(declaration.ProviderID, "retention-declaration", canonical, declaration.ProviderSignature); err != nil {
		return err
	}
	return nil
}

// RetainUntil uses only the declaration named by the settled claim; until that claim exists, the provider keeps the record.
func RetainUntil(issuedAt time.Time, claimProviderID, claimDeclarationID string, declarations []wire.RetentionDeclaration) (time.Time, error) {
	declaration, ok := declarationByID(declarations, claimDeclarationID)
	if !ok {
		return time.Time{}, ErrUnknownDeclaration
	}
	if declaration.ProviderID != claimProviderID {
		return time.Time{}, ErrProviderMismatch
	}
	if err := Verify(declaration); err != nil {
		return time.Time{}, err
	}
	return time.Unix(issuedAt.Unix()+declaration.RetentionSeconds, 0).UTC(), nil
}

// ForClaim selects the newest verified declaration allowed by the settlement claim link rule.
func ForClaim(declarations []wire.RetentionDeclaration, providerID string, periodStart time.Time, previous *Link) (wire.RetentionDeclaration, error) {
	var selected wire.RetentionDeclaration
	var selectedAt time.Time
	found := false
	for _, declaration := range declarations {
		if declaration.ProviderID != providerID || Verify(declaration) != nil {
			continue
		}
		effectiveAt, err := wire.ParseTime(declaration.EffectiveAt)
		if err != nil || effectiveAt.After(periodStart) {
			continue
		}
		if previous != nil && effectiveAt.Before(previous.PeriodEnd) {
			continue
		}
		if !found || effectiveAt.After(selectedAt) || effectiveAt.Equal(selectedAt) && declaration.DeclarationID < selected.DeclarationID {
			selected = declaration
			selectedAt = effectiveAt
			found = true
		}
	}
	if found {
		return selected, nil
	}
	if previous != nil && previous.ProviderID == providerID {
		if declaration, ok := declarationByID(declarations, previous.DeclarationID); ok && declaration.ProviderID == providerID && Verify(declaration) == nil {
			return declaration, nil
		}
	}
	return wire.RetentionDeclaration{}, ErrNoFittingDeclaration
}

// NewestFirst returns declarations ordered as GET /v1/retention specifies.
func NewestFirst(declarations []wire.RetentionDeclaration) []wire.RetentionDeclaration {
	ordered := append([]wire.RetentionDeclaration(nil), declarations...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].EffectiveAt != ordered[j].EffectiveAt {
			return ordered[i].EffectiveAt > ordered[j].EffectiveAt
		}
		return ordered[i].DeclarationID < ordered[j].DeclarationID
	})
	return ordered
}

// Expired reports whether the retain_until instant has arrived.
func Expired(retainUntil, now time.Time) bool {
	return !now.Before(retainUntil)
}

func declarationByID(declarations []wire.RetentionDeclaration, id string) (wire.RetentionDeclaration, bool) {
	for _, declaration := range declarations {
		if declaration.DeclarationID == id {
			return declaration, true
		}
	}
	return wire.RetentionDeclaration{}, false
}
