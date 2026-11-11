package settlement

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

// ErrClaimSignature identifies a claim with an incorrect ID or provider signature.
var ErrClaimSignature = errors.New("settlement: bad claim_id or provider signature")

// ClaimInput contains the claim fields supplied by a provider's settlement builder.
type ClaimInput struct {
	Network                string
	Previous               *wire.SettlementClaim
	PeriodStart, PeriodEnd time.Time
	DeclarationID          string
	SuccessionID           *string
	ReceiptCount           int
	Total                  *big.Int
	ReceiptsRoot           string
}

// ClaimID computes the typed ID of a claim without its ID and signature fields.
func ClaimID(c wire.SettlementClaim) (string, error) {
	canonical, err := canon.EncodeWithout(c, "claim_id", "provider_signature")
	if err != nil {
		return "", err
	}
	return keys.DigestID("fysc2:", "settlement-claim-id", canonical)
}

// BuildClaim constructs and signs a settlement claim from its root and accounting totals.
func BuildClaim(key keys.PrivateKey, in ClaimInput) (wire.SettlementClaim, error) {
	if err := checkClaimNetwork(in.Network); err != nil {
		return wire.SettlementClaim{}, err
	}
	if in.ReceiptCount < 1 || in.ReceiptCount > 10000 {
		return wire.SettlementClaim{}, errors.New("settlement: receipt count outside 1..10000")
	}
	if in.Total == nil {
		return wire.SettlementClaim{}, errors.New("settlement: total must be positive")
	}
	if _, err := wire.PositiveAmount(in.Total.String()); err != nil {
		return wire.SettlementClaim{}, fmt.Errorf("settlement: invalid total amount: %w", err)
	}
	start, end := in.PeriodStart.UTC().Truncate(time.Second), in.PeriodEnd.UTC().Truncate(time.Second)
	if span := end.Sub(start); span <= 0 || span > 24*time.Hour {
		return wire.SettlementClaim{}, errors.New("settlement: canonical claim period outside (0, 24h]")
	}
	cumulative := new(big.Int).Set(in.Total)
	sequence := int64(1)
	var previousID *string
	if in.Previous != nil {
		if in.Previous.Sequence == math.MaxInt64 {
			return wire.SettlementClaim{}, errors.New("settlement: claim sequence overflow")
		}
		previousTotal, err := wire.PositiveAmount(in.Previous.CumulativeBaseUnits)
		if err != nil {
			return wire.SettlementClaim{}, fmt.Errorf("settlement: invalid previous cumulative amount: %w", err)
		}
		cumulative.Add(cumulative, previousTotal)
		sequence = in.Previous.Sequence + 1
		id := in.Previous.ClaimID
		previousID = &id
	}
	claim := wire.SettlementClaim{
		ProviderID:          keys.DIDOf(key),
		Network:             in.Network,
		Sequence:            sequence,
		PreviousClaimID:     previousID,
		PeriodStart:         wire.FormatTime(start),
		PeriodEnd:           wire.FormatTime(end),
		DeclarationID:       in.DeclarationID,
		SuccessionID:        in.SuccessionID,
		ReceiptCount:        int64(in.ReceiptCount),
		TotalBaseUnits:      in.Total.String(),
		CumulativeBaseUnits: cumulative.String(),
		ReceiptsRoot:        in.ReceiptsRoot,
	}
	claimID, err := ClaimID(claim)
	if err != nil {
		return wire.SettlementClaim{}, err
	}
	claim.ClaimID = claimID
	canonical, err := canon.EncodeWithout(claim, "provider_signature")
	if err != nil {
		return wire.SettlementClaim{}, err
	}
	claim.ProviderSignature, err = keys.Sign(key, "settlement-claim", canonical)
	if err != nil {
		return wire.SettlementClaim{}, err
	}
	return claim, nil
}

func checkClaimNetwork(network string) error {
	if network != wire.BitcoinMainnet && network != wire.EthereumMainnet {
		return unsupportedClaimNetwork(network)
	}
	return nil
}

func unsupportedClaimNetwork(network string) error {
	return fmt.Errorf("settlement: unsupported network %q", network)
}

// VerifyClaimObject verifies the claim ID and provider signature under strict Ed25519.
func VerifyClaimObject(c wire.SettlementClaim) error {
	id, err := ClaimID(c)
	if err != nil || id != c.ClaimID {
		return ErrClaimSignature
	}
	canonical, err := canon.EncodeWithout(c, "provider_signature")
	if err != nil || keys.Verify(c.ProviderID, "settlement-claim", canonical, c.ProviderSignature) != nil {
		return ErrClaimSignature
	}
	return nil
}
