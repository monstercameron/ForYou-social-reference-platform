package distribution

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

var (
	// ErrPolicyInvalid identifies a schema, time, bound, or monitor identity violation.
	ErrPolicyInvalid = errors.New("distribution: invalid policy")
	// ErrPolicyID identifies a policy ID that does not match the policy contents.
	ErrPolicyID = errors.New("distribution: invalid policy ID")
	// ErrPolicyTiming identifies an invalid effective time or approval notice period.
	ErrPolicyTiming = errors.New("distribution: invalid policy timing")
	// ErrCommitteeChange identifies a committee transition outside the handover bounds.
	ErrCommitteeChange = errors.New("distribution: invalid committee change")
	// ErrCommitteeSpacing identifies committee changes less than 30 days apart.
	ErrCommitteeSpacing = errors.New("distribution: committee changes too close together")
	// ErrReleaseApproval identifies a missing or invalid release-key approval.
	ErrReleaseApproval = errors.New("distribution: invalid release approval")
)

// ReleaseVerifier verifies release-key approval for a signed object.
type ReleaseVerifier interface {
	VerifyRelease(label string, unsigned any, sigs []wire.ReleaseSignature, signedAt time.Time, signed any) error
}

// PolicyID returns the domain-separated identifier for p without its ID or signatures.
func PolicyID(p wire.DistributionPolicy) (string, error) {
	canonical, err := canon.EncodeWithout(p, "policy_id", "release_signatures")
	if err != nil {
		return "", fmt.Errorf("%w: encode policy: %v", ErrPolicyID, err)
	}
	return keys.DigestID("fydp2:", "distribution-policy-id", canonical)
}

// CheckPolicy validates p against the verified earlier policies in ascending effective_at order.
// The last earlier policy is the predecessor; an empty slice means p has no predecessor.
func CheckPolicy(p wire.DistributionPolicy, earlier []wire.DistributionPolicy, rv ReleaseVerifier) error {
	full, err := canon.Encode(p)
	if err != nil {
		return fmt.Errorf("%w: encode: %v", ErrPolicyInvalid, err)
	}
	value, err := canon.Parse(full, canon.Limits{MaxDepth: 32})
	if err != nil {
		return fmt.Errorf("%w: parse: %v", ErrPolicyInvalid, err)
	}
	if err := wire.Check("DistributionPolicy", value); err != nil {
		return fmt.Errorf("%w: schema: %v", ErrPolicyInvalid, err)
	}
	approved, err := wire.ParseTime(p.ApprovedAt)
	if err != nil {
		return fmt.Errorf("%w: approved_at: %v", ErrPolicyInvalid, err)
	}
	effective, err := wire.ParseTime(p.EffectiveAt)
	if err != nil {
		return fmt.Errorf("%w: effective_at: %v", ErrPolicyInvalid, err)
	}
	if p.VaultShareBP < 2500 {
		return fmt.Errorf("%w: vault_share_bp below 2500", ErrPolicyInvalid)
	}
	if p.MonitorShareBP < 0 || p.MonitorShareBP > 1000 {
		return fmt.Errorf("%w: monitor_share_bp outside bounds", ErrPolicyInvalid)
	}
	if p.MonitorShareBP > p.VaultShareBP {
		return fmt.Errorf("%w: monitor_share_bp exceeds vault_share_bp", ErrPolicyInvalid)
	}
	if p.MinReports < 3 || p.MaxRetentionYears < 1 || len(p.Monitors) < 5 {
		return fmt.Errorf("%w: reports, retention, or monitor count below bounds", ErrPolicyInvalid)
	}
	for i, did := range p.Monitors {
		pub, err := keys.ParseDID(did)
		if err != nil || keys.DID(pub) != did {
			return fmt.Errorf("%w: non-canonical monitor DID", ErrPolicyInvalid)
		}
		if i > 0 && p.Monitors[i-1] >= did {
			return fmt.Errorf("%w: monitors must be sorted and unique", ErrPolicyInvalid)
		}
	}
	id, err := PolicyID(p)
	if err != nil {
		return fmt.Errorf("%w: compute ID: %v", ErrPolicyID, err)
	}
	if id != p.PolicyID {
		return fmt.Errorf("%w: got %q, want %q", ErrPolicyID, p.PolicyID, id)
	}

	var previous *wire.DistributionPolicy
	var previousAt time.Time
	earlierTimes := make([]time.Time, len(earlier))
	for i, prior := range earlier {
		earlierTimes[i], err = wire.ParseTime(prior.EffectiveAt)
		if err != nil {
			return fmt.Errorf("%w: earlier effective_at: %v", ErrPolicyInvalid, err)
		}
		if i > 0 && !earlierTimes[i].After(earlierTimes[i-1]) {
			return fmt.Errorf("%w: earlier policies are not ordered", ErrPolicyInvalid)
		}
	}
	if len(earlier) > 0 {
		previous = &earlier[len(earlier)-1]
		previousAt = earlierTimes[len(earlierTimes)-1]
		if !effective.After(previousAt) {
			return fmt.Errorf("%w: effective_at must follow its predecessor", ErrPolicyTiming)
		}
	}
	committeeOnly := previous != nil && sameNonCommitteePolicy(*previous, p)
	if previous == nil || !committeeOnly {
		if !(CalendarQuarters{}).IsQuarterEnd(effective) || approved.After(effective.Add(-60*24*time.Hour)) {
			return fmt.Errorf("%w: quarter start and 60 days' notice required", ErrPolicyTiming)
		}
	} else if approved.After(effective.Add(-14 * 24 * time.Hour)) {
		return fmt.Errorf("%w: committee replacement requires 14 days' notice", ErrPolicyTiming)
	}

	monitorsChanged := previous != nil && !sameMonitorSet(previous.Monitors, p.Monitors)
	if monitorsChanged {
		if err := CheckCommitteeChange(previous.Monitors, p.Monitors); err != nil {
			return err
		}
		lastChange := earlierTimes[0]
		for i := len(earlier) - 1; i > 0; i-- {
			if sameMonitorSet(earlier[i-1].Monitors, earlier[i].Monitors) {
				continue
			}
			lastChange = earlierTimes[i]
			break
		}
		if effective.Before(lastChange.Add(30 * 24 * time.Hour)) {
			return fmt.Errorf("%w: latest committee change was at %s", ErrCommitteeSpacing, lastChange)
		}
	}
	if rv == nil {
		return fmt.Errorf("%w: verifier is required", ErrReleaseApproval)
	}
	unsigned, err := policyValueWithout(p, "release_signatures")
	if err != nil {
		return fmt.Errorf("%w: encode unsigned policy: %v", ErrPolicyInvalid, err)
	}
	if err := rv.VerifyRelease("distribution-policy", unsigned, p.ReleaseSignatures, approved, p); err != nil {
		return fmt.Errorf("%w: %w", ErrReleaseApproval, err)
	}
	return nil
}

func policyValueWithout(p wire.DistributionPolicy, omit string) (any, error) {
	encoded, err := canon.EncodeWithout(p, omit)
	if err != nil {
		return nil, err
	}
	return canon.Parse(encoded, canon.Limits{MaxDepth: 32})
}

func sameNonCommitteePolicy(a, b wire.DistributionPolicy) bool {
	return a.VaultShareBP == b.VaultShareBP && a.MonitorShareBP == b.MonitorShareBP && a.MaxRetentionYears == b.MaxRetentionYears && a.MinReports == b.MinReports
}

func sameMonitorSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	left, right := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(left)
	sort.Strings(right)
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// CheckCommitteeChange enforces the settlement safety bounds for a monitor-set transition.
func CheckCommitteeChange(old, next []string) error {
	oldSet := make(map[string]struct{}, len(old))
	nextSet := make(map[string]struct{}, len(next))
	for _, did := range old {
		oldSet[did] = struct{}{}
	}
	for _, did := range next {
		nextSet[did] = struct{}{}
	}
	removed, added := 0, 0
	for did := range oldSet {
		if _, ok := nextSet[did]; !ok {
			removed++
		}
	}
	for did := range nextSet {
		if _, ok := oldSet[did]; !ok {
			added++
		}
	}
	if removed == 0 && added == 0 {
		return nil
	}
	n := len(oldSet)
	if n == 0 {
		return fmt.Errorf("%w: empty old committee", ErrCommitteeChange)
	}
	q, f := 2*n/3+1, (n-1)/3
	limit := q - f - 1
	if len(oldSet) == len(nextSet) && removed <= limit || removed == 0 && added <= limit {
		return nil
	}
	return fmt.Errorf("%w: removed %d and added %d", ErrCommitteeChange, removed, added)
}

// InForce returns the policy with the greatest valid effective_at not after t.
func InForce(policies []wire.DistributionPolicy, t time.Time) (wire.DistributionPolicy, bool) {
	var selected wire.DistributionPolicy
	var selectedAt time.Time
	found := false
	for _, p := range policies {
		effective, err := wire.ParseTime(p.EffectiveAt)
		if err == nil && !effective.After(t) && (!found || effective.After(selectedAt)) {
			selected, selectedAt, found = p, effective, true
		}
	}
	return selected, found
}

// Interval is a half-open period during which a monitor belongs to the committee.
type Interval struct{ Start, End time.Time }

// Membership returns each monitor's committee intervals within [start, end).
func Membership(policies []wire.DistributionPolicy, start, end time.Time) map[string][]Interval {
	result := make(map[string][]Interval)
	if !start.Before(end) {
		return result
	}
	boundaries := []time.Time{start, end}
	for _, p := range policies {
		at, err := wire.ParseTime(p.EffectiveAt)
		if err == nil && at.After(start) && at.Before(end) {
			boundaries = append(boundaries, at)
		}
	}
	sort.Slice(boundaries, func(i, j int) bool { return boundaries[i].Before(boundaries[j]) })
	unique := boundaries[:0]
	for _, at := range boundaries {
		if len(unique) == 0 || !unique[len(unique)-1].Equal(at) {
			unique = append(unique, at)
		}
	}
	for i := 0; i+1 < len(unique); i++ {
		p, ok := InForce(policies, unique[i])
		if !ok {
			continue
		}
		for _, did := range p.Monitors {
			intervals := result[did]
			n := len(intervals)
			if n > 0 && intervals[n-1].End.Equal(unique[i]) {
				intervals[n-1].End = unique[i+1]
				result[did] = intervals
			} else {
				result[did] = append(intervals, Interval{Start: unique[i], End: unique[i+1]})
			}
		}
	}
	return result
}
