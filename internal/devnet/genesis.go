package devnet

import (
	"fmt"
	"sync"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/distribution"
	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/fee"
	"foryou.local/foryou/internal/governance"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

const (
	genesisTime = "2026-07-01T00:00:00Z"
	policyStart = "2026-10-01T00:00:00Z"
	scheduleEnd = "2027-04-01T00:00:00Z"
	genesisEnd  = "2027-07-02T00:00:00Z"
)

var (
	genesisOnce sync.Once
	genesisRoot wire.GenesisManifest
)

// Genesis returns the deterministically signed development genesis manifest.
func Genesis() wire.GenesisManifest {
	genesisOnce.Do(func() {
		var err error
		genesisRoot, err = buildGenesis()
		if err == nil {
			err = selfCheck(genesisRoot)
		}
		if err != nil {
			panic(fmt.Sprintf("devnet: invalid deterministic genesis: %v", err))
		}
	})
	return cloneGenesis(genesisRoot)
}

// Certificates returns the genesis certificate and every deterministic renewal issued by now.
func Certificates(now time.Time) []wire.FoundationKeyCertificate {
	Genesis()
	result := []wire.FoundationKeyCertificate{cloneCertificate(genesisRoot.FoundationKeyCertificates[0])}
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	for renewal := int64(1); ; renewal++ {
		validFrom := base.AddDate(0, 0, int(365*renewal))
		issuedAt := validFrom.AddDate(0, 0, -30)
		if issuedAt.After(now) {
			return result
		}
		certificate, err := makeCertificate(wire.FormatTime(issuedAt), wire.FormatTime(validFrom), wire.FormatTime(validFrom.AddDate(0, 0, 366)))
		if err != nil {
			panic(fmt.Sprintf("devnet: build deterministic certificate renewal: %v", err))
		}
		result = append(result, certificate)
	}
}

// FeeSchedules returns both approved schedule 1 network schedules.
func FeeSchedules() []wire.ApprovedFeeSchedule {
	genesis := Genesis()
	result := make([]wire.ApprovedFeeSchedule, len(genesis.FeeSchedules))
	for i := range genesis.FeeSchedules {
		result[i] = wire.ApprovedFeeSchedule{Schedule: genesis.FeeSchedules[i], Approval: genesis.FeeScheduleApprovals[i]}
		result[i].Approval.ReleaseSignatures = append([]wire.ReleaseSignature(nil), genesis.FeeScheduleApprovals[i].ReleaseSignatures...)
	}
	return result
}

// Policies returns the release-approved development distribution policy history.
func Policies() []wire.DistributionPolicy {
	policy := Genesis().DistributionPolicy
	policy.Monitors = append([]string(nil), policy.Monitors...)
	policy.ReleaseSignatures = append([]wire.ReleaseSignature(nil), policy.ReleaseSignatures...)
	return []wire.DistributionPolicy{policy}
}

// Disclosures returns the development Foundation's statement about each committee monitor of each policy.
func Disclosures() []wire.MonitorDisclosure {
	var disclosures []wire.MonitorDisclosure
	for _, policy := range Policies() {
		for _, monitor := range policy.Monitors {
			disclosures = append(disclosures, wire.MonitorDisclosure{
				PolicyID: policy.PolicyID, MonitorID: monitor,
				SelectionCriteria: "Development network: the committee is five fixed keys derived from the public dev seed. They were not selected for independence.",
				Relationships:     []wire.MonitorRelationship{{Party: "foundation", Kind: "ownership", Description: "One operator runs the dev Foundation, every dev monitor and the dev provider on one machine."}},
			})
		}
	}
	return disclosures
}

// Trust builds the verified development trust state with renewals issued by now.
func Trust(now time.Time) (*governance.Trust, error) {
	genesis := Genesis()
	certificates := Certificates(now)
	return governance.NewTrust(genesis, nil, certificates[1:], nil, nil)
}

func buildGenesis() (wire.GenesisManifest, error) {
	signers := ReleaseKeys()
	ids := releaseIDs()
	certificate, err := makeCertificate(genesisTime, genesisTime, genesisEnd)
	if err != nil {
		return wire.GenesisManifest{}, err
	}
	bitcoin, err := makeSchedule(faux.BitcoinMainnet, "millisatoshi", "136")
	if err != nil {
		return wire.GenesisManifest{}, err
	}
	ethereum, err := makeSchedule(faux.EthereumMainnet, "wei", "48900000000")
	if err != nil {
		return wire.GenesisManifest{}, err
	}
	schedules := []wire.FeeSchedule{bitcoin, ethereum}
	approvals := make([]wire.FeeScheduleApproval, len(schedules))
	for i, schedule := range schedules {
		approval := wire.FeeScheduleApproval{ScheduleID: schedule.ScheduleID, ApprovedAt: genesisTime}
		unsigned, err := canonicalWithout(approval, "release_signatures")
		if err != nil {
			return wire.GenesisManifest{}, err
		}
		approval.ReleaseSignatures, err = governance.SignRelease("fee-schedule-approval", unsigned, signers)
		if err != nil {
			return wire.GenesisManifest{}, err
		}
		approvals[i] = approval
	}
	policy := wire.DistributionPolicy{Revision: 1, VaultShareBP: 5000, MonitorShareBP: 500, MaxRetentionYears: 10, MinReports: 3, Monitors: monitorIDs(), ApprovedAt: genesisTime, EffectiveAt: policyStart}
	policy.PolicyID, err = distribution.PolicyID(policy)
	if err != nil {
		return wire.GenesisManifest{}, err
	}
	unsignedPolicy, err := canonicalWithout(policy, "release_signatures")
	if err != nil {
		return wire.GenesisManifest{}, err
	}
	policy.ReleaseSignatures, err = governance.SignRelease("distribution-policy", unsignedPolicy, signers)
	if err != nil {
		return wire.GenesisManifest{}, err
	}
	manifest := wire.GenesisManifest{Release: "1.0", ReleaseKeyIDs: ids, ReleaseThreshold: 4, FoundationKeyCertificates: []wire.FoundationKeyCertificate{certificate}, FeeSchedules: schedules, FeeScheduleApprovals: approvals, DistributionPolicy: policy, SignedAt: genesisTime}
	unsigned, err := canonicalWithout(manifest, "release_signatures")
	if err != nil {
		return wire.GenesisManifest{}, err
	}
	manifest.ReleaseSignatures, err = governance.SignRelease("genesis-manifest", unsigned, signers)
	if err != nil {
		return wire.GenesisManifest{}, err
	}
	return manifest, nil
}

func makeCertificate(issuedAt, validFrom, validUntil string) (wire.FoundationKeyCertificate, error) {
	certificate := wire.FoundationKeyCertificate{FoundationID: keys.DIDOf(FoundationKey()), Recipients: wire.FoundationRecipients{BitcoinMainnet: Recipient(Foundation0Label, faux.BitcoinMainnet), EthereumMainnet: Recipient(Foundation0Label, faux.EthereumMainnet)}, IssuedAt: issuedAt, ValidFrom: validFrom, ValidUntil: validUntil}
	var err error
	certificate.CertificateID, err = governance.CertificateID(certificate)
	if err != nil {
		return wire.FoundationKeyCertificate{}, err
	}
	unsigned, err := canonicalWithout(certificate, "release_signatures")
	if err != nil {
		return wire.FoundationKeyCertificate{}, err
	}
	certificate.ReleaseSignatures, err = governance.SignRelease("foundation-key-certificate", unsigned, ReleaseKeys())
	return certificate, err
}

func makeSchedule(network, denomination, rate string) (wire.FeeSchedule, error) {
	schedule := wire.FeeSchedule{ProfileMajor: 2, ProfileMinor: 0, Revision: 1, Network: network, Denomination: denomination, BaseFeeBaseUnits: "0", PerKiBFeeBaseUnits: rate, EffectiveAt: policyStart, ExpiresAt: scheduleEnd}
	var err error
	schedule.ScheduleID, err = fee.ScheduleID(schedule)
	return schedule, err
}

func canonicalWithout(value any, fields ...string) (any, error) {
	encoded, err := canon.EncodeWithout(value, fields...)
	if err != nil {
		return nil, fmt.Errorf("devnet: canonicalize deterministic object: %w", err)
	}
	unsigned, err := canon.Parse(encoded, canon.Limits{MaxDepth: 32})
	if err != nil {
		return nil, fmt.Errorf("devnet: parse deterministic object: %w", err)
	}
	return unsigned, nil
}

func selfCheck(manifest wire.GenesisManifest) error {
	set, err := governance.CheckGenesis(manifest)
	if err != nil {
		return fmt.Errorf("genesis verification: %w", err)
	}
	if err := faux.ValidateRecipient(faux.BitcoinMainnet, manifest.FoundationKeyCertificates[0].Recipients.BitcoinMainnet); err != nil {
		return err
	}
	if err := faux.ValidateRecipient(faux.EthereumMainnet, manifest.FoundationKeyCertificates[0].Recipients.EthereumMainnet); err != nil {
		return err
	}
	if err := fee.CheckScheduleSet(manifest.FeeSchedules); err != nil {
		return fmt.Errorf("fee schedule set: %w", err)
	}
	for i, schedule := range manifest.FeeSchedules {
		if err := fee.VerifyApproval(schedule, manifest.FeeScheduleApprovals[i], set.IDs, set.Threshold, nil); err != nil {
			return fmt.Errorf("fee schedule approval: %w", err)
		}
	}
	trust, err := governance.NewTrust(manifest, nil, nil, nil, nil)
	if err != nil {
		return fmt.Errorf("trust construction: %w", err)
	}
	if err := distribution.CheckPolicy(manifest.DistributionPolicy, nil, trust); err != nil {
		return fmt.Errorf("distribution policy: %w", err)
	}
	return nil
}

func cloneGenesis(manifest wire.GenesisManifest) wire.GenesisManifest {
	manifest.ReleaseKeyIDs = append([]string(nil), manifest.ReleaseKeyIDs...)
	manifest.FoundationKeyCertificates = append([]wire.FoundationKeyCertificate(nil), manifest.FoundationKeyCertificates...)
	for i := range manifest.FoundationKeyCertificates {
		manifest.FoundationKeyCertificates[i] = cloneCertificate(manifest.FoundationKeyCertificates[i])
	}
	manifest.FeeSchedules = append([]wire.FeeSchedule(nil), manifest.FeeSchedules...)
	manifest.FeeScheduleApprovals = append([]wire.FeeScheduleApproval(nil), manifest.FeeScheduleApprovals...)
	for i := range manifest.FeeScheduleApprovals {
		manifest.FeeScheduleApprovals[i].ReleaseSignatures = append([]wire.ReleaseSignature(nil), manifest.FeeScheduleApprovals[i].ReleaseSignatures...)
	}
	manifest.DistributionPolicy.Monitors = append([]string(nil), manifest.DistributionPolicy.Monitors...)
	manifest.DistributionPolicy.ReleaseSignatures = append([]wire.ReleaseSignature(nil), manifest.DistributionPolicy.ReleaseSignatures...)
	manifest.ReleaseSignatures = append([]wire.ReleaseSignature(nil), manifest.ReleaseSignatures...)
	return manifest
}

func cloneCertificate(certificate wire.FoundationKeyCertificate) wire.FoundationKeyCertificate {
	certificate.ReleaseSignatures = append([]wire.ReleaseSignature(nil), certificate.ReleaseSignatures...)
	return certificate
}
