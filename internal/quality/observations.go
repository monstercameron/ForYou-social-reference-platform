package quality

import (
	"fmt"
	"sort"
	"unicode/utf8"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/wire"
)

// Outcome classifies one provider probe.
type Outcome string

const (
	// OutcomePass records a validated response.
	OutcomePass Outcome = "pass"
	// OutcomeFail records an unsuccessful response.
	OutcomeFail Outcome = "fail"
	// OutcomeRestricted records a legally restricted response.
	OutcomeRestricted Outcome = "restricted"
	// OutcomeRateLimited records a response rejected by rate limits.
	OutcomeRateLimited Outcome = "rate_limited"
	// OutcomeUnavailable records a timeout or unavailable service.
	OutcomeUnavailable Outcome = "unavailable"
	// OutcomeUnknown records an ambiguous response.
	OutcomeUnknown Outcome = "unknown"
)

// Kind identifies the quality measurement performed by an observation.
type Kind string

const (
	// KindAvailability identifies a public listing probe.
	KindAvailability Kind = "availability"
	// KindRetrieval identifies a message retrieval probe.
	KindRetrieval Kind = "retrieval"
	// KindRestore identifies a scoped restore drill.
	KindRestore Kind = "restore"
)

// Aggregates contains report counters and availability latency percentiles.
type Aggregates struct {
	AvailabilityAttempts, AvailabilitySuccesses int64
	RetrievalAttempts, RetrievalPasses          int64
	RestoreAttempts, RestorePasses              int64
	LatencyP50, LatencyP95, LatencyP99          *int64
}

// ObservationsDigest hashes the canonical JCS representation of set.
func ObservationsDigest(set wire.QualityObservationSet) (string, error) {
	if err := checkObservationSet(set); err != nil {
		return "", err
	}
	canonical, err := canon.Encode(set)
	if err != nil {
		return "", err
	}
	if len(canonical) > 4*1024*1024 {
		return "", fmt.Errorf("%w: observation set exceeds 4 MiB", ErrObservation)
	}
	return digestHex(canonical), nil
}

// Classify applies the quality outcome table to a probe result.
func Classify(kind Kind, httpStatus *int64, validated, legalRestriction, inDistributionFrame bool) Outcome {
	if kind != KindAvailability && kind != KindRetrieval && kind != KindRestore {
		return OutcomeUnknown
	}
	if httpStatus == nil {
		if kind == KindRestore && validated {
			return OutcomePass
		}
		return OutcomeUnavailable
	}
	status := *httpStatus
	if status == 429 {
		return OutcomeRateLimited
	}
	if status == 503 {
		return OutcomeUnavailable
	}
	if status == 403 && legalRestriction {
		return OutcomeRestricted
	}
	if (status == 404 || status == 410) && inDistributionFrame {
		return OutcomeFail
	}
	if status == 404 {
		return OutcomeUnknown
	}
	if status != 200 {
		return OutcomeFail
	}
	if validated {
		return OutcomePass
	}
	return OutcomeFail
}

// Aggregate validates set and recomputes its counters and availability latency percentiles.
func Aggregate(set wire.QualityObservationSet) (Aggregates, error) {
	if err := checkObservationSet(set); err != nil {
		return Aggregates{}, err
	}
	var result Aggregates
	durations := make([]int64, 0)
	for _, observation := range set.Observations {
		if !observation.WithinLimits || !eligibleOutcome(Outcome(observation.Outcome)) {
			continue
		}
		switch Kind(observation.Kind) {
		case KindAvailability:
			result.AvailabilityAttempts++
			if observation.Outcome == string(OutcomePass) {
				result.AvailabilitySuccesses++
				durations = append(durations, observation.DurationMs)
			}
		case KindRetrieval:
			result.RetrievalAttempts++
			if observation.Outcome == string(OutcomePass) {
				result.RetrievalPasses++
			}
		case KindRestore:
			result.RestoreAttempts++
			if observation.Outcome == string(OutcomePass) {
				result.RestorePasses++
			}
		}
	}
	if len(durations) > 0 {
		sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
		result.LatencyP50 = percentile(durations, 50)
		result.LatencyP95 = percentile(durations, 95)
		result.LatencyP99 = percentile(durations, 99)
	}
	return result, nil
}

// ApplyRestrictedCap limits restricted retrieval outcomes to two percent of a planned sample.
func ApplyRestrictedCap(observations []wire.QualityObservation, planned int) (capped []wire.QualityObservation, restrictedShareBP int) {
	capped = append([]wire.QualityObservation(nil), observations...)
	if planned <= 0 {
		return capped, 0
	}
	indices := make([]int, 0)
	for index, observation := range capped {
		if observation.Kind == string(KindRetrieval) && observation.Outcome == string(OutcomeRestricted) {
			indices = append(indices, index)
		}
	}
	sort.SliceStable(indices, func(i, j int) bool {
		left, _ := wire.ParseTime(capped[indices[i]].StartedAt)
		right, _ := wire.ParseTime(capped[indices[j]].StartedAt)
		if left.Equal(right) {
			return indices[i] < indices[j]
		}
		return left.Before(right)
	})
	allowed := planned / 50
	for position, index := range indices {
		if position >= allowed {
			capped[index].Outcome = string(OutcomeFail)
		}
	}
	remaining := len(indices)
	if remaining > allowed {
		remaining = allowed
	}
	restrictedShareBP = remaining * 10000 / planned
	return capped, restrictedShareBP
}

func checkObservationSet(set wire.QualityObservationSet) error {
	if err := checkWire("QualityObservationSet", set); err != nil {
		return fmt.Errorf("%w: schema validation: %v", ErrObservation, err)
	}
	if len(set.Observations) > 10000 {
		return fmt.Errorf("%w: too many observations", ErrObservation)
	}
	canonical, err := canon.Encode(set)
	if err != nil {
		return err
	}
	if !utf8.Valid(canonical) || len(canonical) > 4*1024*1024 {
		return fmt.Errorf("%w: invalid or oversized observation encoding", ErrObservation)
	}
	start, err := wire.ParseTime(set.PeriodStart)
	if err != nil {
		return fmt.Errorf("%w: invalid period_start: %v", ErrObservation, err)
	}
	end, err := wire.ParseTime(set.PeriodEnd)
	if err != nil || !start.Before(end) {
		return fmt.Errorf("%w: invalid observation period", ErrObservation)
	}
	for _, observation := range set.Observations {
		started, err := wire.ParseTime(observation.StartedAt)
		if err != nil || started.Before(start) || !started.Before(end) {
			return fmt.Errorf("%w: observation start outside period", ErrObservation)
		}
		if err := checkObservation(observation); err != nil {
			return err
		}
	}
	return nil
}

func checkObservation(observation wire.QualityObservation) error {
	kind := Kind(observation.Kind)
	outcome := Outcome(observation.Outcome)
	status := observation.HTTPStatus
	if kind != KindAvailability && kind != KindRetrieval && kind != KindRestore {
		return fmt.Errorf("%w: invalid observation kind", ErrObservation)
	}
	if kind == KindRetrieval && outcome == OutcomePass && observation.MessageID == nil {
		return fmt.Errorf("%w: retrieval pass has no message ID", ErrObservation)
	}
	if status == nil && kind != KindRestore && outcome != OutcomeUnavailable {
		return fmt.Errorf("%w: null status must be unavailable", ErrObservation)
	}
	if status != nil {
		if *status == 429 && outcome != OutcomeRateLimited {
			return fmt.Errorf("%w: HTTP 429 must be rate_limited", ErrObservation)
		}
		if *status == 503 && outcome != OutcomeUnavailable {
			return fmt.Errorf("%w: HTTP 503 must be unavailable", ErrObservation)
		}
		if *status == 404 && outcome != OutcomeUnknown && outcome != OutcomeFail {
			return fmt.Errorf("%w: HTTP 404 must be unknown or fail", ErrObservation)
		}
		if outcome == OutcomePass && *status != 200 {
			return fmt.Errorf("%w: pass requires HTTP 200", ErrObservation)
		}
	}
	return nil
}

func eligibleOutcome(outcome Outcome) bool {
	return outcome == OutcomePass || outcome == OutcomeFail || outcome == OutcomeRateLimited || outcome == OutcomeUnavailable
}

func percentile(sorted []int64, p int64) *int64 {
	index := (p*int64(len(sorted))+99)/100 - 1
	value := sorted[index]
	return &value
}
