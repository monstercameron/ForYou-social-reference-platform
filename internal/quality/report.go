package quality

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

var (
	// ErrPeriod reports an invalid report period.
	ErrPeriod = errors.New("quality: invalid report period")
	// ErrReplication reports inconsistent replication counts.
	ErrReplication = errors.New("quality: invalid replication counts")
	// ErrCounts reports successes or passes exceeding attempts.
	ErrCounts = errors.New("quality: successes exceed attempts")
	// ErrLatency reports invalid latency percentiles.
	ErrLatency = errors.New("quality: invalid latency percentiles")
	// ErrSeed reports an invalid sample seed or commitment.
	ErrSeed = errors.New("quality: invalid sample seed")
	// ErrIdentity reports a report ID or signature mismatch.
	ErrIdentity = errors.New("quality: report identity mismatch")
	// ErrEvidence reports evidence binding, digest, or methodology errors.
	ErrEvidence = errors.New("quality: invalid evidence")
	// ErrAggregates reports aggregates that differ from the observation set.
	ErrAggregates = errors.New("quality: aggregates do not match observations")
	// ErrObservation reports an invalid observation or observation set.
	ErrObservation = errors.New("quality: invalid observation")
	// ErrFrame reports an invalid sampling frame or sample size.
	ErrFrame = errors.New("quality: invalid sampling frame")
)

// ReportID computes the content ID of a quality report.
func ReportID(r wire.QualityReport) (string, error) {
	canonical, err := canon.EncodeWithout(r, "report_id", "monitor_signature")
	if err != nil {
		return "", err
	}
	return keys.DigestID("fyqr2:", "quality-report-id", canonical)
}

// Sign fills the report ID and monitor signature using key.
func Sign(r wire.QualityReport, key ed25519.PrivateKey) (wire.QualityReport, error) {
	if len(key) != ed25519.PrivateKeySize {
		return wire.QualityReport{}, fmt.Errorf("%w: invalid monitor private key", ErrIdentity)
	}
	if r.MonitorID != keys.DIDOf(key) {
		return wire.QualityReport{}, fmt.Errorf("%w: monitor ID does not match signing key", ErrIdentity)
	}
	r.ReportID = ""
	r.MonitorSignature = ""
	id, err := ReportID(r)
	if err != nil {
		return wire.QualityReport{}, err
	}
	r.ReportID = id
	canonical, err := canon.EncodeWithout(r, "monitor_signature")
	if err != nil {
		return wire.QualityReport{}, err
	}
	r.MonitorSignature, err = keys.Sign(key, "quality-report", canonical)
	if err != nil {
		return wire.QualityReport{}, err
	}
	return r, nil
}

// CheckReport validates a report's schema, semantic fields, ID, and signature.
func CheckReport(r wire.QualityReport) error {
	if err := checkWire("QualityReport", r); err != nil {
		return err
	}
	start, err := wire.ParseTime(r.PeriodStart)
	if err != nil {
		return fmt.Errorf("%w: period_start: %v", ErrPeriod, err)
	}
	end, err := wire.ParseTime(r.PeriodEnd)
	if err != nil {
		return fmt.Errorf("%w: period_end: %v", ErrPeriod, err)
	}
	created, err := wire.ParseTime(r.CreatedAt)
	if err != nil {
		return fmt.Errorf("%w: created_at: %v", ErrPeriod, err)
	}
	if !start.Before(end) || end.After(created) || end.Sub(start) > 31*24*time.Hour {
		return fmt.Errorf("%w: period bounds are invalid or exceed 31 days", ErrPeriod)
	}
	if (r.ReplicaOperators == nil) != (r.IndependentControllers == nil) {
		return fmt.Errorf("%w: counts must both be known or null", ErrReplication)
	}
	if r.ReplicaOperators != nil && (*r.ReplicaOperators < 0 || *r.IndependentControllers < 0 || *r.IndependentControllers > *r.ReplicaOperators) {
		return ErrReplication
	}
	if r.AvailabilitySuccesses > r.AvailabilityAttempts || r.RetrievalPasses > r.RetrievalAttempts || r.RestorePasses > r.RestoreAttempts {
		return ErrCounts
	}
	if !orderedNullable(r.LatencyP50Ms, r.LatencyP95Ms, r.LatencyP99Ms) {
		return ErrLatency
	}
	seed, err := keys.DecodeB64URL(r.SampleSeed, 32)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrSeed, err)
	}
	commitment, err := SeedCommitment(keys.EncodeB64URL(seed))
	if err != nil || commitment != r.SampleSeedCommitment {
		return fmt.Errorf("%w: commitment mismatch", ErrSeed)
	}
	id, err := ReportID(r)
	if err != nil || r.ReportID != id {
		return fmt.Errorf("%w: report ID mismatch", ErrIdentity)
	}
	canonical, err := canon.EncodeWithout(r, "monitor_signature")
	if err != nil {
		return err
	}
	if err := keys.Verify(r.MonitorID, "quality-report", canonical, r.MonitorSignature); err != nil {
		return fmt.Errorf("%w: monitor signature: %v", ErrIdentity, err)
	}
	return nil
}

// Verify validates a report against its observations and exact methodology bytes.
func Verify(r wire.QualityReport, set wire.QualityObservationSet, methodology []byte) error {
	if err := CheckReport(r); err != nil {
		return err
	}
	if len(methodology) > 1024*1024 || !utf8.Valid(methodology) || bytes.IndexByte(methodology, 0) >= 0 {
		return fmt.Errorf("%w: methodology must be valid UTF-8 without NUL and at most 1 MiB", ErrEvidence)
	}
	methodDigest := sha256.Sum256(methodology)
	if hex.EncodeToString(methodDigest[:]) != r.MethodologyDigest {
		return fmt.Errorf("%w: methodology digest mismatch", ErrEvidence)
	}
	if set.ProviderID != r.ProviderID || set.MonitorID != r.MonitorID || set.PeriodStart != r.PeriodStart || set.PeriodEnd != r.PeriodEnd {
		return fmt.Errorf("%w: observation set does not match report", ErrEvidence)
	}
	digest, err := ObservationsDigest(set)
	if err != nil {
		if errors.Is(err, ErrObservation) {
			return err
		}
		return fmt.Errorf("%w: %v", ErrEvidence, err)
	}
	if digest != r.ObservationsDigest {
		return fmt.Errorf("%w: observations digest mismatch", ErrEvidence)
	}
	aggregates, err := Aggregate(set)
	if err != nil {
		return err
	}
	if aggregates.AvailabilityAttempts != r.AvailabilityAttempts || aggregates.AvailabilitySuccesses != r.AvailabilitySuccesses ||
		aggregates.RetrievalAttempts != r.RetrievalAttempts || aggregates.RetrievalPasses != r.RetrievalPasses ||
		aggregates.RestoreAttempts != r.RestoreAttempts || aggregates.RestorePasses != r.RestorePasses ||
		!sameInt64(aggregates.LatencyP50, r.LatencyP50Ms) || !sameInt64(aggregates.LatencyP95, r.LatencyP95Ms) || !sameInt64(aggregates.LatencyP99, r.LatencyP99Ms) {
		return ErrAggregates
	}
	return nil
}

func sameInt64(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func orderedNullable(p50, p95, p99 *int64) bool {
	if p50 == nil || p95 == nil || p99 == nil {
		return p50 == nil && p95 == nil && p99 == nil
	}
	return *p50 >= 0 && *p50 <= *p95 && *p95 <= *p99
}

func checkWire(def string, value any) error {
	encoded, err := canon.Encode(value)
	if err != nil {
		return err
	}
	model, err := canon.Parse(encoded, canon.Limits{MaxDepth: 32})
	if err != nil {
		return err
	}
	return wire.Check(def, model)
}

func digestHex(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}
