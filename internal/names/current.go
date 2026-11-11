package names

import (
	"strings"
	"time"

	"foryou.local/foryou/internal/wire"
)

// Newer reports whether a is selected over b by issue time and ID tie-break.
// Both records must be valid, verified records; Current passes only verified records.
func Newer(a, b NameRecord) bool {
	at, _ := wire.ParseTime(a.IssuedAt)
	bt, _ := wire.ParseTime(b.IssuedAt)
	if !at.Equal(bt) {
		return at.After(bt)
	}
	return strings.Compare(a.NameID, b.NameID) < 0
}

// Current returns the selected valid record for authorID among records known locally.
func Current(authorID string, records []NameRecord, now time.Time) (NameRecord, bool) {
	var current NameRecord
	found := false
	for _, record := range records {
		if Verify(record, now) != nil || record.AuthorID != authorID {
			continue
		}
		if !found || Newer(record, current) {
			current, found = record, true
		}
	}
	return current, found
}
