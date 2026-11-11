package delegation

import (
	"errors"
	"fmt"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

const (
	// MaxDelegationBytes is the maximum canonical delegation size.
	MaxDelegationBytes = 4096
	// MaxDelegationLifetime is the longest permitted delegation validity period.
	MaxDelegationLifetime = 24 * time.Hour
)

var (
	// ErrInvalidDelegation identifies structurally or semantically invalid delegation data.
	ErrInvalidDelegation = errors.New("delegation: invalid delegation")
	// ErrBadRootSignature identifies an invalid root signature.
	ErrBadRootSignature = errors.New("delegation: invalid root signature")
	// ErrRequestExpired identifies a request whose not_after has passed.
	ErrRequestExpired = errors.New("delegation: request not_after has passed")
	// ErrWrongAuthor identifies a request author that differs from the authenticated root.
	ErrWrongAuthor = errors.New("delegation: request author is not the authenticated root")
	// ErrStale identifies stale authorization state or a storage version conflict.
	ErrStale = errors.New("delegation: stale epoch state")
	// ErrRequestConflict identifies a nonce reused for different request content.
	ErrRequestConflict = errors.New("delegation: nonce reused with different content")
	// ErrRevoked identifies a frozen, retired or revoked delegation.
	ErrRevoked = errors.New("delegation: revoked, frozen or retired")
	// ErrCapacity identifies exhausted per-author revocation capacity.
	ErrCapacity = errors.New("delegation: revocation capacity exhausted")
	// ErrUncertain identifies authorization state that cannot be trusted.
	ErrUncertain = errors.New("delegation: authorization state uncertain")
)

// Use identifies the message and time for which a delegation is checked.
type Use struct {
	// AuthorID is the root author named by the delegation.
	AuthorID string
	// SignerID is the delegated signer.
	SignerID string
	// MessageType is the baseline message type being authorized.
	MessageType string
	// ProviderID is the provider named by the receipt or admission gate.
	ProviderID string
	// At is the historical authorization time.
	At time.Time
}

// DelegationID derives the content ID of d without its ID or root signature.
func DelegationID(d wire.Delegation) (string, error) {
	b, err := canon.EncodeWithout(d, "delegation_id", "delegation_signature")
	if err != nil {
		return "", err
	}
	return keys.DigestID("fyd1:", "delegation-id", b)
}

// SignDelegation fills the content-derived ID and root signature of d.
func SignDelegation(root keys.PrivateKey, d wire.Delegation) (wire.Delegation, error) {
	d.DelegationID = ""
	d.DelegationSignature = ""
	id, err := DelegationID(d)
	if err != nil {
		return d, err
	}
	d.DelegationID = id
	b, err := canon.EncodeWithout(d, "delegation_signature")
	if err != nil {
		return d, err
	}
	d.DelegationSignature, err = keys.Sign(root, "delegation", b)
	return d, err
}

// VerifyDelegation validates d for the specified historical use.
func VerifyDelegation(d wire.Delegation, use Use) error {
	b, err := canon.Encode(d)
	if err != nil || len(b) > MaxDelegationBytes {
		return ErrInvalidDelegation
	}
	if err = checkWire("Delegation", d); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidDelegation, err)
	}
	if use.AuthorID != d.AuthorID || use.SignerID != d.SignerID || use.ProviderID != d.ProviderID || d.Epoch.AuthorID != d.AuthorID || d.Epoch.ProviderID != d.ProviderID {
		return ErrInvalidDelegation
	}
	if err = VerifyEpoch(d.Epoch); err != nil {
		return fmt.Errorf("%w: epoch", ErrInvalidDelegation)
	}
	from, e1 := wire.ParseTime(d.ValidFrom)
	until, e2 := wire.ParseTime(d.ValidUntil)
	activated, e3 := wire.ParseTime(d.Epoch.ActivatedAt)
	if e1 != nil || e2 != nil || e3 != nil || !from.Before(until) || until.Sub(from) > MaxDelegationLifetime || use.At.Before(from) || !use.At.Before(until) || activated.After(use.At) {
		return ErrInvalidDelegation
	}
	if use.MessageType != "post" {
		return ErrInvalidDelegation
	}
	matched := false
	for _, scope := range d.Scope {
		if scope == use.MessageType {
			matched = true
		}
	}
	if !matched {
		return ErrInvalidDelegation
	}
	id, err := DelegationID(d)
	if err != nil || id != d.DelegationID {
		return ErrInvalidDelegation
	}
	b, err = canon.EncodeWithout(d, "delegation_signature")
	if err != nil || keys.Verify(d.AuthorID, "delegation", b, d.DelegationSignature) != nil {
		return ErrBadRootSignature
	}
	return nil
}

// CheckIntentDelegation verifies a delegation against an intent and provider.
func CheckIntentDelegation(d wire.Delegation, i wire.Intent, providerID string, at time.Time) error {
	if i.Delegation == nil {
		return ErrInvalidDelegation
	}
	left, err := canon.Encode(*i.Delegation)
	if err != nil {
		return ErrInvalidDelegation
	}
	right, err := canon.Encode(d)
	if err != nil || string(left) != string(right) {
		return ErrInvalidDelegation
	}
	return VerifyDelegation(d, Use{AuthorID: i.AuthorID, SignerID: i.SignerID, MessageType: i.MessageType, ProviderID: providerID, At: at})
}
