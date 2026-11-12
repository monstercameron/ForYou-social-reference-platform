package sdk

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/quality"
	"foryou.local/foryou/internal/wire"
)

const qeMethodology = "Fixture methodology: two availability probes and two retrievals."

func qeObservations(monitorID, providerID string, retrievalPasses int) wire.QualityObservationSet {
	ok, failed := int64(200), int64(500)
	first, second := "fy1:"+strings.Repeat("a1", 32), "fy1:"+strings.Repeat("b2", 32)
	secondStatus, secondOutcome := &ok, "pass"
	if retrievalPasses < 2 {
		secondStatus, secondOutcome = &failed, "fail"
	}
	return wire.QualityObservationSet{ProviderID: providerID, MonitorID: monitorID, PeriodStart: "2027-01-01T00:00:00Z", PeriodEnd: "2027-01-02T00:00:00Z", Observations: []wire.QualityObservation{
		{Kind: "availability", StartedAt: "2027-01-01T00:00:01Z", DurationMs: 40, Outcome: "pass", HTTPStatus: &ok, WithinLimits: true},
		{Kind: "availability", StartedAt: "2027-01-01T00:00:02Z", DurationMs: 60, Outcome: "pass", HTTPStatus: &ok, WithinLimits: true},
		{Kind: "retrieval", MessageID: &first, StartedAt: "2027-01-01T00:00:03Z", DurationMs: 50, Outcome: "pass", HTTPStatus: &ok, WithinLimits: true},
		{Kind: "retrieval", MessageID: &second, StartedAt: "2027-01-01T00:00:04Z", DurationMs: 50, Outcome: secondOutcome, HTTPStatus: secondStatus, WithinLimits: true},
	}}
}

func qeReport(t *testing.T, key keys.PrivateKey, providerID string) wire.QualityReport {
	t.Helper()
	monitorID := keys.DIDOf(key)
	set := qeObservations(monitorID, providerID, 2)
	observationsDigest, err := quality.ObservationsDigest(set)
	if err != nil {
		t.Fatal(err)
	}
	aggregates, err := quality.Aggregate(set)
	if err != nil {
		t.Fatal(err)
	}
	seed := keys.EncodeB64URL(make([]byte, 32))
	commitment, err := quality.SeedCommitment(seed)
	if err != nil {
		t.Fatal(err)
	}
	methodologyDigest := sha256.Sum256([]byte(qeMethodology))
	report, err := quality.Sign(wire.QualityReport{MonitorID: monitorID, ProviderID: providerID, PeriodStart: set.PeriodStart, PeriodEnd: set.PeriodEnd, CreatedAt: "2027-01-02T00:00:00Z", MethodologyDigest: hex.EncodeToString(methodologyDigest[:]), ObservationsDigest: observationsDigest, SampleSeed: seed, SampleSeedCommitment: commitment, SampleCoverage: "complete", AvailabilityAttempts: 2, AvailabilitySuccesses: 2, RetrievalAttempts: 2, RetrievalPasses: 2, LatencyP50Ms: aggregates.LatencyP50, LatencyP95Ms: aggregates.LatencyP95, LatencyP99Ms: aggregates.LatencyP99, Sponsors: []string{}, Conflicts: "none", SampleFrameDigest: strings.Repeat("c", 64)}, key)
	if err != nil {
		t.Fatal(err)
	}
	return report
}

// qeMonitor serves one report's evidence; a nil set or empty methodology answers 404 for that part.
func qeMonitor(t *testing.T, report wire.QualityReport, methodology string, set *wire.QualityObservationSet) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/quality-reports/"+report.ReportID+"/methodology" && methodology != "":
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte(methodology))
		case r.URL.Path == "/v1/quality-reports/"+report.ReportID+"/observations" && set != nil:
			body, err := canon.Encode(*set)
			if err != nil {
				t.Error(err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"status":"not_found"}`))
		}
	}))
	t.Cleanup(server.Close)
	return rdTestClient(t, server.URL, Options{AllowLoopbackHTTP: true})
}

func TestQualityEvidence(t *testing.T) {
	key, err := keys.NewKeyFromSeed(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	providerID := "did:key:z6MkgFMdkUdT5dYyzMgBHmCBnPvwEFS7pZkjGnLCVG4coNEM"
	report := qeReport(t, key, providerID)
	honest := qeObservations(report.MonitorID, providerID, 2)

	t.Run("served evidence that recomputes is accepted", func(t *testing.T) {
		set, methodology, err := qeMonitor(t, report, qeMethodology, &honest).QualityEvidence(context.Background(), report)
		if err != nil || string(methodology) != qeMethodology || len(set.Observations) != 4 {
			t.Errorf("QualityEvidence = %d observations, %q, %v; want the four observations and the methodology", len(set.Observations), methodology, err)
		}
	})
	t.Run("an observation set that is not served is an error", func(t *testing.T) {
		if _, _, err := qeMonitor(t, report, qeMethodology, nil).QualityEvidence(context.Background(), report); err == nil {
			t.Error("a report without a served observation set was accepted")
		}
	})
	t.Run("a methodology that is not served is an error", func(t *testing.T) {
		if _, _, err := qeMonitor(t, report, "", &honest).QualityEvidence(context.Background(), report); err == nil {
			t.Error("a report without a served methodology was accepted")
		}
	})
	t.Run("observations other than the ones the report committed to are refused", func(t *testing.T) {
		other := qeObservations(report.MonitorID, providerID, 1)
		if _, _, err := qeMonitor(t, report, qeMethodology, &other).QualityEvidence(context.Background(), report); err == nil {
			t.Error("an observation set with a different digest was accepted")
		}
	})
	t.Run("a methodology other than the committed text is refused", func(t *testing.T) {
		if _, _, err := qeMonitor(t, report, qeMethodology+" Edited.", &honest).QualityEvidence(context.Background(), report); err == nil {
			t.Error("a methodology with a different digest was accepted")
		}
	})
	t.Run("counts that do not recompute are refused even when the digests match", func(t *testing.T) {
		weaker := qeObservations(report.MonitorID, providerID, 1)
		digest, err := quality.ObservationsDigest(weaker)
		if err != nil {
			t.Fatal(err)
		}
		inflated := report
		inflated.ObservationsDigest = digest
		inflated, err = quality.Sign(inflated, key)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := qeMonitor(t, inflated, qeMethodology, &weaker).QualityEvidence(context.Background(), inflated); err == nil {
			t.Error("a report claiming 2 retrieval passes over observations with 1 was accepted")
		}
	})
}
