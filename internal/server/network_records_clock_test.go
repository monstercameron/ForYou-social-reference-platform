package server

import (
	"testing"
	"time"

	"foryou.local/foryou/internal/sdk"
	"foryou.local/foryou/internal/wire"
)

func TestRecordAheadOfTheClockIsShownAsNotVerifiedYet(t *testing.T) {
	issued := time.Date(2026, 11, 12, 9, 30, 0, 0, time.UTC)
	rd := recordReader{root: &sdk.TrustRoot{}, now: issued.Add(-2 * time.Minute)}
	view, shown := rd.unverifiable(sdk.Record{Class: sdk.ClassNotEvaluated, EvaluateAfter: issued}, wire.TransportMessage{MessageID: "fy1:clock"})
	want := "Not verified yet: the receipt is dated ahead of this server's clock, and that clock may be the one that is wrong. It is checked again after 12 Nov 2026, 09:30 UTC."
	if !shown || view.Unverifiable != want {
		t.Errorf("shown %v with %q; want the record shown with %q", shown, view.Unverifiable, want)
	}
}
