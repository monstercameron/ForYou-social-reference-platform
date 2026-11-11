package settlement

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/big"
	"sort"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/wire"
)

var (
	// ErrLeafSet reports an empty, oversized, unsorted, or duplicate leaf set.
	ErrLeafSet = errors.New("settlement: leaves not 1 to 10000 in strictly ascending receipt_hash order")
	// ErrLeafPeriod reports a leaf issued outside its claim period.
	ErrLeafPeriod = errors.New("settlement: leaf issued outside the claim period")
	// ErrScheduleUnknown reports that the caller does not hold the leaf's schedule.
	ErrScheduleUnknown = errors.New("settlement: leaf schedule not held")
	// ErrLeafFee reports a schedule or amount that cannot support the leaf fee.
	ErrLeafFee = errors.New("settlement: leaf schedule not in force or amount not a possible fee")
	// ErrTotals reports totals that do not match the leaf set.
	ErrTotals = errors.New("settlement: receipt_count, total_base_units or receipts_root does not match the leaves")
)

// Schedule contains the fee values needed to validate a leaf.
type Schedule struct {
	BaseFee, PerKiBFee *big.Int
}

// Schedules supplies the fee schedules held by a caller.
type Schedules interface {
	// Lookup reports the schedule the caller holds; false means it has not been fetched.
	Lookup(scheduleID string) (Schedule, bool)
	// InForceWithin reports whether the schedule was in force for network at `at` or at some instant in the 3,600 seconds before it.
	InForceWithin(scheduleID, network string, at time.Time) bool
}

// ReceiptHash returns the lowercase SHA-256 digest of a receipt's JCS bytes.
func ReceiptHash(receipt wire.SubmissionReceipt) (string, error) {
	canonical, err := canon.Encode(receipt)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(canonical)
	return hex.EncodeToString(hash[:]), nil
}

// LeafOf derives the settlement leaf committed by a receipt.
func LeafOf(receipt wire.SubmissionReceipt) (wire.SettlementLeaf, error) {
	hash, err := ReceiptHash(receipt)
	if err != nil {
		return wire.SettlementLeaf{}, err
	}
	return wire.SettlementLeaf{
		ReceiptHash:     hash,
		ScheduleID:      receipt.ScheduleID,
		AmountBaseUnits: receipt.AmountBaseUnits,
		IssuedAt:        receipt.IssuedAt,
	}, nil
}

// SortLeaves returns a copy ordered by receipt hash.
func SortLeaves(leaves []wire.SettlementLeaf) []wire.SettlementLeaf {
	sorted := append([]wire.SettlementLeaf(nil), leaves...)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].ReceiptHash < sorted[j].ReceiptHash
	})
	return sorted
}

// PossibleFee reports whether amount is base fee plus 1 to 1,024 KiB charges.
func PossibleFee(schedule Schedule, amount *big.Int) bool {
	if schedule.BaseFee == nil || schedule.PerKiBFee == nil || amount == nil || schedule.BaseFee.Sign() < 0 || schedule.PerKiBFee.Sign() < 0 || amount.Sign() < 0 {
		return false
	}
	if schedule.PerKiBFee.Sign() == 0 {
		return amount.Cmp(schedule.BaseFee) == 0
	}
	delta := new(big.Int).Sub(amount, schedule.BaseFee)
	if delta.Sign() <= 0 {
		return false
	}
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(delta, schedule.PerKiBFee, remainder)
	return remainder.Sign() == 0 && quotient.Sign() >= 1 && quotient.Cmp(big.NewInt(1024)) <= 0
}

// CheckLeafSet enforces the claim's size and receipt-hash ordering rules.
func CheckLeafSet(leaves []wire.SettlementLeaf) error {
	if len(leaves) < 1 || len(leaves) > 10000 {
		return ErrLeafSet
	}
	for index := 1; index < len(leaves); index++ {
		if leaves[index-1].ReceiptHash >= leaves[index].ReceiptHash {
			return ErrLeafSet
		}
	}
	return nil
}

// CheckLeaf checks the period, held schedule, schedule validity window, and possible fee.
func CheckLeaf(claim wire.SettlementClaim, leaf wire.SettlementLeaf, schedules Schedules) error {
	issuedAt, issuedErr := wire.ParseTime(leaf.IssuedAt)
	periodStart, startErr := wire.ParseTime(claim.PeriodStart)
	periodEnd, endErr := wire.ParseTime(claim.PeriodEnd)
	if issuedErr != nil || startErr != nil || endErr != nil || issuedAt.Before(periodStart) || !issuedAt.Before(periodEnd) {
		return ErrLeafPeriod
	}
	if schedules == nil {
		return ErrScheduleUnknown
	}
	schedule, ok := schedules.Lookup(leaf.ScheduleID)
	if !ok {
		return ErrScheduleUnknown
	}
	amount, amountErr := wire.PositiveAmount(leaf.AmountBaseUnits)
	if amountErr != nil || !schedules.InForceWithin(leaf.ScheduleID, claim.Network, issuedAt) || !PossibleFee(schedule, amount) {
		return ErrLeafFee
	}
	return nil
}

// CheckTotals verifies receipt count, total amount, and root against the leaves.
func CheckTotals(claim wire.SettlementClaim, leaves []wire.SettlementLeaf) error {
	root, err := TreeRoot(leaves)
	if err != nil {
		return ErrTotals
	}
	total, err := wire.Amount(claim.TotalBaseUnits)
	if err != nil || claim.ReceiptCount != int64(len(leaves)) || total.Cmp(root.Sum) != 0 || claim.ReceiptsRoot != hex.EncodeToString(root.Hash[:]) {
		return ErrTotals
	}
	return nil
}
