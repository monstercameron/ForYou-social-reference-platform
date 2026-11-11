package devnet

import (
	"fmt"
	"time"

	"foryou.local/foryou/internal/distribution"
)

var calendarEpoch = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

// Calendar returns Gregorian quarters for a standard day or fixed 90-day dev quarters.
func Calendar(day time.Duration) (distribution.Calendar, error) {
	if day == 0 || day == 24*time.Hour {
		return distribution.CalendarQuarters{}, nil
	}
	if day < 30*time.Second {
		return nil, fmt.Errorf("devnet: dev day must be at least 30 seconds")
	}
	calendar := distribution.DevQuarters{Epoch: calendarEpoch, DayLength: day}
	if err := calendar.Validate(); err != nil {
		return nil, err
	}
	return calendar, nil
}
