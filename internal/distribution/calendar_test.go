package distribution

import (
	"testing"
	"time"
)

func TestCalendarQuarters(t *testing.T) {
	c := CalendarQuarters{}
	t.Run("quarter and deadlines", func(t *testing.T) {
		start, end := c.Quarter(mustTime(t, "2026-11-11T12:00:00Z"))
		if start != mustTime(t, "2026-10-01T00:00:00Z") || end != mustTime(t, "2027-01-01T00:00:00Z") {
			t.Fatalf("Quarter = [%s,%s)", start, end)
		}
		if got := c.CutOff(end); got != mustTime(t, "2027-01-15T00:00:00Z") {
			t.Fatalf("CutOff = %s", got)
		}
		if got := c.Due(end); got != mustTime(t, "2027-01-31T00:00:00Z") {
			t.Fatalf("Due = %s", got)
		}
	})
	t.Run("quarter end", func(t *testing.T) {
		if !c.IsQuarterEnd(mustTime(t, "2027-01-01T00:00:00Z")) {
			t.Fatal("rejected quarter end")
		}
		if c.IsQuarterEnd(mustTime(t, "2027-01-02T00:00:00Z")) {
			t.Fatal("accepted ordinary day")
		}
	})
}

func TestDevQuarters(t *testing.T) {
	c := DevQuarters{Epoch: mustTime(t, "2026-10-01T00:00:00Z"), DayLength: 30 * time.Second}
	t.Run("short quarter and deadlines", func(t *testing.T) {
		start, end := c.Quarter(c.Epoch.Add(50 * time.Minute))
		if start != c.Epoch.Add(45*time.Minute) || end != c.Epoch.Add(90*time.Minute) {
			t.Fatalf("Quarter = [%s,%s)", start, end)
		}
		if got := c.CutOff(end); got != end.Add(7*time.Minute) {
			t.Fatalf("CutOff = %s", got)
		}
		if got := c.Due(end); got != end.Add(15*time.Minute) {
			t.Fatalf("Due = %s", got)
		}
	})
	t.Run("floor before epoch", func(t *testing.T) {
		start, end := c.Quarter(c.Epoch.Add(-time.Minute))
		if start != c.Epoch.Add(-45*time.Minute) || end != c.Epoch {
			t.Fatalf("Quarter before epoch = [%s,%s)", start, end)
		}
	})
	t.Run("validation", func(t *testing.T) {
		if err := c.Validate(); err != nil {
			t.Fatal(err)
		}
		bad := c
		bad.DayLength = time.Millisecond
		if err := bad.Validate(); err == nil {
			t.Fatal("accepted short day")
		}
		bad = c
		bad.Epoch = time.Date(2026, 10, 1, 0, 0, 0, 0, time.FixedZone("utc", 0))
		if err := bad.Validate(); err == nil {
			t.Fatal("accepted non-UTC location")
		}
		bad = c
		bad.Epoch = c.Epoch.Add(time.Nanosecond)
		if err := bad.Validate(); err == nil {
			t.Fatal("accepted sub-second epoch")
		}
		if got, _ := bad.Quarter(c.Epoch); !got.IsZero() {
			t.Fatalf("invalid calendar quarter start = %s", got)
		}
	})
}
