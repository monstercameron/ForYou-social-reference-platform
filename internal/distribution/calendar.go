package distribution

import (
	"errors"
	"fmt"
	"math"
	"time"
)

var (
	// ErrCalendar identifies an invalid calendar configuration.
	ErrCalendar             = errors.New("distribution: invalid calendar")
	devQuarterDurationLimit = time.Duration(math.MaxInt64 / 90)
)

// Calendar defines statement periods and their publication deadlines.
type Calendar interface {
	Quarter(t time.Time) (start, end time.Time)
	IsQuarterEnd(t time.Time) bool
	Day() time.Duration
	CutOff(quarterEnd time.Time) time.Time
	Due(quarterEnd time.Time) time.Time
}

// CalendarQuarters uses UTC Gregorian calendar quarters.
type CalendarQuarters struct{}

// Quarter returns the UTC quarter containing t as a half-open interval.
func (CalendarQuarters) Quarter(t time.Time) (time.Time, time.Time) {
	u := t.UTC()
	month := (int(u.Month())-1)/3*3 + 1
	start := time.Date(u.Year(), time.Month(month), 1, 0, 0, 0, 0, time.UTC)
	return start, start.AddDate(0, 3, 0)
}

// IsQuarterEnd reports whether t is midnight UTC at a quarter boundary.
func (CalendarQuarters) IsQuarterEnd(t time.Time) bool {
	if t.Location() != time.UTC || t.Hour() != 0 || t.Minute() != 0 || t.Second() != 0 || t.Nanosecond() != 0 {
		return false
	}
	return t.Day() == 1 && (t.Month() == time.January || t.Month() == time.April || t.Month() == time.July || t.Month() == time.October)
}

// Day returns the real 24-hour day length.
func (CalendarQuarters) Day() time.Duration { return 24 * time.Hour }

// CutOff returns the statement evidence cutoff fourteen days after quarterEnd.
func (c CalendarQuarters) CutOff(quarterEnd time.Time) time.Time {
	return quarterEnd.Add(14 * c.Day())
}

// Due returns the statement deadline thirty days after quarterEnd.
func (c CalendarQuarters) Due(quarterEnd time.Time) time.Time {
	return quarterEnd.Add(30 * c.Day())
}

// DevQuarters uses fixed 90-day quarters. Statements are dev artifacts, and
// verifiers must be configured with the same calendar. Call Validate before
// using its methods; invalid values yield zero times, false, and a zero day.
type DevQuarters struct {
	Epoch     time.Time
	DayLength time.Duration
}

// Validate checks the dev epoch and day length before calendar operations.
func (c DevQuarters) Validate() error {
	if c.DayLength < time.Second || c.DayLength > devQuarterDurationLimit {
		return fmt.Errorf("%w: day length must be at least one second and fit a quarter", ErrCalendar)
	}
	if c.Epoch.Location() != time.UTC || c.Epoch.Nanosecond() != 0 {
		return fmt.Errorf("%w: epoch must be a canonical UTC second", ErrCalendar)
	}
	return nil
}

// Quarter returns the fixed-length dev quarter containing t, including times
// before Epoch. Call Validate first; an invalid calendar returns zero times.
func (c DevQuarters) Quarter(t time.Time) (time.Time, time.Time) {
	if c.Validate() != nil {
		return time.Time{}, time.Time{}
	}
	length := 90 * c.DayLength
	delta := t.Sub(c.Epoch)
	if !c.Epoch.Add(delta).Equal(t) {
		return time.Time{}, time.Time{}
	}
	index := int64(delta / length)
	if delta < 0 && delta%length != 0 {
		index--
	}
	if index > 0 && index > math.MaxInt64/int64(length) || index < 0 && index < math.MinInt64/int64(length) {
		return time.Time{}, time.Time{}
	}
	start := c.Epoch.Add(time.Duration(index * int64(length)))
	end := start.Add(length)
	if !start.Before(end) || t.Before(start) || !t.Before(end) {
		return time.Time{}, time.Time{}
	}
	return start, end
}

// IsQuarterEnd reports whether t is a dev quarter boundary. Call Validate first;
// an invalid calendar returns false.
func (c DevQuarters) IsQuarterEnd(t time.Time) bool {
	if c.Validate() != nil {
		return false
	}
	start, _ := c.Quarter(t)
	return !start.IsZero() && t.Equal(start)
}

// Day returns the configured dev day length, or zero for an invalid calendar.
func (c DevQuarters) Day() time.Duration {
	if c.Validate() != nil {
		return 0
	}
	return c.DayLength
}

// CutOff returns the dev evidence cutoff fourteen dev days after quarterEnd.
func (c DevQuarters) CutOff(quarterEnd time.Time) time.Time {
	day := c.Day()
	if day == 0 {
		return time.Time{}
	}
	return quarterEnd.Add(14 * day)
}

// Due returns the dev statement deadline thirty dev days after quarterEnd.
func (c DevQuarters) Due(quarterEnd time.Time) time.Time {
	day := c.Day()
	if day == 0 {
		return time.Time{}
	}
	return quarterEnd.Add(30 * day)
}

var _ Calendar = CalendarQuarters{}
var _ Calendar = DevQuarters{}
