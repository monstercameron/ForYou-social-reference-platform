package foundation

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"sort"
	"time"

	"foryou.local/foryou/internal/distribution"
	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/fee"
	"foryou.local/foryou/internal/governance"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

// Config contains the verified inputs and local dependencies for a Foundation service.
type Config struct {
	// Dev selects the local devnet mode without weakening verification.
	Dev     bool
	Key     ed25519.PrivateKey
	Genesis wire.GenesisManifest
	// Certificates returns the Foundation certificates issued by now.
	Certificates func(now time.Time) []wire.FoundationKeyCertificate
	// Closures contains release-approved closures of Foundation keys.
	Closures      []wire.FoundationKeyClosure
	KeySetUpdates []wire.ReleaseKeySetUpdate
	FeeSchedules  []wire.ApprovedFeeSchedule
	Policies      []wire.DistributionPolicy
	Calendar      distribution.Calendar
	DataDir       string
	Clock         func() time.Time
	// HeadInterval defaults to one hour and cannot exceed 24 hours.
	HeadInterval time.Duration
	// Faux is the chain interface used for Foundation payments.
	Faux faux.Chain
	// Providers lists provider base URLs used for settlement and distribution.
	Providers []string
	// Monitors maps monitor DIDs to their committee service base URLs.
	Monitors map[string]string
	// MonitorPayouts maps monitor DIDs and networks to payout addresses; the specification names no source, so devnet supplies these addresses.
	MonitorPayouts map[string]map[string]string
}

func (cfg Config) validate(now time.Time) (*governance.Trust, error) {
	if len(cfg.Key) != ed25519.PrivateKeySize || keys.DIDOf(cfg.Key) == "" {
		return nil, errors.New("foundation: invalid Foundation private key")
	}
	if cfg.DataDir == "" || cfg.Calendar == nil {
		return nil, errors.New("foundation: data directory and calendar are required")
	}
	if validator, ok := cfg.Calendar.(interface{ Validate() error }); ok {
		if err := validator.Validate(); err != nil {
			return nil, fmt.Errorf("foundation: calendar: %w", err)
		}
	}
	if cfg.Clock == nil {
		return nil, errors.New("foundation: clock is required")
	}
	if cfg.HeadInterval <= 0 || cfg.HeadInterval > 24*time.Hour {
		return nil, errors.New("foundation: head interval must be positive and no greater than 24 hours")
	}

	certificates := []wire.FoundationKeyCertificate(nil)
	if cfg.Certificates != nil {
		certificates = cfg.Certificates(now)
	}
	trust, err := governance.NewTrust(cfg.Genesis, cfg.KeySetUpdates, certificates, cfg.Closures, nil)
	if err != nil {
		return nil, fmt.Errorf("foundation: trust: %w", err)
	}
	if !hasCertificateForKey(cfg.Genesis, certificates, keys.DIDOf(cfg.Key)) {
		return nil, ErrUncertifiedFoundationKey
	}
	if err := validateSchedules(cfg); err != nil {
		return nil, err
	}
	for i, policy := range cfg.Policies {
		if err := distribution.CheckPolicy(policy, cfg.Policies[:i], trust); err != nil {
			return nil, fmt.Errorf("foundation: policy %d: %w", i+1, err)
		}
	}
	return trust, nil
}

func hasCertificateForKey(genesis wire.GenesisManifest, certificates []wire.FoundationKeyCertificate, id string) bool {
	for _, certificate := range genesis.FoundationKeyCertificates {
		if certificate.FoundationID == id {
			return true
		}
	}
	for _, certificate := range certificates {
		if certificate.FoundationID == id {
			return true
		}
	}
	return false
}

func validateSchedules(cfg Config) error {
	schedules := make([]wire.FeeSchedule, 0, len(cfg.FeeSchedules))
	for _, approved := range cfg.FeeSchedules {
		schedules = append(schedules, approved.Schedule)
	}
	if err := fee.CheckScheduleSet(schedules); err != nil {
		return fmt.Errorf("foundation: fee schedule set: %w", err)
	}
	sort.Slice(schedules, func(i, j int) bool {
		if schedules[i].Network == schedules[j].Network {
			return schedules[i].Revision < schedules[j].Revision
		}
		return schedules[i].Network < schedules[j].Network
	})
	set, err := governance.CheckGenesis(cfg.Genesis)
	if err != nil {
		return fmt.Errorf("foundation: genesis release set: %w", err)
	}
	timeline, err := governance.NewTimeline(set, cfg.KeySetUpdates)
	if err != nil {
		return fmt.Errorf("foundation: release timeline: %w", err)
	}
	previous := make(map[string]*wire.FeeSchedule)
	for _, schedule := range schedules {
		var approval *wire.FeeScheduleApproval
		for i := range cfg.FeeSchedules {
			if cfg.FeeSchedules[i].Schedule.ScheduleID == schedule.ScheduleID {
				approval = &cfg.FeeSchedules[i].Approval
				break
			}
		}
		if approval == nil {
			return fmt.Errorf("foundation: missing approval for schedule %s", schedule.ScheduleID)
		}
		approvedAt, err := wire.ParseTime(approval.ApprovedAt)
		if err != nil {
			return fmt.Errorf("foundation: schedule approval time: %w", err)
		}
		releaseSet, err := timeline.SetAt(approvedAt, approval)
		if err != nil {
			return fmt.Errorf("foundation: schedule approval key set: %w", err)
		}
		if err := fee.VerifyApproval(schedule, *approval, releaseSet.IDs, releaseSet.Threshold, previous[schedule.Network]); err != nil {
			return fmt.Errorf("foundation: schedule %s: %w", schedule.ScheduleID, err)
		}
		copyOfSchedule := schedule
		previous[schedule.Network] = &copyOfSchedule
	}
	return nil
}

func cloneConfig(cfg Config) Config {
	cfg.Key = append(ed25519.PrivateKey(nil), cfg.Key...)
	cfg.Genesis.ReleaseKeyIDs = append([]string(nil), cfg.Genesis.ReleaseKeyIDs...)
	cfg.Genesis.FoundationKeyCertificates = cloneCertificates(cfg.Genesis.FoundationKeyCertificates)
	cfg.Genesis.FeeSchedules = append([]wire.FeeSchedule(nil), cfg.Genesis.FeeSchedules...)
	cfg.Genesis.FeeScheduleApprovals = cloneApprovals(cfg.Genesis.FeeScheduleApprovals)
	cfg.Genesis.DistributionPolicy = clonePolicy(cfg.Genesis.DistributionPolicy)
	cfg.Genesis.ReleaseSignatures = cloneSignatures(cfg.Genesis.ReleaseSignatures)
	cfg.Closures = append([]wire.FoundationKeyClosure(nil), cfg.Closures...)
	for i := range cfg.Closures {
		cfg.Closures[i].Voided = cloneStrings(cfg.Closures[i].Voided)
		cfg.Closures[i].ReleaseSignatures = cloneSignatures(cfg.Closures[i].ReleaseSignatures)
	}
	cfg.KeySetUpdates = append([]wire.ReleaseKeySetUpdate(nil), cfg.KeySetUpdates...)
	for i := range cfg.KeySetUpdates {
		cfg.KeySetUpdates[i].ReleaseKeyIDs = append([]string(nil), cfg.KeySetUpdates[i].ReleaseKeyIDs...)
		cfg.KeySetUpdates[i].PriorObjects = cloneStrings(cfg.KeySetUpdates[i].PriorObjects)
		cfg.KeySetUpdates[i].ReleaseSignatures = cloneSignatures(cfg.KeySetUpdates[i].ReleaseSignatures)
		if cfg.KeySetUpdates[i].PreviousUpdateID != nil {
			previous := *cfg.KeySetUpdates[i].PreviousUpdateID
			cfg.KeySetUpdates[i].PreviousUpdateID = &previous
		}
	}
	cfg.FeeSchedules = append([]wire.ApprovedFeeSchedule(nil), cfg.FeeSchedules...)
	for i := range cfg.FeeSchedules {
		cfg.FeeSchedules[i].Approval.ReleaseSignatures = cloneSignatures(cfg.FeeSchedules[i].Approval.ReleaseSignatures)
	}
	cfg.Policies = append([]wire.DistributionPolicy(nil), cfg.Policies...)
	for i := range cfg.Policies {
		cfg.Policies[i] = clonePolicy(cfg.Policies[i])
	}
	cfg.Providers = append([]string(nil), cfg.Providers...)
	monitors := make(map[string]string, len(cfg.Monitors))
	for id, address := range cfg.Monitors {
		monitors[id] = address
	}
	cfg.Monitors = monitors
	payouts := make(map[string]map[string]string, len(cfg.MonitorPayouts))
	for id, addresses := range cfg.MonitorPayouts {
		payouts[id] = make(map[string]string, len(addresses))
		for network, address := range addresses {
			payouts[id][network] = address
		}
	}
	cfg.MonitorPayouts = payouts
	return cfg
}

func cloneCertificates(values []wire.FoundationKeyCertificate) []wire.FoundationKeyCertificate {
	result := append([]wire.FoundationKeyCertificate(nil), values...)
	for i := range result {
		result[i].ReleaseSignatures = cloneSignatures(result[i].ReleaseSignatures)
	}
	return result
}

func cloneApprovals(values []wire.FeeScheduleApproval) []wire.FeeScheduleApproval {
	result := append([]wire.FeeScheduleApproval(nil), values...)
	for i := range result {
		result[i].ReleaseSignatures = cloneSignatures(result[i].ReleaseSignatures)
	}
	return result
}

func clonePolicy(policy wire.DistributionPolicy) wire.DistributionPolicy {
	policy.Monitors = append([]string(nil), policy.Monitors...)
	policy.ReleaseSignatures = cloneSignatures(policy.ReleaseSignatures)
	return policy
}

func cloneSignatures(values []wire.ReleaseSignature) []wire.ReleaseSignature {
	return append([]wire.ReleaseSignature(nil), values...)
}

// cloneStrings keeps an empty non-nil slice empty, because the specification encodes it as an array, never null.
func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append(make([]string, 0, len(values)), values...)
}
