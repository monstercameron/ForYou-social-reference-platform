package succession

import (
	"errors"
	"fmt"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

var (
	// ErrInvalid identifies a malformed or unverifiable succession.
	ErrInvalid = errors.New("succession: invalid")
	// ErrConflict identifies a provider succession that conflicts with its accepted line.
	ErrConflict = errors.New("succession: conflicting succession")
	// ErrSuccessorSignedClaims identifies a successor that signed a settlement claim too early.
	ErrSuccessorSignedClaims = errors.New("succession: successor key already signed settlement claims")
	// ErrRegistryFull identifies an old key that reached its held-succession limit.
	ErrRegistryFull = errors.New("succession: registry full for this key")
)

type successionCore struct {
	OldID       string `json:"old_id"`
	NewID       string `json:"new_id"`
	EffectiveAt string `json:"effective_at"`
}

func core(oldID, newID, effectiveAt string) successionCore {
	return successionCore{OldID: oldID, NewID: newID, EffectiveAt: effectiveAt}
}

// ID computes the content ID for a key succession at effectiveAt's UTC second.
func ID(oldID, newID string, effectiveAt time.Time) (string, error) {
	effective := wire.FormatTime(effectiveAt)
	if _, err := wire.ParseTime(effective); err != nil {
		return "", fmt.Errorf("%w: effective_at", ErrInvalid)
	}
	return successionID(oldID, newID, effective)
}

func successionID(oldID, newID, effectiveAt string) (string, error) {
	encoded, err := canon.Encode(core(oldID, newID, effectiveAt))
	if err != nil {
		return "", fmt.Errorf("%w: canonical input: %v", ErrInvalid, err)
	}
	id, err := keys.DigestID("fyks2:", "key-succession-id", encoded)
	if err != nil {
		return "", fmt.Errorf("%w: ID: %v", ErrInvalid, err)
	}
	return id, nil
}

// New creates a succession signed by both the old and new keys.
func New(oldKey, newKey keys.PrivateKey, effectiveAt time.Time) (wire.KeySuccession, error) {
	oldID, newID := keys.DIDOf(oldKey), keys.DIDOf(newKey)
	effective := wire.FormatTime(effectiveAt)
	if oldID == "" || newID == "" || oldID == newID {
		return wire.KeySuccession{}, ErrInvalid
	}
	if _, err := wire.ParseTime(effective); err != nil {
		return wire.KeySuccession{}, fmt.Errorf("%w: effective_at", ErrInvalid)
	}
	id, err := ID(oldID, newID, effectiveAt)
	if err != nil {
		return wire.KeySuccession{}, err
	}
	encoded, err := canon.Encode(core(oldID, newID, effective))
	if err != nil {
		return wire.KeySuccession{}, fmt.Errorf("%w: canonical input: %v", ErrInvalid, err)
	}
	oldSignature, err := keys.Sign(oldKey, "key-succession", encoded)
	if err != nil {
		return wire.KeySuccession{}, fmt.Errorf("%w: old signature: %v", ErrInvalid, err)
	}
	newSignature, err := keys.Sign(newKey, "key-succession-accept", encoded)
	if err != nil {
		return wire.KeySuccession{}, fmt.Errorf("%w: new signature: %v", ErrInvalid, err)
	}
	return wire.KeySuccession{
		SuccessionID: id,
		OldID:        oldID,
		NewID:        newID,
		EffectiveAt:  effective,
		OldSignature: oldSignature,
		NewSignature: newSignature,
	}, nil
}

// Verify checks the closed wire shape, identifiers, content ID and both strict signatures.
func Verify(s wire.KeySuccession) error {
	if s.SuccessionID == "" || s.OldID == "" || s.NewID == "" || s.EffectiveAt == "" || s.OldSignature == "" || s.NewSignature == "" || s.OldID == s.NewID {
		return ErrInvalid
	}
	oldPublicKey, err := keys.ParseDID(s.OldID)
	if err != nil || keys.DID(oldPublicKey) != s.OldID {
		return fmt.Errorf("%w: old_id", ErrInvalid)
	}
	newPublicKey, err := keys.ParseDID(s.NewID)
	if err != nil || keys.DID(newPublicKey) != s.NewID {
		return fmt.Errorf("%w: new_id", ErrInvalid)
	}
	effectiveAt, err := wire.ParseTime(s.EffectiveAt)
	if err != nil {
		return fmt.Errorf("%w: effective_at", ErrInvalid)
	}
	encoded, err := canon.Encode(core(s.OldID, s.NewID, s.EffectiveAt))
	if err != nil {
		return fmt.Errorf("%w: canonical input: %v", ErrInvalid, err)
	}
	id, err := ID(s.OldID, s.NewID, effectiveAt)
	if err != nil || id != s.SuccessionID {
		return fmt.Errorf("%w: succession_id", ErrInvalid)
	}
	if err := keys.Verify(s.OldID, "key-succession", encoded, s.OldSignature); err != nil {
		return fmt.Errorf("%w: old_signature", ErrInvalid)
	}
	if err := keys.Verify(s.NewID, "key-succession-accept", encoded, s.NewSignature); err != nil {
		return fmt.Errorf("%w: new_signature", ErrInvalid)
	}
	return nil
}

// SignerAt expects s to be a verified succession and returns the key required for an object's signed time.
// Callers must not apply this provider-key rule to submission receipts or settlement claims.
func SignerAt(s wire.KeySuccession, signedAt time.Time) string {
	effectiveAt, err := wire.ParseTime(s.EffectiveAt)
	if err != nil || signedAt.Before(effectiveAt) {
		return s.OldID
	}
	return s.NewID
}
