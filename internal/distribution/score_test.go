package distribution

import (
	"testing"
	"time"
)

func TestMonitorScore(t *testing.T) {
	base := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	span := func(start, end int, score *int) ReportSpan {
		return ReportSpan{Start: base.Add(time.Duration(start) * 24 * time.Hour), End: base.Add(time.Duration(end) * 24 * time.Hour), Score: score}
	}
	value := func(v int) *int { return &v }
	check := func(t *testing.T, reports []ReportSpan, required time.Duration, want int, wantOK bool) {
		t.Helper()
		got, ok := MonitorScore(reports, required)
		if got != want || ok != wantOK {
			t.Errorf("MonitorScore() = (%d, %t); want (%d, %t)", got, ok, want, wantOK)
		}
	}
	t.Run("duration weighted partial reports rounds down", func(t *testing.T) {
		check(t, []ReportSpan{span(0, 59, value(0)), span(59, 60, value(10000))}, 60*24*time.Hour, 166, true)
	})
	t.Run("weighted average rounds down", func(t *testing.T) {
		check(t, []ReportSpan{span(0, 31, value(10000)), span(31, 62, value(2000))}, 60*24*time.Hour, 6000, true)
	})
	t.Run("three overlapping reports invalidate monitor", func(t *testing.T) {
		check(t, []ReportSpan{span(0, 31, value(1)), span(20, 51, value(2)), span(40, 71, value(3))}, 0, 0, false)
	})
	t.Run("fifty nine days do not meet sixty day coverage", func(t *testing.T) {
		check(t, []ReportSpan{span(0, 59, value(9000))}, 60*24*time.Hour, 0, false)
	})
	t.Run("short quarter report does not meet full quarter coverage", func(t *testing.T) {
		check(t, []ReportSpan{span(60, 90, value(9000))}, 90*24*time.Hour, 0, false)
	})
	t.Run("unknown scores provide no coverage", func(t *testing.T) {
		check(t, []ReportSpan{span(0, 90, nil)}, 60*24*time.Hour, 0, false)
	})
	t.Run("invalid interval rejected", func(t *testing.T) {
		check(t, []ReportSpan{span(4, 4, value(5000))}, 0, 0, false)
	})
}

func TestLowerMedian(t *testing.T) {
	t.Run("odd number", func(t *testing.T) {
		if got := LowerMedian([]int{9100, 0, 9000}); got != 9000 {
			t.Errorf("LowerMedian() = %d; want 9000", got)
		}
	})
	t.Run("even number chooses lower", func(t *testing.T) {
		if got := LowerMedian([]int{9000, 10000}); got != 9000 {
			t.Errorf("LowerMedian() = %d; want 9000", got)
		}
	})
	t.Run("empty", func(t *testing.T) {
		if got := LowerMedian(nil); got != 0 {
			t.Errorf("LowerMedian(nil) = %d; want 0", got)
		}
	})
}
