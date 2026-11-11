package settlement

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"sort"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

// Reason is a closed settlement refusal reason.
type Reason string

// The refusal reasons a monitor may give; docs/03-economics.md allows no other.
const (
	ReasonBadSignature       Reason = "bad_signature"
	ReasonBadChain           Reason = "bad_chain"
	ReasonBadPeriod          Reason = "bad_period"
	ReasonBadLeaf            Reason = "bad_leaf"
	ReasonDuplicateReceipt   Reason = "duplicate_receipt"
	ReasonBadTotals          Reason = "bad_totals"
	ReasonUnfunded           Reason = "unfunded"
	ReasonUnknownProviderKey Reason = "unknown_provider_key"
)

const (
	refusalLabel   = "settlement-refusal"
	holdLabel      = "settlement-hold"
	holdClaimLimit = 64
)

func validReason(reason string) bool {
	switch Reason(reason) {
	case ReasonBadSignature, ReasonBadChain, ReasonBadPeriod, ReasonBadLeaf,
		ReasonDuplicateReceipt, ReasonBadTotals, ReasonUnfunded, ReasonUnknownProviderKey:
		return true
	default:
		return false
	}
}

func refusalUnsigned(r wire.SettlementRefusal) ([]byte, error) {
	return canon.EncodeWithout(r, "witness_signature")
}

func holdUnsigned(h wire.SettlementHold) ([]byte, error) {
	return canon.EncodeWithout(h, "witness_signature")
}

// SignRefusal signs a refusal with one of the protocol's defined reasons.
func SignRefusal(key keys.PrivateKey, c wire.SettlementClaim, reason Reason, at time.Time) (wire.SettlementRefusal, error) {
	if len(key) != ed25519.PrivateKeySize {
		return wire.SettlementRefusal{}, errors.New("settlement: invalid monitor private key")
	}
	if !validReason(string(reason)) {
		return wire.SettlementRefusal{}, errors.New("settlement: invalid refusal reason")
	}
	r := wire.SettlementRefusal{
		WitnessID: keys.DIDOf(key), ClaimID: c.ClaimID, ProviderID: c.ProviderID,
		Network: c.Network, Sequence: c.Sequence, Reason: string(reason), RefusedAt: wire.FormatTime(at),
	}
	encoded, err := refusalUnsigned(r)
	if err != nil {
		return wire.SettlementRefusal{}, fmt.Errorf("settlement: encode refusal: %w", err)
	}
	r.WitnessSignature, err = keys.Sign(key, refusalLabel, encoded)
	if err != nil {
		return wire.SettlementRefusal{}, fmt.Errorf("settlement: sign refusal: %w", err)
	}
	return r, nil
}

// VerifyRefusal validates the refusal reason, timestamp and strict did:key signature.
func VerifyRefusal(r wire.SettlementRefusal) error {
	if !validReason(r.Reason) {
		return errors.New("settlement: invalid refusal reason")
	}
	if _, err := wire.ParseTime(r.RefusedAt); err != nil {
		return fmt.Errorf("settlement: invalid refused_at: %w", err)
	}
	encoded, err := refusalUnsigned(r)
	if err != nil {
		return fmt.Errorf("settlement: encode refusal: %w", err)
	}
	if err := keys.Verify(r.WitnessID, refusalLabel, encoded, r.WitnessSignature); err != nil {
		return fmt.Errorf("settlement: verify refusal signature: %w", err)
	}
	return nil
}

// SignHold signs a hold after sorting its distinct claim IDs.
func SignHold(key keys.PrivateKey, providerID, network string, sequence int64, claimIDs []string, at time.Time) (wire.SettlementHold, error) {
	if len(key) != ed25519.PrivateKeySize {
		return wire.SettlementHold{}, errors.New("settlement: invalid monitor private key")
	}
	if err := validateClaimIDs(claimIDs); err != nil {
		return wire.SettlementHold{}, err
	}
	sortedIDs := append([]string(nil), claimIDs...)
	sort.Strings(sortedIDs)
	h := wire.SettlementHold{
		WitnessID: keys.DIDOf(key), ProviderID: providerID, Network: network,
		Sequence: sequence, ClaimIDs: sortedIDs, HeldAt: wire.FormatTime(at),
	}
	encoded, err := holdUnsigned(h)
	if err != nil {
		return wire.SettlementHold{}, fmt.Errorf("settlement: encode hold: %w", err)
	}
	h.WitnessSignature, err = keys.Sign(key, holdLabel, encoded)
	if err != nil {
		return wire.SettlementHold{}, fmt.Errorf("settlement: sign hold: %w", err)
	}
	return h, nil
}

func validateClaimIDs(ids []string) error {
	if len(ids) == 0 || len(ids) > holdClaimLimit {
		return fmt.Errorf("settlement: claim_ids must contain 1 to %d IDs", holdClaimLimit)
	}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, exists := seen[id]; exists {
			return errors.New("settlement: claim_ids must be distinct")
		}
		seen[id] = struct{}{}
	}
	return nil
}

// VerifyHold validates the hold's claim IDs, timestamp and strict did:key signature.
func VerifyHold(h wire.SettlementHold) error {
	if err := validateClaimIDs(h.ClaimIDs); err != nil {
		return err
	}
	if _, err := wire.ParseTime(h.HeldAt); err != nil {
		return fmt.Errorf("settlement: invalid held_at: %w", err)
	}
	encoded, err := holdUnsigned(h)
	if err != nil {
		return fmt.Errorf("settlement: encode hold: %w", err)
	}
	if err := keys.Verify(h.WitnessID, holdLabel, encoded, h.WitnessSignature); err != nil {
		return fmt.Errorf("settlement: verify hold signature: %w", err)
	}
	return nil
}
