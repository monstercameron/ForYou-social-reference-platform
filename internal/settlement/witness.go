package settlement

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"math/big"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

const witnessLabel = "settlement-witness"

func witnessUnsigned(w wire.SettlementWitness) ([]byte, error) {
	return canon.EncodeWithout(w, "witness_signature")
}

// SignWitness signs the claim fields and available credit as a committee witness.
func SignWitness(key keys.PrivateKey, c wire.SettlementClaim, credit *big.Int, at time.Time) (wire.SettlementWitness, error) {
	if len(key) != ed25519.PrivateKeySize {
		return wire.SettlementWitness{}, errors.New("settlement: invalid monitor private key")
	}
	if credit == nil || credit.Sign() < 0 {
		return wire.SettlementWitness{}, errors.New("settlement: credit must be non-negative")
	}
	periodEnd, err := wire.ParseTime(c.PeriodEnd)
	if err != nil || at.Before(periodEnd) {
		return wire.SettlementWitness{}, errors.New("settlement: witness time precedes claim period end")
	}
	w := wire.SettlementWitness{
		WitnessID: keys.DIDOf(key), ClaimID: c.ClaimID, ProviderID: c.ProviderID,
		Network: c.Network, Sequence: c.Sequence, PeriodEnd: c.PeriodEnd,
		TotalBaseUnits: c.TotalBaseUnits, CumulativeBaseUnits: c.CumulativeBaseUnits,
		ReceiptsRoot: c.ReceiptsRoot, CreditBaseUnits: credit.String(), WitnessedAt: wire.FormatTime(at),
	}
	encoded, err := witnessUnsigned(w)
	if err != nil {
		return wire.SettlementWitness{}, fmt.Errorf("settlement: encode witness: %w", err)
	}
	w.WitnessSignature, err = keys.Sign(key, witnessLabel, encoded)
	if err != nil {
		return wire.SettlementWitness{}, fmt.Errorf("settlement: sign witness: %w", err)
	}
	return w, nil
}

// VerifyWitness validates a witness's canonical fields, timing and strict did:key signature.
func VerifyWitness(w wire.SettlementWitness) error {
	periodEnd, err := wire.ParseTime(w.PeriodEnd)
	if err != nil {
		return fmt.Errorf("settlement: invalid period_end: %w", err)
	}
	witnessedAt, err := wire.ParseTime(w.WitnessedAt)
	if err != nil {
		return fmt.Errorf("settlement: invalid witnessed_at: %w", err)
	}
	if witnessedAt.Before(periodEnd) {
		return errors.New("settlement: witnessed_at precedes period_end")
	}
	if _, err := wire.Amount(w.CreditBaseUnits); err != nil {
		return fmt.Errorf("settlement: invalid credit_base_units: %w", err)
	}
	encoded, err := witnessUnsigned(w)
	if err != nil {
		return fmt.Errorf("settlement: encode witness: %w", err)
	}
	if err := keys.Verify(w.WitnessID, witnessLabel, encoded, w.WitnessSignature); err != nil {
		return fmt.Errorf("settlement: verify witness signature: %w", err)
	}
	return nil
}

// WitnessMatches reports whether a witness attests to the eight specified claim fields.
func WitnessMatches(w wire.SettlementWitness, c wire.SettlementClaim) bool {
	return w.ClaimID == c.ClaimID && w.ProviderID == c.ProviderID && w.Network == c.Network &&
		w.Sequence == c.Sequence && w.PeriodEnd == c.PeriodEnd &&
		w.TotalBaseUnits == c.TotalBaseUnits && w.CumulativeBaseUnits == c.CumulativeBaseUnits &&
		w.ReceiptsRoot == c.ReceiptsRoot
}
