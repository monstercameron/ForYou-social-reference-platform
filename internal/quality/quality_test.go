package quality

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"testing"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

const testSampleSeed = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"
const testMessageID = "fy1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestFixtureReportAndAggregates(t *testing.T) {
	r, set, method, monitorKey, providerKey := fixture(t)
	t.Run("sealed fixture verifies and has nearest rank latency", func(t *testing.T) {
		if err := Verify(r, set, method); err != nil {
			t.Fatal(err)
		}
		// These identity and evidence digests are pinned from tests/test_quality_reports.py.
		if r.ReportID != "fyqr2:7f7ac61529bcfb9a9196eb4828f98bbfadd421653e074c9ad149adc5eacc5bfc" {
			t.Fatalf("ReportID() = %s", r.ReportID)
		}
		if r.ObservationsDigest != "45c04d056eb5ce779d4c64cc7e16e0bba87b8324765d38ac81b375c69c7799d0" {
			t.Fatalf("ObservationsDigest() = %s", r.ObservationsDigest)
		}
		if *r.LatencyP50Ms != 9 || *r.LatencyP95Ms != 18 || *r.LatencyP99Ms != 18 {
			t.Fatalf("latencies = %d/%d/%d; want 9/18/18", *r.LatencyP50Ms, *r.LatencyP95Ms, *r.LatencyP99Ms)
		}
		if err := keys.Verify(keys.DIDOf(providerKey), "quality-report", mustCanonical(t, r), r.MonitorSignature); err == nil {
			t.Fatal("provider key verified monitor signature")
		}
		if err := keys.Verify(keys.DIDOf(monitorKey), "quality-report", mustCanonical(t, r), r.MonitorSignature); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("sign fills ID and signature", func(t *testing.T) {
		unsigned := r
		unsigned.ReportID = ""
		unsigned.MonitorSignature = ""
		signed, err := Sign(unsigned, monitorKey)
		if err != nil {
			t.Fatal(err)
		}
		if signed.ReportID != r.ReportID || CheckReport(signed) != nil {
			t.Fatal("Sign did not produce the fixture identity and valid signature")
		}
	})
}

func TestSignedInconsistenciesAreRejected(t *testing.T) {
	r, set, method, monitorKey, _ := fixture(t)
	t.Run("success count", func(t *testing.T) {
		changed := r
		changed.AvailabilitySuccesses = 21
		assertResealedRejected(t, changed, set, method, monitorKey, ErrCounts)
	})
	t.Run("latency percentile", func(t *testing.T) {
		changed := r
		changed.LatencyP95Ms = int64Pointer(17)
		assertResealedRejected(t, changed, set, method, monitorKey, ErrAggregates)
	})
	t.Run("controllers exceed operators", func(t *testing.T) {
		changed := r
		changed.IndependentControllers = int64Pointer(2)
		assertResealedRejected(t, changed, set, method, monitorKey, ErrReplication)
	})
	t.Run("report crosses maximum period", func(t *testing.T) {
		changed := r
		changed.PeriodEnd = "2026-12-31T00:00:00Z"
		changed.CreatedAt = changed.PeriodEnd
		if err := CheckReport(reseal(t, changed, set, monitorKey)); !errors.Is(err, ErrPeriod) {
			t.Fatalf("CheckReport() error = %v; want ErrPeriod", err)
		}
	})
	t.Run("dropped observation with original digest", func(t *testing.T) {
		dropped := set
		dropped.Observations = append([]wire.QualityObservation(nil), set.Observations[:len(set.Observations)-1]...)
		if err := Verify(r, dropped, method); !errors.Is(err, ErrEvidence) {
			t.Fatalf("Verify() error = %v; want ErrEvidence", err)
		}
	})
	t.Run("dropped observation with new digest", func(t *testing.T) {
		dropped := set
		dropped.Observations = append([]wire.QualityObservation(nil), set.Observations[:len(set.Observations)-1]...)
		changed := reseal(t, r, dropped, monitorKey)
		if err := Verify(changed, dropped, method); !errors.Is(err, ErrAggregates) {
			t.Fatalf("Verify() error = %v; want ErrAggregates", err)
		}
	})
}

func TestObservationAndReportTimeBounds(t *testing.T) {
	r, set, method, monitorKey, _ := fixture(t)
	t.Run("observation starts before period", func(t *testing.T) {
		changed := withFirstObservation(t, set, func(observation *wire.QualityObservation) {
			observation.StartedAt = "2026-10-31T23:59:59Z"
		})
		assertResealedRejected(t, r, changed, method, monitorKey, ErrObservation)
	})
	t.Run("observation at end is excluded", func(t *testing.T) {
		changed := withFirstObservation(t, set, func(observation *wire.QualityObservation) {
			observation.StartedAt = r.PeriodEnd
		})
		assertResealedRejected(t, r, changed, method, monitorKey, ErrObservation)
	})
	t.Run("created before period end", func(t *testing.T) {
		changed := r
		changed.CreatedAt = "2026-11-01T12:00:00Z"
		assertResealedRejected(t, changed, set, method, monitorKey, ErrPeriod)
	})
}

func TestHTTPClassificationConsistency(t *testing.T) {
	r, set, method, monitorKey, _ := fixture(t)
	t.Run("500 cannot pass", func(t *testing.T) { assertHTTPRejected(t, r, set, method, monitorKey, int64Pointer(500), "pass") })
	t.Run("204 cannot pass", func(t *testing.T) { assertHTTPRejected(t, r, set, method, monitorKey, int64Pointer(204), "pass") })
	t.Run("429 must be rate limited", func(t *testing.T) { assertHTTPRejected(t, r, set, method, monitorKey, int64Pointer(429), "pass") })
	t.Run("429 cannot be fail", func(t *testing.T) { assertHTTPRejected(t, r, set, method, monitorKey, int64Pointer(429), "fail") })
	t.Run("503 must be unavailable", func(t *testing.T) { assertHTTPRejected(t, r, set, method, monitorKey, int64Pointer(503), "pass") })
	t.Run("null status cannot pass", func(t *testing.T) { assertHTTPRejected(t, r, set, method, monitorKey, nil, "pass") })
	t.Run("local restore failure may have null status", func(t *testing.T) {
		changed := set
		changed.Observations = append(slices.Clone(set.Observations), observation(KindRestore, nil, r.PeriodStart, 1, OutcomeFail, nil))
		report := r
		report.RestoreAttempts = 1
		if err := Verify(reseal(t, report, changed, monitorKey), changed, method); err != nil {
			t.Fatalf("Verify() error = %v; want nil", err)
		}
	})
}

func TestMethodologyValidation(t *testing.T) {
	r, set, _, monitorKey, _ := fixture(t)
	t.Run("NUL byte is rejected after matching digest", func(t *testing.T) {
		invalid := []byte("method\x00ology")
		changed := resealWithMethod(t, r, set, invalid, monitorKey)
		if err := Verify(changed, set, invalid); !errors.Is(err, ErrEvidence) {
			t.Fatalf("Verify() error = %v; want ErrEvidence", err)
		}
	})
	t.Run("invalid UTF-8 is rejected after matching digest", func(t *testing.T) {
		invalid := []byte{0xff}
		changed := resealWithMethod(t, r, set, invalid, monitorKey)
		if err := Verify(changed, set, invalid); !errors.Is(err, ErrEvidence) {
			t.Fatalf("Verify() error = %v; want ErrEvidence", err)
		}
	})
}

func TestReplicationUnknownAndReferenceScore(t *testing.T) {
	r, set, method, monitorKey, _ := fixture(t)
	t.Run("null replication counts", func(t *testing.T) {
		changed := r
		changed.ReplicaOperators, changed.IndependentControllers = nil, nil
		changed = reseal(t, changed, set, monitorKey)
		if err := Verify(changed, set, method); err != nil {
			t.Fatal(err)
		}
		if changed.ReportID == r.ReportID {
			t.Fatal("null and numeric replication counts shared an ID")
		}
	})
	t.Run("zero replication counts", func(t *testing.T) {
		changed := r
		changed.ReplicaOperators, changed.IndependentControllers = int64Pointer(0), int64Pointer(0)
		changed = reseal(t, changed, set, monitorKey)
		if err := Verify(changed, set, method); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("one null replication count", func(t *testing.T) {
		changed := r
		changed.ReplicaOperators, changed.IndependentControllers = nil, int64Pointer(0)
		if err := CheckReport(reseal(t, changed, set, monitorKey)); !errors.Is(err, ErrReplication) {
			t.Fatalf("CheckReport() error = %v; want ErrReplication", err)
		}
	})
	t.Run("fixture score", func(t *testing.T) { assertScore(t, ReferenceScore(r), 9000) })
	t.Run("doubling counts leaves score unchanged", func(t *testing.T) {
		changed := r
		changed.AvailabilityAttempts, changed.AvailabilitySuccesses = 40, 36
		changed.RetrievalAttempts, changed.RetrievalPasses = 40, 36
		assertScore(t, ReferenceScore(changed), 9000)
	})
	t.Run("incomplete coverage is unknown", func(t *testing.T) {
		changed := r
		changed.SampleCoverage = "incomplete"
		assertUnknownScore(t, ReferenceScore(changed))
	})
	t.Run("unknown coverage is unknown", func(t *testing.T) {
		changed := r
		changed.SampleCoverage = "unknown"
		assertUnknownScore(t, ReferenceScore(changed))
	})
	t.Run("fewer than twenty retrievals is unknown", func(t *testing.T) {
		changed := r
		changed.RetrievalAttempts = 19
		assertUnknownScore(t, ReferenceScore(changed))
	})
	t.Run("minimum of availability and retrieval", func(t *testing.T) {
		changed := r
		changed.AvailabilitySuccesses, changed.RetrievalPasses = 18, 16
		assertScore(t, ReferenceScore(changed), 8000)
	})
}

func TestSamplingVectors(t *testing.T) {
	t.Run("seed commitment from pre helper vector", func(t *testing.T) {
		// Command used: pre('quality-sample-seed', {'sample_seed': b64(bytes(range(32)))}); SHA-256 of its output is c37530324033147dec3bcb69921cb212e59dceca9d4ac2706f3942cb54dd310b.
		got, err := SeedCommitment(testSampleSeed)
		if err != nil {
			t.Fatal(err)
		}
		if got != "c37530324033147dec3bcb69921cb212e59dceca9d4ac2706f3942cb54dd310b" {
			t.Fatalf("SeedCommitment() = %s", got)
		}
	})
	frame := []string{messageID('a'), messageID('b'), messageID('c'), messageID('d'), messageID('e')}
	t.Run("deterministic sample order", func(t *testing.T) {
		selected, err := Select(testSampleSeed, frame, 3)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{messageID('c'), messageID('b'), messageID('a')}
		if !slices.Equal(selected, want) {
			t.Fatalf("Select() = %v; want %v", selected, want)
		}
	})
	t.Run("sample larger than frame", func(t *testing.T) {
		if _, err := Select(testSampleSeed, frame, 6); !errors.Is(err, ErrFrame) {
			t.Fatalf("Select() error = %v; want ErrFrame", err)
		}
	})
	t.Run("unsorted frame", func(t *testing.T) {
		if _, err := FrameDigest([]string{messageID('b'), messageID('a')}); !errors.Is(err, ErrFrame) {
			t.Fatalf("FrameDigest() error = %v; want ErrFrame", err)
		}
	})
	t.Run("duplicate frame", func(t *testing.T) {
		if _, err := FrameDigest([]string{messageID('a'), messageID('a')}); !errors.Is(err, ErrFrame) {
			t.Fatalf("FrameDigest() error = %v; want ErrFrame", err)
		}
	})
}

func TestClassificationAndRestrictedCap(t *testing.T) {
	t.Run("distribution frame 404 fails", func(t *testing.T) { assertClassification(t, int64Pointer(404), false, false, true, OutcomeFail) })
	t.Run("ambiguous 404 is unknown", func(t *testing.T) { assertClassification(t, int64Pointer(404), false, false, false, OutcomeUnknown) })
	t.Run("distribution frame 410 fails", func(t *testing.T) { assertClassification(t, int64Pointer(410), false, false, true, OutcomeFail) })
	t.Run("timeout is unavailable", func(t *testing.T) { assertClassification(t, nil, false, false, false, OutcomeUnavailable) })
	t.Run("legal 403 is restricted", func(t *testing.T) { assertClassification(t, int64Pointer(403), false, true, false, OutcomeRestricted) })
	t.Run("verified restore may pass without HTTP", func(t *testing.T) {
		if got := Classify(KindRestore, nil, true, false, false); got != OutcomePass {
			t.Fatalf("Classify() = %s; want pass", got)
		}
	})
	t.Run("restricted cap keeps earliest two percent", func(t *testing.T) {
		observations := []wire.QualityObservation{
			{Kind: string(KindRetrieval), StartedAt: "2026-11-01T00:00:03Z", Outcome: string(OutcomeRestricted)},
			{Kind: string(KindRetrieval), StartedAt: "2026-11-01T00:00:01Z", Outcome: string(OutcomeRestricted)},
			{Kind: string(KindRetrieval), StartedAt: "2026-11-01T00:00:02Z", Outcome: string(OutcomeRestricted)},
		}
		capped, share := ApplyRestrictedCap(observations, 100)
		if capped[0].Outcome != string(OutcomeFail) || capped[1].Outcome != string(OutcomeRestricted) || capped[2].Outcome != string(OutcomeRestricted) || share != 200 {
			t.Fatalf("capped outcomes/share = %s/%s/%s, %d; want fail/restricted/restricted, 200", capped[0].Outcome, capped[1].Outcome, capped[2].Outcome, share)
		}
	})
}

func fixture(t *testing.T) (wire.QualityReport, wire.QualityObservationSet, []byte, ed25519.PrivateKey, ed25519.PrivateKey) {
	t.Helper()
	authorSeed := [32]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31}
	providerSeed := [32]byte{32, 33, 34, 35, 36, 37, 38, 39, 40, 41, 42, 43, 44, 45, 46, 47, 48, 49, 50, 51, 52, 53, 54, 55, 56, 57, 58, 59, 60, 61, 62, 63}
	monitorKey, err := keys.NewKeyFromSeed(authorSeed[:])
	if err != nil {
		t.Fatal(err)
	}
	providerKey, err := keys.NewKeyFromSeed(providerSeed[:])
	if err != nil {
		t.Fatal(err)
	}
	start, end := "2026-11-01T00:00:00Z", "2026-11-02T00:00:00Z"
	set := wire.QualityObservationSet{
		ProviderID: keys.DIDOf(providerKey), MonitorID: keys.DIDOf(monitorKey), PeriodStart: start, PeriodEnd: end,
		Observations: append(availabilityFixture(start), retrievalFixture(start)...),
	}
	method := []byte("synthetic methodology")
	methodDigest := sha256.Sum256(method)
	observationsDigest, err := ObservationsDigest(set)
	if err != nil {
		t.Fatal(err)
	}
	seedCommitment, err := SeedCommitment(testSampleSeed)
	if err != nil {
		t.Fatal(err)
	}
	report := wire.QualityReport{
		MonitorID: set.MonitorID, ProviderID: set.ProviderID, PeriodStart: start, PeriodEnd: end, CreatedAt: end,
		MethodologyDigest: hex.EncodeToString(methodDigest[:]), ObservationsDigest: observationsDigest,
		SampleSeed: testSampleSeed, SampleSeedCommitment: seedCommitment, SampleCoverage: "complete",
		AvailabilityAttempts: 20, AvailabilitySuccesses: 18, RetrievalAttempts: 20, RetrievalPasses: 18,
		LatencyP50Ms: int64Pointer(9), LatencyP95Ms: int64Pointer(18), LatencyP99Ms: int64Pointer(18),
		ReplicaOperators: int64Pointer(1), IndependentControllers: int64Pointer(1), Sponsors: []string{},
		Conflicts: "", SampleFrameDigest: digestHex([]byte("synthetic sample frame")),
	}
	report, err = Sign(report, monitorKey)
	if err != nil {
		t.Fatal(err)
	}
	return report, set, method, monitorKey, providerKey
}

func availabilityFixture(start string) []wire.QualityObservation {
	return []wire.QualityObservation{
		observation(KindAvailability, nil, start, 1, OutcomePass, int64Pointer(200)),
		observation(KindAvailability, nil, start, 2, OutcomePass, int64Pointer(200)),
		observation(KindAvailability, nil, start, 3, OutcomePass, int64Pointer(200)),
		observation(KindAvailability, nil, start, 4, OutcomePass, int64Pointer(200)),
		observation(KindAvailability, nil, start, 5, OutcomePass, int64Pointer(200)),
		observation(KindAvailability, nil, start, 6, OutcomePass, int64Pointer(200)),
		observation(KindAvailability, nil, start, 7, OutcomePass, int64Pointer(200)),
		observation(KindAvailability, nil, start, 8, OutcomePass, int64Pointer(200)),
		observation(KindAvailability, nil, start, 9, OutcomePass, int64Pointer(200)),
		observation(KindAvailability, nil, start, 10, OutcomePass, int64Pointer(200)),
		observation(KindAvailability, nil, start, 11, OutcomePass, int64Pointer(200)),
		observation(KindAvailability, nil, start, 12, OutcomePass, int64Pointer(200)),
		observation(KindAvailability, nil, start, 13, OutcomePass, int64Pointer(200)),
		observation(KindAvailability, nil, start, 14, OutcomePass, int64Pointer(200)),
		observation(KindAvailability, nil, start, 15, OutcomePass, int64Pointer(200)),
		observation(KindAvailability, nil, start, 16, OutcomePass, int64Pointer(200)),
		observation(KindAvailability, nil, start, 17, OutcomePass, int64Pointer(200)),
		observation(KindAvailability, nil, start, 18, OutcomePass, int64Pointer(200)),
		observation(KindAvailability, nil, start, 19, OutcomeUnavailable, int64Pointer(503)),
		observation(KindAvailability, nil, start, 20, OutcomeUnavailable, int64Pointer(503)),
	}
}

func retrievalFixture(start string) []wire.QualityObservation {
	messageID := testMessageID
	return []wire.QualityObservation{
		observation(KindRetrieval, &messageID, start, 1, OutcomePass, int64Pointer(200)),
		observation(KindRetrieval, &messageID, start, 2, OutcomePass, int64Pointer(200)),
		observation(KindRetrieval, &messageID, start, 3, OutcomePass, int64Pointer(200)),
		observation(KindRetrieval, &messageID, start, 4, OutcomePass, int64Pointer(200)),
		observation(KindRetrieval, &messageID, start, 5, OutcomePass, int64Pointer(200)),
		observation(KindRetrieval, &messageID, start, 6, OutcomePass, int64Pointer(200)),
		observation(KindRetrieval, &messageID, start, 7, OutcomePass, int64Pointer(200)),
		observation(KindRetrieval, &messageID, start, 8, OutcomePass, int64Pointer(200)),
		observation(KindRetrieval, &messageID, start, 9, OutcomePass, int64Pointer(200)),
		observation(KindRetrieval, &messageID, start, 10, OutcomePass, int64Pointer(200)),
		observation(KindRetrieval, &messageID, start, 11, OutcomePass, int64Pointer(200)),
		observation(KindRetrieval, &messageID, start, 12, OutcomePass, int64Pointer(200)),
		observation(KindRetrieval, &messageID, start, 13, OutcomePass, int64Pointer(200)),
		observation(KindRetrieval, &messageID, start, 14, OutcomePass, int64Pointer(200)),
		observation(KindRetrieval, &messageID, start, 15, OutcomePass, int64Pointer(200)),
		observation(KindRetrieval, &messageID, start, 16, OutcomePass, int64Pointer(200)),
		observation(KindRetrieval, &messageID, start, 17, OutcomePass, int64Pointer(200)),
		observation(KindRetrieval, &messageID, start, 18, OutcomePass, int64Pointer(200)),
		observation(KindRetrieval, &messageID, start, 19, OutcomeUnavailable, int64Pointer(503)),
		observation(KindRetrieval, &messageID, start, 20, OutcomeUnavailable, int64Pointer(503)),
	}
}

func observation(kind Kind, messageID *string, started string, duration int64, outcome Outcome, status *int64) wire.QualityObservation {
	return wire.QualityObservation{Kind: string(kind), MessageID: messageID, StartedAt: started, DurationMs: duration, Outcome: string(outcome), HTTPStatus: status, WithinLimits: true}
}

func assertResealedRejected(t *testing.T, report wire.QualityReport, set wire.QualityObservationSet, method []byte, key ed25519.PrivateKey, want error) {
	t.Helper()
	if err := Verify(reseal(t, report, set, key), set, method); !errors.Is(err, want) {
		t.Fatalf("Verify() error = %v; want %v", err, want)
	}
}

func reseal(t *testing.T, report wire.QualityReport, set wire.QualityObservationSet, key ed25519.PrivateKey) wire.QualityReport {
	t.Helper()
	canonical, err := canon.Encode(set)
	if err != nil {
		t.Fatal(err)
	}
	report.ObservationsDigest = digestHex(canonical)
	report.ReportID, report.MonitorSignature = "", ""
	signed, err := Sign(report, key)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func withFirstObservation(t *testing.T, set wire.QualityObservationSet, modify func(*wire.QualityObservation)) wire.QualityObservationSet {
	t.Helper()
	set.Observations = append([]wire.QualityObservation(nil), set.Observations...)
	modify(&set.Observations[0])
	return set
}

func assertHTTPRejected(t *testing.T, report wire.QualityReport, set wire.QualityObservationSet, method []byte, key ed25519.PrivateKey, status *int64, outcome string) {
	t.Helper()
	changed := withFirstObservation(t, set, func(observation *wire.QualityObservation) {
		observation.HTTPStatus, observation.Outcome = status, outcome
	})
	assertResealedRejected(t, report, changed, method, key, ErrObservation)
}

func resealWithMethod(t *testing.T, report wire.QualityReport, set wire.QualityObservationSet, method []byte, key ed25519.PrivateKey) wire.QualityReport {
	t.Helper()
	digest := sha256.Sum256(method)
	report.MethodologyDigest = hex.EncodeToString(digest[:])
	return reseal(t, report, set, key)
}

func assertScore(t *testing.T, score *int64, want int64) {
	t.Helper()
	if score == nil || *score != want {
		t.Fatalf("score = %v; want %d", score, want)
	}
}

func assertUnknownScore(t *testing.T, score *int64) {
	t.Helper()
	if score != nil {
		t.Fatalf("score = %d; want unknown", *score)
	}
}

func assertClassification(t *testing.T, status *int64, validated, restricted, inFrame bool, want Outcome) {
	t.Helper()
	if got := Classify(KindRetrieval, status, validated, restricted, inFrame); got != want {
		t.Fatalf("Classify() = %s; want %s", got, want)
	}
}

func int64Pointer(value int64) *int64 { return &value }

func messageID(letter byte) string { return "fy1:" + strings.Repeat(string(letter), 64) }

func mustCanonical(t *testing.T, report wire.QualityReport) []byte {
	t.Helper()
	canonical, err := canon.EncodeWithout(report, "monitor_signature")
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}
