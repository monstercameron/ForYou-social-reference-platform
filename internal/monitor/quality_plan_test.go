package monitor

import (
	"bytes"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/distribution"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/quality"
	"foryou.local/foryou/internal/store"
	"foryou.local/foryou/internal/wire"
)

func TestQualityPlanIsCommittedBeforeProbing(t *testing.T) {
	seed := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31}
	w := qualityNewWorld(t, qualityWorldOptions{settled: 30, members: 1})
	qualitySeedSource = bytes.NewReader(seed)
	planStoredAtFirstProbe := false
	w.firstProbeSeen = func() { _, planStoredAtFirstProbe = w.plan(0, qualitySettledAt) }
	w.runAt(qualitySettledAt)
	plan, planned := w.plan(0, qualitySettledAt)
	w.runAt(qualitySettledAt.Add(299 * time.Second))
	t.Run("the plan is stored before the first probe", func(t *testing.T) {
		if !planned || !planStoredAtFirstProbe {
			t.Fatalf("plan stored after the first pass %v, at the first probe %v; want both", planned, planStoredAtFirstProbe)
		}
	})
	t.Run("the sample is quality.Select over the sorted 30-message frame", func(t *testing.T) {
		encodedSeed := keys.EncodeB64URL(seed)
		want, err := quality.Select(encodedSeed, w.ids, 24)
		if err != nil {
			t.Fatal(err)
		}
		got := qualityRetrievalTargets(plan)
		sort.Strings(want)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("retrieval targets = %v; want %v", got, want)
		}
		frameDigest, err := quality.FrameDigest(w.ids)
		if err != nil {
			t.Fatal(err)
		}
		commitment, err := quality.SeedCommitment(encodedSeed)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Seed != encodedSeed || plan.SeedCommitment != commitment || plan.FrameDigest != frameDigest || plan.FrameSize != 30 || plan.FrameSource != qualityVaultSource {
			t.Errorf("plan commitments = seed %s, commitment %s, frame %s of %d from %q", plan.Seed, plan.SeedCommitment, plan.FrameDigest, plan.FrameSize, plan.FrameSource)
		}
	})
	t.Run("the probe-time digest commits to the planned times", func(t *testing.T) {
		if len(plan.Probes) != 48 || plan.ProbeTimesDigest == "" {
			t.Fatalf("planned probes = %d with digest %q; want 48 and a digest", len(plan.Probes), plan.ProbeTimesDigest)
		}
		changed := append([]qualityPlannedProbe(nil), plan.Probes...)
		changed[0].StartedAt = "2027-01-01T00:04:59Z"
		digest, err := qualityProbeTimesDigest(changed)
		if err != nil {
			t.Fatal(err)
		}
		if digest == plan.ProbeTimesDigest {
			t.Error("moving one planned time leaves the digest unchanged")
		}
	})
}

func TestQualityProbeTimesKeepHostedMembersWithinTheSourceLimit(t *testing.T) {
	start := qualitySettledAt
	seed := make([]byte, 32)
	t.Run("five members and 24 listings at six per minute need 20 minutes", func(t *testing.T) {
		_, err := qualityProbeTimes(seed, quality.KindAvailability, start, start, start.Add(1190*time.Second), 24, 6, 4, 5)
		if err == nil || !strings.Contains(err.Error(), "has 23 availability slots for 24 probes with 5 hosted members at 6 requests per minute per source") {
			t.Errorf("19m50s window error for the fifth member = %v; want a refusal naming 23 slots, 5 members and the limit", err)
		}
	})
	t.Run("in 20 minutes each member takes every fifth ten-second slot", func(t *testing.T) {
		first, err := qualityProbeTimes(seed, quality.KindAvailability, start, start, start.Add(20*time.Minute), 24, 6, 0, 5)
		if err != nil {
			t.Fatal(err)
		}
		last, err := qualityProbeTimes(seed, quality.KindAvailability, start, start, start.Add(20*time.Minute), 24, 6, 4, 5)
		if err != nil {
			t.Fatal(err)
		}
		got := []time.Time{first[0], first[1], first[23], last[0], last[1], last[23]}
		want := []time.Time{start, start.Add(50 * time.Second), start.Add(1150 * time.Second), start.Add(40 * time.Second), start.Add(90 * time.Second), start.Add(1190 * time.Second)}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("member 0 and member 4 times = %v; want %v", got, want)
		}
	})
	t.Run("a late plan only uses slots inside the remaining window", func(t *testing.T) {
		times, err := qualityProbeTimes(seed, quality.KindRetrieval, start, start.Add(90*time.Second), start.Add(120*time.Second), 15, 30, 0, 1)
		if err != nil {
			t.Fatal(err)
		}
		if !times[0].Equal(start.Add(90*time.Second)) || !times[14].Equal(start.Add(118*time.Second)) {
			t.Errorf("times span %s to %s; want 00:01:30 to 00:01:58", wire.FormatTime(times[0]), wire.FormatTime(times[14]))
		}
	})
}

func TestQualityProbesNeverExceedThePerSourceLimit(t *testing.T) {
	w := qualityNewWorld(t, qualityWorldOptions{settled: 30, members: 1})
	qualitySetTunables(t, 6, 30, 0)
	w.runAt(qualitySettledAt)
	late := qualitySettledAt.Add(299 * time.Second)
	w.runAt(late)
	var listings, direct qualitySendLog
	if err := w.service.DB().View(func(tx store.Tx) error {
		var err error
		if listings, _, err = qualitySends.Get(tx, w.provider.URL+"\x00availability"); err != nil {
			return err
		}
		direct, _, err = qualitySends.Get(tx, w.provider.URL+"\x00retrieval")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	at := wire.FormatTime(late)
	t.Run("a backlog of listings is cut at six in the last minute", func(t *testing.T) {
		if want := []string{at, at, at, at, at, at}; !reflect.DeepEqual(listings.Sent, want) {
			t.Errorf("listing sends in the last minute = %v; want %v", listings.Sent, want)
		}
	})
	t.Run("the plan ends incomplete instead of exceeding the limit", func(t *testing.T) {
		w.runAt(qualitySettledAt.Add(300 * time.Second))
		report, _ := w.report(0, qualitySettledAt)
		if report.SampleCoverage != "incomplete" || len(direct.Sent) > 30 {
			t.Errorf("coverage %s with %d direct sends in a minute; want incomplete and at most 30", report.SampleCoverage, len(direct.Sent))
		}
		if quality.ReferenceScore(report) != nil {
			t.Errorf("reference score = %v; want unknown for incomplete coverage", *quality.ReferenceScore(report))
		}
	})
}

func TestQualityPeriods(t *testing.T) {
	devCalendar, err := devnet.Calendar(30 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	check := func(t *testing.T, calendar distribution.Calendar, reportDays int, at, wantStart, wantEnd string) {
		t.Helper()
		qualitySetTunables(t, 6, 30, reportDays)
		instant, err := wire.ParseTime(at)
		if err != nil {
			t.Fatal(err)
		}
		start, end, err := qualityPeriodAt(calendar, instant)
		if err != nil {
			t.Fatal(err)
		}
		if wire.FormatTime(start) != wantStart || wire.FormatTime(end) != wantEnd {
			t.Errorf("period at %s = %s to %s; want %s to %s", at, wire.FormatTime(start), wire.FormatTime(end), wantStart, wantEnd)
		}
	}
	t.Run("a dev calendar reports in ten dev days", func(t *testing.T) {
		check(t, devCalendar, 0, "2027-01-01T00:07:12Z", "2027-01-01T00:05:00Z", "2027-01-01T00:10:00Z")
	})
	t.Run("the last period is clipped at the quarter end", func(t *testing.T) {
		check(t, devCalendar, 7, "2027-01-01T00:44:59Z", "2027-01-01T00:42:00Z", "2027-01-01T00:45:00Z")
	})
	t.Run("the next quarter starts a new period", func(t *testing.T) {
		check(t, devCalendar, 7, "2027-01-01T00:45:00Z", "2027-01-01T00:45:00Z", "2027-01-01T00:48:30Z")
	})
	t.Run("a real calendar reports daily", func(t *testing.T) {
		check(t, distribution.CalendarQuarters{}, 0, "2027-03-31T13:00:00Z", "2027-03-31T00:00:00Z", "2027-04-01T00:00:00Z")
	})
	t.Run("a period longer than 31 days is refused", func(t *testing.T) {
		qualitySetTunables(t, 6, 30, 32)
		if _, _, err := qualityPeriodAt(distribution.CalendarQuarters{}, qualitySettledAt); err == nil {
			t.Error("a 32-day period was accepted")
		}
	})
}
