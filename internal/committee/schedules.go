package committee

import (
	"fmt"
	"math/big"
	"sort"
	"time"

	"foryou.local/foryou/internal/fee"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/wire"
)

// VerifiedSchedules holds fee schedules whose release approvals have been checked.
type VerifiedSchedules struct {
	entries []wire.FeeSchedule
	byID    map[string]settlement.Schedule
}

// NewSchedules validates the approved schedule set and every release approval.
func NewSchedules(entries []wire.ApprovedFeeSchedule, releaseKeyIDs []string, threshold int) (*VerifiedSchedules, error) {
	if len(entries) == 0 {
		return nil, fmt.Errorf("committee: no fee schedules")
	}
	schedules := make([]wire.FeeSchedule, len(entries))
	for i, entry := range entries {
		schedules[i] = entry.Schedule
	}
	if err := fee.CheckScheduleSet(schedules); err != nil {
		return nil, err
	}
	ordered := append([]wire.ApprovedFeeSchedule(nil), entries...)
	sort.Slice(ordered, func(i, j int) bool {
		a, _ := wire.ParseTime(ordered[i].Schedule.EffectiveAt)
		b, _ := wire.ParseTime(ordered[j].Schedule.EffectiveAt)
		if a.Equal(b) {
			return ordered[i].Schedule.Network < ordered[j].Schedule.Network
		}
		return a.Before(b)
	})
	previous := make(map[string]*wire.FeeSchedule)
	result := &VerifiedSchedules{entries: make([]wire.FeeSchedule, 0, len(entries)), byID: make(map[string]settlement.Schedule, len(entries))}
	for _, entry := range ordered {
		prior := previous[entry.Schedule.Network]
		if err := fee.VerifyApproval(entry.Schedule, entry.Approval, releaseKeyIDs, threshold, prior); err != nil {
			return nil, err
		}
		base, err := fee.ParseAmount(entry.Schedule.BaseFeeBaseUnits)
		if err != nil {
			return nil, err
		}
		rate, err := fee.ParseAmount(entry.Schedule.PerKiBFeeBaseUnits)
		if err != nil {
			return nil, err
		}
		if _, exists := result.byID[entry.Schedule.ScheduleID]; exists {
			return nil, fmt.Errorf("committee: duplicate schedule ID")
		}
		result.entries = append(result.entries, entry.Schedule)
		result.byID[entry.Schedule.ScheduleID] = settlement.Schedule{BaseFee: base, PerKiBFee: rate}
		copy := entry.Schedule
		previous[copy.Network] = &copy
	}
	return result, nil
}

// Lookup returns the base and per-KiB fee for a verified schedule.
func (s *VerifiedSchedules) Lookup(scheduleID string) (settlement.Schedule, bool) {
	value, ok := s.byID[scheduleID]
	if !ok {
		return settlement.Schedule{}, false
	}
	return settlement.Schedule{BaseFee: new(big.Int).Set(value.BaseFee), PerKiBFee: new(big.Int).Set(value.PerKiBFee)}, true
}

// InForceWithin reports whether a verified schedule was in force during the fee window.
func (s *VerifiedSchedules) InForceWithin(scheduleID, network string, at time.Time) bool {
	boundary := at.Add(-time.Hour)
	current, err := fee.InForce(s.entries, network, boundary)
	if err == nil && current.ScheduleID == scheduleID {
		return true
	}
	for _, candidate := range s.entries {
		if candidate.ScheduleID != scheduleID || candidate.Network != network {
			continue
		}
		effective, parseErr := wire.ParseTime(candidate.EffectiveAt)
		if parseErr == nil && effective.After(boundary) && !effective.After(at) {
			return true
		}
	}
	return false
}
