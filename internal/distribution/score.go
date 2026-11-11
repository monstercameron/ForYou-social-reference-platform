package distribution

import (
	"math/big"
	"sort"
	"time"
)

// MonitorScore returns a duration-weighted score when reports are valid and provide required coverage.
// Durations are weighted in seconds, matching day weights for whole-day report periods.
func MonitorScore(reports []ReportSpan, required time.Duration) (int, bool) {
	if required < 0 {
		return 0, false
	}
	ordered := append([]ReportSpan(nil), reports...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Start.Before(ordered[j].Start) })
	var covered time.Duration
	weighted := new(big.Int)
	var previousEnd time.Time
	for i, report := range ordered {
		if !report.End.After(report.Start) || report.Score != nil && (*report.Score < 0 || *report.Score > 10000) {
			return 0, false
		}
		if i > 0 && report.Start.Before(previousEnd) {
			return 0, false
		}
		if report.End.After(previousEnd) {
			previousEnd = report.End
		}
		if report.Score == nil {
			continue
		}
		duration := report.End.Sub(report.Start)
		seconds := int64(duration / time.Second)
		covered += time.Duration(seconds) * time.Second
		weighted.Add(weighted, new(big.Int).Mul(big.NewInt(int64(*report.Score)), big.NewInt(seconds)))
	}
	if covered < required || covered == 0 {
		return 0, false
	}
	return int(weighted.Quo(weighted, big.NewInt(int64(covered/time.Second))).Int64()), true
}

// LowerMedian returns the lower middle value of values, or zero when values is empty.
func LowerMedian(values []int) int {
	if len(values) == 0 {
		return 0
	}
	ordered := append([]int(nil), values...)
	sort.Ints(ordered)
	return ordered[(len(ordered)-1)/2]
}
