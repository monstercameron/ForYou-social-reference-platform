package monitor

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/committee"
	"foryou.local/foryou/internal/distribution"
	"foryou.local/foryou/internal/fee"
	"foryou.local/foryou/internal/governance"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/store"
	"foryou.local/foryou/internal/wire"
)

const (
	viewPolicyCollection   = "view.policies"
	viewScheduleCollection = "view.schedules"
)

var policyValues = store.Collection[wire.DistributionPolicy]{Name: viewPolicyCollection}
var scheduleValues = store.Collection[wire.ApprovedFeeSchedule]{Name: viewScheduleCollection}

// View holds verified governance trust, committee policies and fee schedules.
type View struct {
	mu                sync.RWMutex
	db                *store.DB
	trust             *governance.Trust
	genesis           wire.GenesisManifest
	policies          []wire.DistributionPolicy
	schedules         []wire.ApprovedFeeSchedule
	verifiedSchedules *committee.VerifiedSchedules
}

func openView(root string, genesis wire.GenesisManifest, trust *governance.Trust) (*View, error) {
	db, err := store.Open(filepath.Join(root, "view"))
	if err != nil {
		return nil, fmt.Errorf("monitor: open view journal: %w", err)
	}
	v := &View{db: db, trust: trust, genesis: genesis, policies: []wire.DistributionPolicy{genesis.DistributionPolicy}, schedules: approvedGenesisSchedules(genesis)}
	if err := db.View(func(tx store.Tx) error {
		return policyValues.Scan(tx, store.Range{}, func(_ string, p wire.DistributionPolicy) (bool, error) {
			v.policies = append(v.policies, p)
			return true, nil
		})
	}); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := db.View(func(tx store.Tx) error {
		return scheduleValues.Scan(tx, store.Range{}, func(_ string, s wire.ApprovedFeeSchedule) (bool, error) {
			v.schedules = append(v.schedules, s)
			return true, nil
		})
	}); err != nil {
		_ = db.Close()
		return nil, err
	}
	sort.Slice(v.policies, func(i, j int) bool { return v.policies[i].EffectiveAt < v.policies[j].EffectiveAt })
	sort.Slice(v.schedules, func(i, j int) bool { return v.schedules[i].Schedule.EffectiveAt < v.schedules[j].Schedule.EffectiveAt })
	if err := v.verifyPolicies(); err != nil {
		_ = db.Close()
		return nil, err
	}
	v.verifiedSchedules, err = verifyScheduleView(v.schedules, genesis)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("monitor: verify fee schedules: %w", err)
	}
	return v, nil
}

func verifyScheduleView(entries []wire.ApprovedFeeSchedule, genesis wire.GenesisManifest) (*committee.VerifiedSchedules, error) {
	ordered := append([]wire.ApprovedFeeSchedule(nil), entries...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Schedule.EffectiveAt == ordered[j].Schedule.EffectiveAt {
			return ordered[i].Schedule.Network < ordered[j].Schedule.Network
		}
		return ordered[i].Schedule.EffectiveAt < ordered[j].Schedule.EffectiveAt
	})
	previous := make(map[string]wire.FeeSchedule)
	complete := make([]wire.ApprovedFeeSchedule, 0, len(ordered))
	for _, entry := range ordered {
		schedule := entry.Schedule
		prior, hasPrior := previous[schedule.Network]
		var priorPtr *wire.FeeSchedule
		if hasPrior {
			priorPtr = &prior
		}
		if err := fee.VerifyApproval(schedule, entry.Approval, genesis.ReleaseKeyIDs, int(genesis.ReleaseThreshold), priorPtr); err != nil {
			return nil, err
		}
		previous[schedule.Network] = schedule
		if schedule.Revision == 1 || hasSchedulePartner(ordered, schedule) {
			complete = append(complete, entry)
		}
	}
	return committee.NewSchedules(complete, genesis.ReleaseKeyIDs, int(genesis.ReleaseThreshold))
}

func hasSchedulePartner(entries []wire.ApprovedFeeSchedule, schedule wire.FeeSchedule) bool {
	partnerNetwork := wire.BitcoinMainnet
	if schedule.Network == wire.BitcoinMainnet {
		partnerNetwork = wire.EthereumMainnet
	}
	for _, entry := range entries {
		if entry.Schedule.Network == partnerNetwork && entry.Schedule.Revision == schedule.Revision && entry.Schedule.EffectiveAt == schedule.EffectiveAt {
			return true
		}
	}
	return false
}

func approvedGenesisSchedules(genesis wire.GenesisManifest) []wire.ApprovedFeeSchedule {
	entries := make([]wire.ApprovedFeeSchedule, len(genesis.FeeSchedules))
	for i, schedule := range genesis.FeeSchedules {
		entries[i] = wire.ApprovedFeeSchedule{Schedule: schedule, Approval: genesis.FeeScheduleApprovals[i]}
	}
	return entries
}

func (v *View) verifyPolicies() error {
	verified := make([]wire.DistributionPolicy, 0, len(v.policies))
	for _, policy := range v.policies {
		if len(verified) == 0 && policy.PolicyID == v.genesis.DistributionPolicy.PolicyID {
			verified = append(verified, policy)
			continue
		}
		if policy.PolicyID == v.genesis.DistributionPolicy.PolicyID {
			continue
		}
		if err := distribution.CheckPolicy(policy, verified, v.trust); err != nil {
			return err
		}
		verified = append(verified, policy)
	}
	if len(verified) == 0 || verified[0].PolicyID != v.genesis.DistributionPolicy.PolicyID {
		return errors.New("monitor: missing genesis distribution policy")
	}
	v.policies = verified
	return nil
}

// AddPolicy verifies and durably adds a distribution policy without changing the view on failure.
func (v *View) AddPolicy(policy wire.DistributionPolicy) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, current := range v.policies {
		if current.PolicyID == policy.PolicyID {
			oldBytes, oldErr := canon.Encode(current)
			newBytes, newErr := canon.Encode(policy)
			if oldErr == nil && newErr == nil && bytes.Equal(oldBytes, newBytes) {
				return nil
			}
			return errors.New("monitor: conflicting distribution policy ID")
		}
	}
	history := append([]wire.DistributionPolicy(nil), v.policies...)
	if err := distribution.CheckPolicy(policy, history, v.trust); err != nil {
		return err
	}
	if err := v.db.Update(func(tx store.Tx) error { return policyValues.Put(tx, policy.PolicyID, policy) }); err != nil {
		return err
	}
	v.policies = append(v.policies, policy)
	sort.Slice(v.policies, func(i, j int) bool { return v.policies[i].EffectiveAt < v.policies[j].EffectiveAt })
	return nil
}

// AddSchedule verifies and durably adds a release-approved fee schedule.
func (v *View) AddSchedule(entry wire.ApprovedFeeSchedule) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, current := range v.schedules {
		if current.Schedule.ScheduleID == entry.Schedule.ScheduleID {
			oldBytes, oldErr := canon.Encode(current)
			newBytes, newErr := canon.Encode(entry)
			if oldErr == nil && newErr == nil && bytes.Equal(oldBytes, newBytes) {
				return nil
			}
			return errors.New("monitor: conflicting fee schedule ID")
		}
	}
	candidate := append(append([]wire.ApprovedFeeSchedule(nil), v.schedules...), entry)
	verified, err := verifyScheduleView(candidate, v.genesis)
	if err != nil {
		return err
	}
	if err := v.db.Update(func(tx store.Tx) error { return scheduleValues.Put(tx, entry.Schedule.ScheduleID, entry) }); err != nil {
		return err
	}
	v.schedules, v.verifiedSchedules = candidate, verified
	return nil
}

// CommitteeAt returns the committee in force at t.
func (v *View) CommitteeAt(t time.Time) []string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	policy, ok := distribution.InForce(v.policies, t)
	if !ok {
		return nil
	}
	return append([]string(nil), policy.Monitors...)
}

// PreviousCommittee returns the prior committee and effective time for 30 days after a committee change.
func (v *View) PreviousCommittee(t time.Time) ([]string, time.Time, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	for i := len(v.policies) - 1; i > 0; i-- {
		changeAt, err := wire.ParseTime(v.policies[i].EffectiveAt)
		if err != nil || t.Before(changeAt) {
			continue
		}
		if sameDIDs(v.policies[i-1].Monitors, v.policies[i].Monitors) {
			continue
		}
		if !t.Before(changeAt.Add(30 * 24 * time.Hour)) {
			return nil, changeAt, false
		}
		return append([]string(nil), v.policies[i-1].Monitors...), changeAt, true
	}
	return nil, time.Time{}, false
}

// IsMember reports membership under the policy effective at t.
func (v *View) IsMember(did string, t time.Time) bool {
	for _, member := range v.CommitteeAt(t) {
		if member == did {
			return true
		}
	}
	return false
}

// JoinedAt returns the effective time of the policy change that most recently named did.
func (v *View) JoinedAt(did string, t time.Time) (time.Time, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	for i := len(v.policies) - 1; i > 0; i-- {
		at, err := wire.ParseTime(v.policies[i].EffectiveAt)
		if err != nil || at.After(t) {
			continue
		}
		if containsDID(v.policies[i].Monitors, did) && !containsDID(v.policies[i-1].Monitors, did) {
			return at, true
		}
	}
	return time.Time{}, false
}

// Schedules returns the verified fee schedule adapter used by committee monitors.
func (v *View) Schedules() settlement.Schedules {
	return scheduleSource{view: v}
}

type scheduleSource struct{ view *View }

func (s scheduleSource) Lookup(id string) (settlement.Schedule, bool) {
	s.view.mu.RLock()
	defer s.view.mu.RUnlock()
	return s.view.verifiedSchedules.Lookup(id)
}

func (s scheduleSource) InForceWithin(id, network string, at time.Time) bool {
	s.view.mu.RLock()
	defer s.view.mu.RUnlock()
	return s.view.verifiedSchedules.InForceWithin(id, network, at)
}

// Trust returns the verified Foundation trust view.
func (v *View) Trust() *governance.Trust {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.trust
}

// SetTrust replaces the verified Foundation trust used for later policy checks.
func (v *View) SetTrust(trust *governance.Trust) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.trust = trust
}

// Close flushes and closes the view journal.
func (v *View) Close() error { return v.db.Close() }

func sameDIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	left := append([]string(nil), a...)
	right := append([]string(nil), b...)
	sort.Strings(left)
	sort.Strings(right)
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
func containsDID(values []string, did string) bool {
	for _, value := range values {
		if value == did {
			return true
		}
	}
	return false
}
