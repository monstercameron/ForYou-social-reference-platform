package monitor

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"foryou.local/foryou/internal/quality"
)

func TestQualityReportForOnePeriod(t *testing.T) {
	periodStart, periodEnd := qualitySettledAt, qualitySettledAt.Add(300*time.Second)
	check := func(t *testing.T, options qualityWorldOptions, wantTally map[string]int, wantCounts [4]int64, wantScore *int64) {
		t.Helper()
		w := qualityNewWorld(t, options)
		w.runPeriod(periodStart, periodEnd)
		report, evidence := w.report(0, periodStart)
		if err := quality.Verify(report, evidence.Observations, []byte(evidence.Methodology)); err != nil {
			t.Fatalf("stored report fails quality.Verify: %v", err)
		}
		if report.PeriodStart != "2027-01-01T00:00:00Z" || report.PeriodEnd != "2027-01-01T00:05:00Z" || report.CreatedAt != "2027-01-01T00:05:00Z" || report.SampleCoverage != "complete" {
			t.Errorf("report period %s to %s, created %s, coverage %s; want 00:00 to 00:05, created 00:05, complete", report.PeriodStart, report.PeriodEnd, report.CreatedAt, report.SampleCoverage)
		}
		if got := [4]int64{report.AvailabilitySuccesses, report.AvailabilityAttempts, report.RetrievalPasses, report.RetrievalAttempts}; got != wantCounts {
			t.Errorf("availability and retrieval counts = %v; want %v", got, wantCounts)
		}
		if got := qualityTally(evidence.Observations); !reflect.DeepEqual(got, wantTally) {
			t.Errorf("observations = %v; want %v", got, wantTally)
		}
		got := quality.ReferenceScore(report)
		if (got == nil) != (wantScore == nil) || got != nil && *got != *wantScore {
			t.Errorf("reference score = %v; want %v", got, wantScore)
		}
		if report.RestoreAttempts != 0 || report.ReplicaOperators != nil || report.IndependentControllers != nil || len(report.Sponsors) != 0 || report.Conflicts != qualityConflicts {
			t.Errorf("disclosures = restore %d, operators %v, controllers %v, sponsors %v, conflicts %q", report.RestoreAttempts, report.ReplicaOperators, report.IndependentControllers, report.Sponsors, report.Conflicts)
		}
	}
	score := func(value int64) *int64 { return &value }
	t.Run("healthy provider passes every probe", func(t *testing.T) {
		check(t, qualityWorldOptions{settled: 30, members: 1},
			map[string]int{"availability pass 200": 24, "retrieval pass 200": 24},
			[4]int64{24, 24, 24, 24}, score(10000))
	})
	t.Run("503 listings and wrong-ID bodies lower the score to the weaker route", func(t *testing.T) {
		check(t, qualityWorldOptions{settled: 30, members: 1, unavailableListing: 4, wrongIDRetrievals: 2},
			map[string]int{"availability pass 200": 20, "availability unavailable 503": 4, "retrieval pass 200": 22, "retrieval fail 200": 2},
			[4]int64{20, 24, 22, 24}, score(8333))
	})
	t.Run("in-frame 410 is a failed retrieval", func(t *testing.T) {
		check(t, qualityWorldOptions{settled: 30, members: 1, goneRetrievals: 1},
			map[string]int{"availability pass 200": 24, "retrieval pass 200": 23, "retrieval fail 410": 1},
			[4]int64{24, 24, 23, 24}, score(9583))
	})
	t.Run("timeouts are unavailable with null status and a small sample leaves the score unknown", func(t *testing.T) {
		qualitySetTunables(t, 600, 600, 0)
		qualityProbeTimeout = 400 * time.Millisecond
		check(t, qualityWorldOptions{settled: 12, members: 1, hangListings: true},
			map[string]int{"availability unavailable null": 24, "retrieval pass 200": 12},
			[4]int64{0, 24, 12, 12}, nil)
	})
}

func TestQualityFrameSource(t *testing.T) {
	periodStart, periodEnd := qualitySettledAt, qualitySettledAt.Add(300*time.Second)
	t.Run("a vault that answers with an error gives no plan and no provider traffic", func(t *testing.T) {
		w := qualityNewWorld(t, qualityWorldOptions{settled: 30, members: 1, vault: qualityVaultNotFound})
		w.runAt(periodStart)
		w.runAt(periodEnd.Add(-time.Second))
		if _, planned := w.plan(0, periodStart); planned {
			t.Error("a plan was stored without a frame from the vault")
		}
		if w.providerHits != 0 {
			t.Errorf("provider requests = %d; want 0", w.providerHits)
		}
	})
	t.Run("an unreachable vault falls back to the provider's changes and says so", func(t *testing.T) {
		w := qualityNewWorld(t, qualityWorldOptions{settled: 30, members: 1, vault: qualityVaultDown})
		w.runPeriod(periodStart, periodEnd)
		report, evidence := w.report(0, periodStart)
		if !strings.Contains(evidence.Methodology, "Frame: provider GET /v1/changes (the vault could not be reached), mirrored incrementally.") {
			t.Errorf("methodology does not name the provider fallback:\n%s", evidence.Methodology)
		}
		if report.RetrievalPasses != 24 {
			t.Errorf("retrieval passes = %d; want 24", report.RetrievalPasses)
		}
	})
	t.Run("records outside the member's audited claims are excluded and disclosed", func(t *testing.T) {
		w := qualityNewWorld(t, qualityWorldOptions{settled: 30, unsettled: 2, members: 1})
		w.runAt(periodStart)
		plan, planned := w.plan(0, periodStart)
		if !planned {
			t.Fatal("no plan stored")
		}
		if plan.FrameSize != 30 || plan.Excluded != (qualityExclusions{NoClaim: 2}) {
			t.Errorf("frame size %d, exclusions %+v; want 30 and 2 without a claim", plan.FrameSize, plan.Excluded)
		}
		w.runAt(periodEnd.Add(-time.Second))
		w.runAt(periodEnd)
		_, evidence := w.report(0, periodStart)
		if !strings.Contains(evidence.Methodology, "Exclusions: 2 records whose receipt is in no claim this monitor holds with leaves") {
			t.Errorf("methodology does not disclose the exclusions:\n%s", evidence.Methodology)
		}
	})
}
