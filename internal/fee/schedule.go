package fee

import (
	"errors"
	"fmt"
	"math/big"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

var (
	// ErrNoScheduleInForce identifies a time before the first approved schedule.
	ErrNoScheduleInForce = errors.New("fee: no schedule in force")
	// ErrApproval identifies an invalid fee schedule approval.
	ErrApproval = errors.New("fee: invalid approval")
	// ErrRevisionBound identifies a schedule outside its permitted revision rules.
	ErrRevisionBound = errors.New("fee: revision bound violated")
)

func wireValue(v any) (any, error) {
	b, err := canon.Encode(v)
	if err != nil {
		return nil, err
	}
	return canon.Parse(b, canon.Limits{MaxDepth: 32})
}

// ScheduleID computes the immutable identifier for a fee schedule.
func ScheduleID(s wire.FeeSchedule) (string, error) {
	b, err := canon.EncodeWithout(s, "schedule_id")
	if err != nil {
		return "", err
	}
	return keys.DigestID("fyfs2:", "fee-schedule-id", b)
}

// ValidateSchedule checks the closed wire shape and fee schedule invariants.
func ValidateSchedule(s wire.FeeSchedule) error {
	v, err := wireValue(s)
	if err != nil {
		return err
	}
	if err := wire.Check("FeeSchedule", v); err != nil {
		return err
	}
	unit, err := Denomination(s.Network)
	if err != nil {
		return err
	}
	if s.Denomination != unit || s.ProfileMajor != 2 || s.ProfileMinor != 0 || s.Revision < 1 {
		return fmt.Errorf("fee: invalid schedule fields")
	}
	base, err := ParseAmount(s.BaseFeeBaseUnits)
	if err != nil {
		return err
	}
	rate, err := ParseAmount(s.PerKiBFeeBaseUnits)
	if err != nil {
		return err
	}
	if base.Sign() == 0 && rate.Sign() == 0 {
		return fmt.Errorf("fee: both schedule rates are zero")
	}
	if _, err := wire.ParseTime(s.EffectiveAt); err != nil {
		return err
	}
	if _, err := wire.ParseTime(s.ExpiresAt); err != nil {
		return fmt.Errorf("fee: invalid expires_at")
	}
	id, err := ScheduleID(s)
	if err != nil {
		return err
	}
	if s.ScheduleID != id {
		return fmt.Errorf("fee: schedule_id mismatch")
	}
	return nil
}

// VerifyApproval verifies release signatures, notice, and the preceding revision bound.
func VerifyApproval(s wire.FeeSchedule, a wire.FeeScheduleApproval, releaseKeyIDs []string, threshold int, previous *wire.FeeSchedule) error {
	if err := ValidateSchedule(s); err != nil {
		return fmt.Errorf("%w: %v", ErrApproval, err)
	}
	if a.ScheduleID != s.ScheduleID {
		return ErrApproval
	}
	approvalValue, err := wireValue(a)
	if err != nil || wire.Check("FeeScheduleApproval", approvalValue) != nil {
		return ErrApproval
	}
	approved, err := wire.ParseTime(a.ApprovedAt)
	if err != nil {
		return ErrApproval
	}
	effective, _ := wire.ParseTime(s.EffectiveAt)
	notice := 60 * 24 * time.Hour
	if a.Drift {
		notice = 14 * 24 * time.Hour
	}
	if effective.Sub(approved) < notice {
		return fmt.Errorf("%w: notice period", ErrApproval)
	}
	if len(a.ReleaseSignatures) < 4 || len(a.ReleaseSignatures) > 64 || threshold < 1 {
		return ErrApproval
	}
	signed, err := canon.EncodeWithout(a, "release_signatures")
	if err != nil {
		return err
	}
	allowed := make(map[string]bool, len(releaseKeyIDs))
	for _, id := range releaseKeyIDs {
		allowed[id] = true
	}
	verified, prior := 0, ""
	for _, sig := range a.ReleaseSignatures {
		if sig.ReleaseKeyID <= prior {
			return ErrApproval
		}
		prior = sig.ReleaseKeyID
		if !allowed[sig.ReleaseKeyID] {
			continue
		}
		if err := keys.Verify(sig.ReleaseKeyID, "fee-schedule-approval", signed, sig.Signature); err != nil {
			return fmt.Errorf("%w: release signature", ErrApproval)
		}
		verified++
	}
	if verified < threshold {
		return ErrApproval
	}
	if s.Revision == 1 {
		if previous != nil || a.Drift {
			return ErrRevisionBound
		}
	} else {
		if previous == nil || previous.Network != s.Network || previous.Revision+1 != s.Revision || ValidateSchedule(*previous) != nil {
			return ErrRevisionBound
		}
		oldAt, _ := wire.ParseTime(previous.EffectiveAt)
		spacing := 90 * 24 * time.Hour
		if a.Drift {
			spacing = 30 * 24 * time.Hour
		}
		if effective.Sub(oldAt) < spacing || approved.Before(oldAt) {
			return ErrRevisionBound
		}
		for _, bytes := range []int{1024, MaxBillableBytes} {
			oldFee, e1 := Amount(*previous, bytes)
			newFee, e2 := Amount(s, bytes)
			if e1 != nil || e2 != nil || new(big.Int).Mul(oldFee, big.NewInt(2)).Cmp(newFee) < 0 || new(big.Int).Mul(newFee, big.NewInt(2)).Cmp(oldFee) < 0 {
				return ErrRevisionBound
			}
		}
	}
	return nil
}

// CheckScheduleSet requires every revised schedule to have a same-time partner network schedule.
func CheckScheduleSet(schedules []wire.FeeSchedule) error {
	for _, s := range schedules {
		if s.Revision <= 1 {
			continue
		}
		other := BitcoinMainnet
		if s.Network == BitcoinMainnet {
			other = EthereumMainnet
		} else if s.Network != EthereumMainnet {
			return ErrUnsupportedNetwork
		}
		found := false
		for _, candidate := range schedules {
			if candidate.Network == other && candidate.Revision > 1 && candidate.EffectiveAt == s.EffectiveAt {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("fee: revision %d for %s lacks simultaneous partner", s.Revision, s.Network)
		}
	}
	return nil
}

// InForce returns the latest schedule effective at at; expiry does not end a schedule.
func InForce(schedules []wire.FeeSchedule, network string, at time.Time) (wire.FeeSchedule, error) {
	if _, err := Denomination(network); err != nil {
		return wire.FeeSchedule{}, err
	}
	var selected wire.FeeSchedule
	var selectedAt time.Time
	found := false
	for _, s := range schedules {
		if s.Network != network {
			continue
		}
		if err := ValidateSchedule(s); err != nil {
			return wire.FeeSchedule{}, err
		}
		effective, _ := wire.ParseTime(s.EffectiveAt)
		if !effective.After(at) && (!found || effective.After(selectedAt)) {
			selected, selectedAt, found = s, effective, true
		}
	}
	if !found {
		return wire.FeeSchedule{}, ErrNoScheduleInForce
	}
	return selected, nil
}
